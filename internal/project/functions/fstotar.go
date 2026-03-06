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
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

type fsToTarConfig struct {
	symlinkBasePath *string
	uidOverride     *int
	gidOverride     *int
}

// FSToTarOption configures the behavior of FSToTar.
type FSToTarOption func(*fsToTarConfig)

// WithSymlinkBasePath provides the real base path of the filesystem, for use in
// symlink resolution.
func WithSymlinkBasePath(bp string) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.symlinkBasePath = &bp
	}
}

// WithUIDOverride sets the owner UID to use in the tar archive.
func WithUIDOverride(uid int) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.uidOverride = &uid
	}
}

// WithGIDOverride sets the owner GID to use in the tar archive.
func WithGIDOverride(gid int) FSToTarOption {
	return func(opts *fsToTarConfig) {
		opts.gidOverride = &gid
	}
}

// FSToTar produces a tarball of all the files in a filesystem. It supports
// following symlinks (even outside the given filesystem) if
// WithSymlinkBasePath is provided and the given filesystem is an
// afero.BasePathFs.
func FSToTar(f afero.Fs, prefix string, opts ...FSToTarOption) ([]byte, error) {
	cfg := &fsToTarConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	prefixHdr := &tar.Header{
		Name:     prefix,
		Typeflag: tar.TypeDir,
		Mode:     0o777,
	}
	if cfg.uidOverride != nil {
		prefixHdr.Uid = *cfg.uidOverride
	}
	if cfg.gidOverride != nil {
		prefixHdr.Gid = *cfg.gidOverride
	}

	err := tw.WriteHeader(prefixHdr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create prefix directory in tar archive")
	}
	err = walkFS(f, ".", func(name string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if cfg.symlinkBasePath == nil {
				return errors.New("cannot follow symlinks unless base path is configured")
			}

			return addSymlinkToTar(tw, prefix, name, cfg)
		}

		return addToTar(tw, prefix, f, name, info, cfg)
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to populate tar archive")
	}
	err = tw.Close()
	if err != nil {
		return nil, errors.Wrap(err, "failed to close tar archive")
	}

	return buf.Bytes(), nil
}

// walkFS walks the filesystem using forward slashes for path consistency.
func walkFS(f afero.Fs, root string, walkFn filepath.WalkFunc) error {
	root = filepath.ToSlash(root)
	if root == "" {
		root = "."
	}

	info, err := f.Stat(root)
	if err != nil {
		return walkFn(root, nil, err)
	}
	return walkFSImpl(f, root, info, walkFn)
}

func walkFSImpl(f afero.Fs, p string, info os.FileInfo, walkFn filepath.WalkFunc) error {
	err := walkFn(p, info, nil)
	if err != nil {
		if info.IsDir() && errors.Is(err, filepath.SkipDir) {
			return nil
		}
		return err
	}

	if !info.IsDir() {
		return nil
	}

	dir, err := f.Open(p)
	if err != nil {
		return walkFn(p, info, err)
	}
	defer dir.Close() //nolint:errcheck // Can't do anything useful with this error.

	list, err := dir.Readdir(-1)
	if err != nil {
		return walkFn(p, info, err)
	}

	for _, fileInfo := range list {
		filename := path.Join(p, fileInfo.Name())
		err = walkFSImpl(f, filename, fileInfo, walkFn)
		if err != nil {
			if !fileInfo.IsDir() || !errors.Is(err, filepath.SkipDir) {
				return err
			}
		}
	}
	return nil
}

func addToTar(tw *tar.Writer, prefix string, f afero.Fs, filename string, info fs.FileInfo, cfg *fsToTarConfig) error {
	fullPath := path.Join(prefix, filename)

	if info.IsDir() {
		if fullPath == prefix {
			return nil
		}

		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = fullPath
		if cfg.uidOverride != nil {
			h.Uid = *cfg.uidOverride
		}
		if cfg.gidOverride != nil {
			h.Gid = *cfg.gidOverride
		}
		return tw.WriteHeader(h)
	}

	if !info.Mode().IsRegular() {
		return errors.Errorf("unhandled file mode %v", info.Mode())
	}

	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = fullPath
	if cfg.uidOverride != nil {
		h.Uid = *cfg.uidOverride
	}
	if cfg.gidOverride != nil {
		h.Gid = *cfg.gidOverride
	}
	if err := tw.WriteHeader(h); err != nil {
		return err
	}

	file, err := f.Open(filename)
	if err != nil {
		return err
	}

	_, err = io.Copy(tw, file)
	return err
}

func addSymlinkToTar(tw *tar.Writer, prefix string, symlinkPath string, cfg *fsToTarConfig) error {
	osFs := afero.NewOsFs()

	targetPath, err := filepath.EvalSymlinks(filepath.Join(*cfg.symlinkBasePath, symlinkPath))
	if err != nil {
		// The symlink target may be missing. It's safe to skip.
		return nil //nolint:nilerr // See comment above.
	}

	exists, err := afero.Exists(osFs, targetPath)
	if err != nil || !exists {
		return err
	}

	return afero.Walk(osFs, targetPath, func(symlinkedFile string, symlinkedInfo fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if symlinkedInfo.IsDir() {
			return nil
		}

		targetHeader, err := tar.FileInfoHeader(symlinkedInfo, "")
		if err != nil {
			return err
		}

		relativePath, err := filepath.Rel(targetPath, symlinkedFile)
		if err != nil {
			return err
		}
		targetHeader.Name = path.Join(prefix, filepath.ToSlash(symlinkPath), filepath.ToSlash(relativePath))
		if cfg.uidOverride != nil {
			targetHeader.Uid = *cfg.uidOverride
		}
		if cfg.gidOverride != nil {
			targetHeader.Gid = *cfg.gidOverride
		}

		if err := tw.WriteHeader(targetHeader); err != nil {
			return err
		}

		targetFile, err := osFs.Open(symlinkedFile)
		if err != nil {
			return err
		}

		_, err = io.Copy(tw, targetFile)
		return err
	})
}
