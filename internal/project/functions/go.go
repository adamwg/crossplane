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
	"context"
	"io"
	"log"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/ko/pkg/build"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
)

// goBuilder builds functions written in Go using ko.
type goBuilder struct {
	baseImage    string
	transport    http.RoundTripper
	imageConfigs []v1alpha1.ImageConfig
}

func (b *goBuilder) Name() string {
	return "go"
}

func (b *goBuilder) match(fromFS afero.Fs) (bool, error) {
	return afero.Exists(fromFS, "go.mod")
}

func (b *goBuilder) Build(ctx context.Context, _ afero.Fs, architectures []string, osBasePath string) ([]v1.Image, error) {
	// ko logs using the Go standard library global logger without providing
	// any option to disable output. Disable output while we do our builds.
	log.SetOutput(io.Discard)

	platforms := make([]string, len(architectures))
	for i, arch := range architectures {
		platforms[i] = "linux/" + arch
	}

	builder, err := build.NewGo(ctx, osBasePath,
		build.WithBaseImages(func(_ context.Context, _ string) (name.Reference, build.Result, error) {
			baseImage := b.baseImage
			if len(b.imageConfigs) > 0 {
				baseImage = RewriteImage(b.baseImage, b.imageConfigs)
			}
			ref, err := name.ParseReference(baseImage, name.StrictValidation)
			if err != nil {
				return nil, nil, err
			}
			img, err := remote.Index(ref, remote.WithTransport(b.transport), remote.WithAuthFromKeychain(authn.DefaultKeychain))
			return ref, img, err
		}),
		build.WithPlatforms(platforms...),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct ko builder")
	}
	builder, err = build.NewCaching(builder)
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct caching builder")
	}

	path, err := builder.QualifyImport(".")
	if err != nil {
		return nil, errors.Wrap(err, "failed to determine go module path for function")
	}

	res, err := builder.Build(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build function")
	}

	var imgs []v1.Image
	switch out := res.(type) {
	case v1.ImageIndex:
		idx, err := out.IndexManifest()
		if err != nil {
			return nil, errors.Wrap(err, "failed to get index manifest")
		}

		imgs = make([]v1.Image, len(idx.Manifests))
		for i, desc := range idx.Manifests {
			img, err := out.Image(desc.Digest)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to get image %v from index", desc.Digest)
			}
			imgs[i] = img
		}

	case v1.Image:
		imgs = []v1.Image{out}

	default:
		return nil, errors.Errorf("ko builder returned unexpected type %T", res)
	}

	return imgs, nil
}

func newGoBuilder(imageConfigs []v1alpha1.ImageConfig) *goBuilder {
	return &goBuilder{
		baseImage:    "xpkg.upbound.io/upbound/provider-base@sha256:d23697e028f65fcc35886fe9e875069c071f637a79d65821830d6bc71c975391",
		transport:    http.DefaultTransport,
		imageConfigs: imageConfigs,
	}
}
