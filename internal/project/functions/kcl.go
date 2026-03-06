/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package functions

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
	"github.com/crossplane/crossplane/v2/internal/xpkg"
)

const (
	crossplaneFunctionRunnerUID = 2000
	crossplaneFunctionRunnerGID = 2000
)

// kclBuilder builds functions written in KCL by injecting their code into a
// function-kcl base image.
type kclBuilder struct {
	baseImage    string
	transport    http.RoundTripper
	imageConfigs []v1alpha1.ImageConfig
}

func (b *kclBuilder) Name() string {
	return "kcl"
}

func (b *kclBuilder) match(fromFS afero.Fs) (bool, error) {
	return afero.Exists(fromFS, "kcl.mod")
}

func (b *kclBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string, osBasePath string) ([]v1.Image, error) {
	baseImage := b.baseImage
	if len(b.imageConfigs) > 0 {
		baseImage = RewriteImage(b.baseImage, b.imageConfigs)
	}
	baseRef, err := name.NewTag(baseImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse KCL base image tag")
	}

	images := make([]v1.Image, len(architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range architectures {
		eg.Go(func() error {
			baseImg, err := baseImageForArch(baseRef, arch, b.transport)
			if err != nil {
				return errors.Wrap(err, "failed to fetch KCL base image")
			}

			src, err := FSToTar(fromFS, "/src",
				WithSymlinkBasePath(osBasePath),
				WithUIDOverride(crossplaneFunctionRunnerUID),
				WithGIDOverride(crossplaneFunctionRunnerGID),
			)
			if err != nil {
				return errors.Wrap(err, "failed to tar layer contents")
			}

			codeLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(src)), nil
			})
			if err != nil {
				return errors.Wrap(err, "failed to create code layer")
			}

			img, err := mutate.AppendLayers(baseImg, codeLayer)
			if err != nil {
				return errors.Wrap(err, "failed to add code to image")
			}

			img, err = setImageEnvvars(img, map[string]string{
				"FUNCTION_KCL_DEFAULT_SOURCE": "/src",
				"KCL_PKG_PATH":                "/src",
			})
			if err != nil {
				return errors.Wrap(err, "failed to configure KCL source path")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

// baseImageForArch pulls the image with the given ref, and returns a version of
// it suitable for use as a function base image. Package and examples layers
// will be removed if present.
func baseImageForArch(ref name.Reference, arch string, transport http.RoundTripper) (v1.Image, error) {
	img, err := remote.Image(ref, remote.WithPlatform(v1.Platform{
		OS:           "linux",
		Architecture: arch,
	}), remote.WithTransport(transport), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return nil, errors.Wrap(err, "failed to pull image")
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config from image")
	}
	if cfg.Architecture != arch {
		return nil, errors.Errorf("image not available for architecture %q", arch)
	}

	mfst, err := img.Manifest()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get manifest from image")
	}
	baseImage := empty.Image
	cfg.RootFS = v1.RootFS{}
	cfg.History = nil
	baseImage, err = mutate.ConfigFile(baseImage, cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to add configuration to base image")
	}
	for _, desc := range mfst.Layers {
		if isNonBaseLayer(desc) {
			continue
		}
		l, err := img.LayerByDigest(desc.Digest)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get layer from image")
		}
		baseImage, err = mutate.AppendLayers(baseImage, l)
		if err != nil {
			return nil, errors.Wrap(err, "failed to add layer to base image")
		}
	}

	return baseImage, nil
}

func isNonBaseLayer(desc v1.Descriptor) bool {
	nonBaseLayerAnns := []string{
		xpkg.PackageAnnotation,
		xpkg.ExamplesAnnotation,
	}

	ann := desc.Annotations[xpkg.AnnotationKey]
	return slices.Contains(nonBaseLayerAnns, ann)
}

func setImageEnvvars(image v1.Image, envVars map[string]string) (v1.Image, error) {
	cfgFile, err := image.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config file")
	}
	cfg := cfgFile.Config

	for k, v := range envVars {
		cfg.Env = append(cfg.Env, fmt.Sprintf("%s=%s", k, v))
	}

	image, err = mutate.Config(image, cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to set config")
	}

	return image, nil
}

func newKCLBuilder(imageConfigs []v1alpha1.ImageConfig) *kclBuilder {
	return &kclBuilder{
		baseImage:    "xpkg.upbound.io/upbound/function-kcl-base:v0.11.2-up.1",
		transport:    http.DefaultTransport,
		imageConfigs: imageConfigs,
	}
}
