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

package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/crossplane/v2/internal/terminal"
)

const projectFileName = "crossplane-project.yaml"

// initCmd initializes a new project.
type initCmd struct {
	Name      string `arg:""                                                    help:"The name of the new project."`
	Template  string `help:"A template URL or well-known name."                 short:"t"`
	Directory string `help:"Directory to initialize. Defaults to project name." short:"d"                           type:"path"`
	RefName   string `help:"Branch or tag to clone from template."              short:"b"`
}

func (c *initCmd) Run(k *kong.Context, logger logging.Logger) error {
	// Validate the project name is a valid DNS-1035 label.
	if errs := validation.IsDNS1035Label(c.Name); len(errs) > 0 {
		return errors.Errorf("'%s' is not a valid project name. DNS-1035 constraints: %s", c.Name, strings.Join(errs, "; "))
	}

	if c.Directory == "" {
		c.Directory = c.Name
	}

	// Check if the target directory is suitable.
	if err := c.checkTargetDirectory(); err != nil {
		return err
	}

	pretty := term.IsTerminal(int(os.Stderr.Fd()))
	sp := terminal.NewSpinnerPrinter(os.Stderr, pretty)

	if c.Template != "" {
		return c.initFromTemplate(k, logger, sp)
	}

	return c.initMinimal(k, sp)
}

func (c *initCmd) checkTargetDirectory() error {
	f, err := os.Stat(c.Directory)
	switch {
	case os.IsNotExist(err):
		return nil // Will be created
	case err != nil:
		return errors.Wrapf(err, "failed to stat directory %s", c.Directory)
	case !f.IsDir():
		return errors.Errorf("path %s is not a directory", c.Directory)
	}

	entries, err := os.ReadDir(c.Directory)
	if err != nil {
		return errors.Wrapf(err, "failed to read directory %s", c.Directory)
	}

	for _, entry := range entries {
		if entry.Name() == ".git" && entry.IsDir() {
			continue
		}
		return errors.Errorf("directory %s is not empty", c.Directory)
	}

	return nil
}

func (c *initCmd) initFromTemplate(k *kong.Context, logger logging.Logger, sp terminal.SpinnerPrinter) error {
	if err := os.MkdirAll(c.Directory, 0o750); err != nil {
		return errors.Wrapf(err, "failed to create directory %s", c.Directory)
	}

	fs := osfs.New(c.Directory, osfs.WithBoundOS())

	var r *git.Repository
	if err := sp.WrapWithSuccessSpinner("Cloning template", func() error {
		var cloneErr error
		r, cloneErr = git.Clone(memory.NewStorage(), fs, &git.CloneOptions{
			URL:           c.Template,
			Depth:         1,
			ReferenceName: plumbing.ReferenceName(c.RefName),
		})
		return cloneErr
	}); err != nil {
		return errors.Wrapf(err, "failed to clone repository from %q", c.Template)
	}

	ref, err := r.Head()
	if err != nil {
		return errors.Wrapf(err, "failed to get repository's HEAD from %q", c.Template)
	}

	if _, err := fmt.Fprintf(k.Stdout, "Initialized project %q in directory %q from %s (%s)\n",
		c.Name, c.Directory, c.Template, ref.Name().Short()); err != nil {
		return errors.Wrap(err, "failed to write to stdout")
	}

	// Print NOTES.txt if it exists.
	notesFile := filepath.Join(c.Directory, "NOTES.txt")
	if f, err := os.Stat(notesFile); err == nil && !f.IsDir() {
		content, err := os.ReadFile(filepath.Clean(notesFile))
		if err != nil {
			logger.Debug("Failed to read NOTES.txt", "error", err)
		} else {
			if _, err := fmt.Fprintf(k.Stdout, "\n%s\n", content); err != nil {
				return errors.Wrap(err, "failed to write to stdout")
			}
		}
	}

	return nil
}

func (c *initCmd) initMinimal(_ *kong.Context, sp terminal.SpinnerPrinter) error {
	return sp.WrapWithSuccessSpinner("Initializing project", func() error {
		if err := os.MkdirAll(c.Directory, 0o750); err != nil {
			return errors.Wrapf(err, "failed to create directory %s", c.Directory)
		}

		// Write a minimal crossplane-project.yaml.
		projFile := filepath.Join(c.Directory, projectFileName)
		content := fmt.Sprintf(`apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: %s
spec:
  repository: example.com/my-org/%s
`, c.Name, c.Name)

		if err := os.WriteFile(projFile, []byte(content), 0o600); err != nil {
			return errors.Wrapf(err, "failed to write %s", projectFileName)
		}

		// Create default subdirectories.
		dirs := []string{"apis", "functions", "examples", "tests", "operations"}
		for _, dir := range dirs {
			dirPath := filepath.Join(c.Directory, dir)
			if err := os.MkdirAll(dirPath, 0o700); err != nil {
				return errors.Wrapf(err, "failed to create directory %s", dirPath)
			}
			// Write a .gitkeep so empty dirs are tracked.
			keepFile := filepath.Join(dirPath, ".gitkeep")
			if err := os.WriteFile(keepFile, nil, 0o600); err != nil {
				return errors.Wrapf(err, "failed to write %s", keepFile)
			}
		}

		return nil
	})
}
