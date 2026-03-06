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
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	"golang.org/x/term"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
	"github.com/crossplane/crossplane/v2/internal/dependency"
	"github.com/crossplane/crossplane/v2/internal/project"
	"github.com/crossplane/crossplane/v2/internal/terminal"
)

// addCmd adds a dependency to the current project.
type addCmd struct {
	Package     string `arg:""                          help:"Package to be added (e.g. xpkg.upbound.io/crossplane-contrib/provider-nop:v0.6.0), or for --api: k8s:v1.33.0, a git repo URL, or an HTTP URL."`
	ProjectFile string `default:"crossplane-project.yaml" help:"Path to project definition file." short:"f"`

	// API dependency specific flags.
	API     bool   `help:"Treat the dependency as an API dependency (k8s or CRD)."`
	GitRef  string `help:"Git ref for CRD dependencies (branch, tag, or commit SHA)." name:"git-ref"`
	GitPath string `help:"Path within the git repository for CRD dependencies."       name:"git-path"`
}

// Run executes the add command.
func (c *addCmd) Run(k *kong.Context, logger logging.Logger) error {
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

	m := dependency.NewManager(proj, projFS,
		dependency.WithProjectFile(c.ProjectFile),
	)

	pretty := term.IsTerminal(int(os.Stderr.Fd()))
	sp := terminal.NewSpinnerPrinter(os.Stderr, pretty)

	if c.API {
		return c.addAPIDependency(ctx, m, sp)
	}

	logger.Debug("Adding package dependency", "package", c.Package)
	return sp.WrapWithSuccessSpinner("Adding "+c.Package, func() error {
		return m.AddPackage(ctx, c.Package)
	})
}

func (c *addCmd) addAPIDependency(ctx context.Context, m *dependency.Manager, sp terminal.SpinnerPrinter) error {
	dep, err := c.buildAPIDependency()
	if err != nil {
		return err
	}

	desc := dependency.GetSourceDescription(dep)
	return sp.WrapWithSuccessSpinner("Adding API dependency: "+desc, func() error {
		return m.AddAPIDependency(ctx, dep)
	})
}

func (c *addCmd) buildAPIDependency() (v1alpha1.APIDependencies, error) {
	// k8s dependency: k8s:vX.Y.Z
	if version, found := strings.CutPrefix(c.Package, "k8s:"); found {
		if version == "" {
			return v1alpha1.APIDependencies{}, errors.New("k8s version is required (e.g., k8s:v1.33.0)")
		}
		return v1alpha1.APIDependencies{
			Type: v1alpha1.APIDependencyTypeK8s,
			K8s: &v1alpha1.APIK8sReference{
				Version: version,
			},
		}, nil
	}

	dep := v1alpha1.APIDependencies{
		Type: v1alpha1.APIDependencyTypeCRD,
	}

	if c.GitRef != "" {
		if c.Package == "" {
			return v1alpha1.APIDependencies{}, errors.New("repository URL is required for git-based CRD dependencies")
		}
		dep.Git = &v1alpha1.APIGitReference{
			Repository: c.Package,
			Ref:        c.GitRef,
			Path:       c.GitPath,
		}
	} else {
		if c.Package == "" {
			return v1alpha1.APIDependencies{}, errors.New("URL is required for HTTP-based CRD dependencies")
		}
		if !strings.HasPrefix(c.Package, "http://") && !strings.HasPrefix(c.Package, "https://") {
			return v1alpha1.APIDependencies{}, errors.New("URL must start with http:// or https://")
		}
		dep.HTTP = &v1alpha1.APIHTTPReference{
			URL: c.Package,
		}
	}

	return dep, nil
}
