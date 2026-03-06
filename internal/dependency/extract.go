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

package dependency

import (
	"archive/tar"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	conregv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

const maxDecompressedSize = 200 * 1024 * 1024 // 200 MB

// extractCRDsFromLayers extracts CRD YAML files from OCI image layers into an
// in-memory filesystem.
func extractCRDsFromLayers(layers []conregv1.Layer) (afero.Fs, error) {
	resultFS := afero.NewMemMapFs()
	found := 0

	for _, layer := range layers {
		n, err := extractCRDsFromLayer(layer, resultFS)
		if err != nil {
			return nil, err
		}
		found += n
	}

	return resultFS, nil
}

func extractCRDsFromLayer(layer conregv1.Layer, resultFS afero.Fs) (int, error) {
	r, err := layer.Uncompressed()
	if err != nil {
		return 0, errors.Wrap(err, "cannot get uncompressed layer")
	}
	defer r.Close() //nolint:errcheck // nothing to do

	tr := tar.NewReader(r)
	found := 0

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return found, errors.Wrap(err, "failed to read tar entry")
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Only extract YAML files from crds/ directory.
		cleanPath := filepath.Clean(hdr.Name)
		if !isCRDPath(cleanPath) {
			continue
		}

		limitedReader := io.LimitReader(tr, maxDecompressedSize)
		content, err := io.ReadAll(limitedReader)
		if err != nil {
			return found, errors.Wrapf(err, "failed to read CRD file %s", cleanPath)
		}

		filename := filepath.Base(cleanPath)
		if err := afero.WriteFile(resultFS, filename, content, 0o644); err != nil {
			return found, errors.Wrapf(err, "failed to write CRD file %s", filename)
		}
		found++
	}

	return found, nil
}

func isCRDPath(path string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	for _, p := range parts {
		if p == "crds" {
			ext := strings.ToLower(filepath.Ext(path))
			return ext == ".yaml" || ext == ".yml"
		}
	}
	return false
}

// FormatRef formats a package reference with version.
func FormatRef(pkg, version string) string {
	if version == "" {
		return pkg
	}
	return fmt.Sprintf("%s:%s", pkg, version)
}
