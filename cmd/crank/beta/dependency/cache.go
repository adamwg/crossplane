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
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	"golang.org/x/term"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/crossplane/v2/internal/async"
	"github.com/crossplane/crossplane/v2/internal/dependency"
	"github.com/crossplane/crossplane/v2/internal/project"
	"github.com/crossplane/crossplane/v2/internal/terminal"
)

// updateCacheCmd updates the dependency cache by regenerating all schemas.
type updateCacheCmd struct {
	ProjectFile string `default:"crossplane-project.yaml" help:"Path to project definition file." short:"f"`
	GitToken    string `env:"CROSSPLANE_GIT_TOKEN"        help:"Token for git HTTPS authentication."`
	GitUsername string `default:"x-access-token"           env:"CROSSPLANE_GIT_USERNAME" help:"Username for git HTTPS authentication."`
}

// Run executes the update-cache command.
func (c *updateCacheCmd) Run(k *kong.Context, logger logging.Logger) error {
	ctx := context.Background()

	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	projFS := afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := project.Parse(projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}
	proj.Default()

	var opts []dependency.ManagerOption
	opts = append(opts, dependency.WithProjectFile(c.ProjectFile))

	if c.GitToken != "" {
		opts = append(opts, dependency.WithGitAuthProvider(&gitAuthProvider{
			username: c.GitUsername,
			token:    c.GitToken,
		}))
	}

	m := dependency.NewManager(proj, projFS, opts...)

	pretty := term.IsTerminal(int(os.Stderr.Fd()))
	sp := terminal.NewSpinnerPrinter(os.Stderr, pretty)

	logger.Debug("Updating all dependencies")
	return sp.WrapAsyncWithSuccessSpinners(func(ch async.EventChannel) error {
		return m.AddAll(ctx, ch)
	})
}

// cleanCacheCmd removes all generated schemas.
type cleanCacheCmd struct {
	ProjectFile string `default:"crossplane-project.yaml" help:"Path to project definition file." short:"f"`
}

// Run executes the clean-cache command.
func (c *cleanCacheCmd) Run(k *kong.Context, _ logging.Logger) error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	projFS := afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := project.Parse(projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}
	proj.Default()

	m := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(c.ProjectFile),
	)

	if err := m.Clean(); err != nil {
		return err
	}

	fmt.Fprintln(k.Stdout, "Schema cache cleaned")
	return nil
}
