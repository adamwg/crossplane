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
	"io"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
)

// pythonBuilder builds functions written in python by injecting their code into
// a function-python base image.
type pythonBuilder struct {
	baseImage    string
	packagePath  string
	transport    http.RoundTripper
	imageConfigs []v1alpha1.ImageConfig
}

func (b *pythonBuilder) Name() string {
	return "python"
}

func (b *pythonBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string, osBasePath string) ([]v1.Image, error) {
	baseImage := b.baseImage
	if len(b.imageConfigs) > 0 {
		baseImage = RewriteImage(b.baseImage, b.imageConfigs)
	}

	baseRef, err := name.NewTag(baseImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse python base image tag")
	}

	images := make([]v1.Image, len(architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range architectures {
		eg.Go(func() error {
			baseImg, err := remote.Image(baseRef, remote.WithPlatform(v1.Platform{
				OS:           "linux",
				Architecture: arch,
			}), remote.WithTransport(b.transport), remote.WithAuthFromKeychain(authn.DefaultKeychain))
			if err != nil {
				return errors.Wrap(err, "failed to fetch python base image")
			}

			src, err := FSToTar(fromFS, b.packagePath,
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

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

func (b *pythonBuilder) match(fromFS afero.Fs) (bool, error) {
	return afero.Exists(fromFS, "main.py")
}

func newPythonBuilder(imageConfigs []v1alpha1.ImageConfig) *pythonBuilder {
	return &pythonBuilder{
		baseImage:    "xpkg.upbound.io/upbound/function-interpreter-python:v0.6.1",
		packagePath:  "/venv/fn/lib/python3.11/site-packages/function",
		transport:    http.DefaultTransport,
		imageConfigs: imageConfigs,
	}
}
