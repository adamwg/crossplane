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

package composition

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/spf13/afero"
	"golang.org/x/term"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"

	apiextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	v2 "github.com/crossplane/crossplane/apis/v2/apiextensions/v2"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	"github.com/crossplane/crossplane/v2/internal/dependency"
	"github.com/crossplane/crossplane/v2/internal/project"
	"github.com/crossplane/crossplane/v2/internal/terminal"
	"github.com/crossplane/crossplane/v2/internal/xpkg"
)

const (
	functionAutoReadyName    = "crossplane-contrib-function-auto-ready"
	functionAutoReadyPackage = "xpkg.crossplane.io/crossplane-contrib/function-auto-ready"
)

type generateCmd struct {
	Resource    string `arg:""                                              help:"Path to the CompositeResourceDefinition (XRD) file."`
	Name        string `help:"Name prefix for the composition."             optional:""`
	Plural      string `help:"Custom plural for the CompositeTypeRef.Kind." optional:""`
	Path        string `help:"Output file path override."                   optional:""`
	ProjectFile string `default:"crossplane-project.yaml"                   help:"Path to project definition file."                    short:"f"`

	projFS     afero.Fs
	apisFS     afero.Fs
	proj       *v1alpha1.Project
	depManager *dependency.Manager
}

// AfterApply sets up the project filesystem.
func (c *generateCmd) AfterApply(_ *kong.Context) error {
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := project.Parse(c.projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}

	c.proj = proj
	c.apisFS = afero.NewBasePathFs(c.projFS, proj.Spec.Paths.APIs)
	c.depManager = dependency.NewManager(proj, c.projFS,
		dependency.WithProjectFile(filepath.Base(c.ProjectFile)),
	)
	return nil
}

func (c *generateCmd) Run(k *kong.Context) error {
	ctx := context.Background()

	pretty := term.IsTerminal(int(os.Stderr.Fd()))
	sp := terminal.NewSpinnerPrinter(os.Stderr, pretty)

	if err := sp.WrapWithSuccessSpinner("Ensuring function-auto-ready dependency", func() error {
		return c.ensureFunctionAutoReady(ctx)
	}); err != nil {
		return errors.Wrap(err, "failed to ensure function-auto-ready dependency")
	}

	return sp.WrapWithSuccessSpinner("Writing Composition", func() error {
		comp, plural, err := c.newComposition()
		if err != nil {
			return errors.Wrap(err, "failed to create Composition")
		}

		compYAML, err := marshalComposition(comp)
		if err != nil {
			return errors.Wrap(err, "failed to marshal Composition to YAML")
		}

		filePath := c.Path
		if filePath == "" {
			if c.Name != "" {
				filePath = fmt.Sprintf("%s/composition-%s.yaml", strings.ToLower(plural), c.Name)
			} else {
				filePath = fmt.Sprintf("%s/composition.yaml", strings.ToLower(plural))
			}
		}

		exists, err := afero.Exists(c.apisFS, filePath)
		if err != nil {
			return errors.Wrap(err, "failed to check if file exists")
		}
		if exists {
			return errors.Errorf("file %q already exists, use --path to specify a different output path or delete the existing file", filePath)
		}

		if err := c.apisFS.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			return errors.Wrap(err, "failed to create directories for the specified output path")
		}

		return afero.WriteFile(c.apisFS, filePath, compYAML, 0o644)
	})
}

func (c *generateCmd) ensureFunctionAutoReady(ctx context.Context) error {
	if c.proj.Spec != nil {
		for _, dep := range c.proj.Spec.DependsOn {
			if dep.Package != nil && *dep.Package == functionAutoReadyPackage {
				return nil
			}
		}
	}

	return c.depManager.AddDependency(ctx, pkgmetav1.Dependency{
		APIVersion: ptr.To(pkgv1.FunctionGroupVersionKind.GroupVersion().String()),
		Kind:       ptr.To(pkgv1.FunctionKind),
		Package:    ptr.To(functionAutoReadyPackage),
		Version:    ">=v0.0.0",
	})
}

func (c *generateCmd) newComposition() (*apiextv1.Composition, string, error) {
	group, version, kind, plural, err := c.processResource()
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to load resource")
	}

	name := strings.ToLower(fmt.Sprintf("%s.%s", plural, group))
	if c.Name != "" {
		name = strings.ToLower(fmt.Sprintf("%s.%s.%s", c.Name, plural, group))
	}

	comp := &apiextv1.Composition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiextv1.CompositionGroupVersionKind.GroupVersion().String(),
			Kind:       apiextv1.CompositionGroupVersionKind.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextv1.CompositionSpec{
			CompositeTypeRef: apiextv1.TypeReference{
				APIVersion: fmt.Sprintf("%s/%s", group, version),
				Kind:       kind,
			},
			Mode: apiextv1.CompositionModePipeline,
			Pipeline: []apiextv1.PipelineStep{
				{
					Step: functionAutoReadyName,
					FunctionRef: apiextv1.FunctionReference{
						Name: xpkg.ToDNSLabel(functionAutoReadyName),
					},
				},
			},
		},
	}

	return comp, plural, nil
}

func (c *generateCmd) processResource() (group, version, kind, plural string, err error) {
	resourceRaw, err := afero.ReadFile(c.projFS, c.Resource)
	if err != nil {
		return "", "", "", "", errors.Wrapf(err, "failed to read resource file %s", c.Resource)
	}

	var xrdObj v2.CompositeResourceDefinition
	if err := yaml.Unmarshal(resourceRaw, &xrdObj); err != nil {
		return "", "", "", "", errors.Wrap(err, "failed to unmarshal XRD")
	}

	if xrdObj.Spec.Group == "" {
		return "", "", "", "", errors.New("XRD spec.group is required")
	}
	if xrdObj.Spec.Names.Kind == "" {
		return "", "", "", "", errors.New("XRD spec.names.kind is required")
	}

	group = xrdObj.Spec.Group
	kind = xrdObj.Spec.Names.Kind
	plural = xrdObj.Spec.Names.Plural
	if c.Plural != "" {
		plural = c.Plural
	}

	// Find the version that is served and referenceable.
	for _, v := range xrdObj.Spec.Versions {
		if v.Served && v.Referenceable {
			version = v.Name
			break
		}
	}
	if version == "" {
		return "", "", "", "", errors.New("no served and referenceable version found in XRD")
	}

	return group, version, kind, plural, nil
}

// marshalComposition marshals a Composition to YAML, removing creationTimestamp and status.
func marshalComposition(obj any) ([]byte, error) {
	unst, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	unstructured.RemoveNestedField(unst, "status")
	val, found, err := unstructured.NestedFieldNoCopy(unst, "metadata", "creationTimestamp")
	if err != nil {
		return nil, err
	}
	if found && val == nil {
		unstructured.RemoveNestedField(unst, "metadata", "creationTimestamp")
	}

	return yaml.Marshal(unst)
}
