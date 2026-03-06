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
	"io/fs"
	"net/http"
	"path/filepath"
	"slices"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
)

// goTemplatingBuilder builds "functions" written in go templating by injecting
// their code into a function-go-templating base image.
type goTemplatingBuilder struct {
	baseImage    string
	transport    http.RoundTripper
	imageConfigs []v1alpha1.ImageConfig
}

func (b *goTemplatingBuilder) Name() string {
	return "go-templating"
}

func (b *goTemplatingBuilder) match(fromFS afero.Fs) (bool, error) {
	goTemplatingExtensions := []string{
		".gotmpl",
		".tmpl",
	}

	matches := false
	err := afero.Walk(fromFS, ".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.Mode().IsDir() {
			return nil
		}

		if !info.Mode().IsRegular() {
			matches = false
			return fs.SkipAll
		}

		if !slices.Contains(goTemplatingExtensions, filepath.Ext(path)) {
			matches = false
			return fs.SkipAll
		}

		matches = true
		return nil
	})

	if errors.Is(err, fs.SkipAll) {
		err = nil
	}

	return matches, err
}

func (b *goTemplatingBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string, osBasePath string) ([]v1.Image, error) {
	baseImage := b.baseImage
	if len(b.imageConfigs) > 0 {
		baseImage = RewriteImage(b.baseImage, b.imageConfigs)
	}
	baseRef, err := name.NewTag(baseImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse go-templating base image tag")
	}

	images := make([]v1.Image, len(architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range architectures {
		eg.Go(func() error {
			baseImg, err := baseImageForArch(baseRef, arch, b.transport)
			if err != nil {
				return errors.Wrap(err, "failed to fetch go-templating base image")
			}

			src, err := FSToTar(fromFS, "/src",
				WithSymlinkBasePath(osBasePath),
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
				"FUNCTION_GO_TEMPLATING_DEFAULT_SOURCE": "/src",
			})
			if err != nil {
				return errors.Wrap(err, "failed to configure go-templating source path")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

func newGoTemplatingBuilder(imageConfigs []v1alpha1.ImageConfig) *goTemplatingBuilder {
	return &goTemplatingBuilder{
		transport:    http.DefaultTransport,
		baseImage:    "xpkg.upbound.io/upbound/function-go-templating-base:v0.9.0-13-gd1fa2e3",
		imageConfigs: imageConfigs,
	}
}
