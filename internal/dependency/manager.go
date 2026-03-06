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

// Package dependency manages schema generation for Crossplane project
// dependencies.
package dependency

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
	"github.com/crossplane/crossplane/v2/internal/async"
	"github.com/crossplane/crossplane/v2/internal/git"
	"github.com/crossplane/crossplane/v2/internal/project"
	"github.com/crossplane/crossplane/v2/internal/schemas/generator"
	smanager "github.com/crossplane/crossplane/v2/internal/schemas/manager"
	"github.com/crossplane/crossplane/v2/internal/schemas/runner"
)

// Manager manages dependencies for a Crossplane project, including fetching
// packages, extracting CRDs, and generating schemas.
type Manager struct {
	proj     *v1alpha1.Project
	projFS   afero.Fs
	projFile string
	schemas  *smanager.Manager

	gitCloner       git.Cloner
	gitAuthProvider git.AuthProvider

	craneOptions []crane.Option
	listTags     func(src string, opt ...crane.Option) ([]string, error)

	updateMutex sync.Mutex
}

// ManagerOption configures the dependency manager.
type ManagerOption func(*managerOptions)

type managerOptions struct {
	projFile         string
	schemaFS         afero.Fs
	schemaGenerators []generator.Interface
	schemaRunner     runner.SchemaRunner
	gitAuthProvider  git.AuthProvider
	craneOptions     []crane.Option
	listTags         func(src string, opt ...crane.Option) ([]string, error)
}

// WithProjectFile sets the path to the project file.
func WithProjectFile(path string) ManagerOption {
	return func(opts *managerOptions) {
		opts.projFile = path
	}
}

// WithSchemaFS sets the filesystem to use for schemas.
func WithSchemaFS(fs afero.Fs) ManagerOption {
	return func(opts *managerOptions) {
		opts.schemaFS = fs
	}
}

// WithSchemaRunner sets the runner to use when generating schemas.
func WithSchemaRunner(r runner.SchemaRunner) ManagerOption {
	return func(opts *managerOptions) {
		opts.schemaRunner = r
	}
}

// WithSchemaGenerators sets the schema generators to call.
func WithSchemaGenerators(gs []generator.Interface) ManagerOption {
	return func(opts *managerOptions) {
		opts.schemaGenerators = gs
	}
}

// WithGitAuthProvider sets the auth provider for git operations.
func WithGitAuthProvider(p git.AuthProvider) ManagerOption {
	return func(opts *managerOptions) {
		opts.gitAuthProvider = p
	}
}

// WithCraneOptions sets crane options for OCI image fetching.
func WithCraneOptions(opts ...crane.Option) ManagerOption {
	return func(o *managerOptions) {
		o.craneOptions = opts
	}
}

// WithTagLister sets the function used to list tags from a registry.
func WithTagLister(fn func(src string, opt ...crane.Option) ([]string, error)) ManagerOption {
	return func(o *managerOptions) {
		o.listTags = fn
	}
}

// NewManager returns an initialized dependency manager.
func NewManager(proj *v1alpha1.Project, projFS afero.Fs, opts ...ManagerOption) *Manager {
	options := &managerOptions{
		projFile:         "crossplane-project.yaml",
		schemaFS:         afero.NewBasePathFs(projFS, "schemas"),
		schemaGenerators: generator.AllLanguages(),
		schemaRunner: runner.NewRealSchemaRunner(
			runner.WithImageConfig(proj.Spec.ImageConfig),
		),
		gitAuthProvider: &git.HTTPSAuthProvider{},
	}

	for _, opt := range opts {
		opt(options)
	}

	schemas := smanager.New(
		options.schemaFS,
		options.schemaGenerators,
		options.schemaRunner,
	)

	listTags := options.listTags
	if listTags == nil {
		listTags = crane.ListTags
	}

	return &Manager{
		proj:            proj,
		projFS:          projFS,
		projFile:        options.projFile,
		schemas:         schemas,
		gitCloner:       &git.DefaultCloner{},
		gitAuthProvider: options.gitAuthProvider,
		craneOptions:    options.craneOptions,
		listTags:        listTags,
	}
}

// ResolveRef resolves a version constraint in an OCI ref to a concrete tag.
// If the ref has no tag, has an exact semver version, or is not a valid
// constraint, it is returned unchanged.
func (m *Manager) ResolveRef(ref string) (string, error) {
	// No tag at all — return as-is.
	base, tag, ok := strings.Cut(ref, ":")
	if !ok {
		return ref, nil
	}

	// If the tag is a valid semver version (not a constraint), return as-is.
	if _, err := semver.NewVersion(tag); err == nil {
		return ref, nil
	}

	// Try parsing as a constraint.
	c, err := semver.NewConstraint(tag)
	if err != nil {
		// Not a constraint either (e.g. a digest ref) — return as-is.
		return ref, nil
	}

	// List all tags from the registry.
	tags, err := m.listTags(base, m.craneOptions...)
	if err != nil {
		return "", errors.Wrapf(err, "cannot list tags for %s", base)
	}

	// Filter to valid semver versions.
	var vs []*semver.Version
	for _, t := range tags {
		v, err := semver.NewVersion(t)
		if err != nil {
			continue
		}
		vs = append(vs, v)
	}

	// Sort ascending.
	sort.Sort(semver.Collection(vs))

	// Find the highest version that matches the constraint.
	var match string
	for i := len(vs) - 1; i >= 0; i-- {
		if c.Check(vs[i]) {
			match = vs[i].Original()
			break
		}
	}

	if match == "" {
		// Build a helpful error with the latest available versions.
		n := 3
		if len(vs) < n {
			n = len(vs)
		}
		var latest []string
		for i := len(vs) - 1; i >= len(vs)-n; i-- {
			latest = append(latest, vs[i].Original())
		}
		return "", errors.Errorf("no tag matching constraint %q for %s; latest available: %s", tag, base, strings.Join(latest, ", "))
	}

	return base + ":" + match, nil
}

// AddPackage fetches an xpkg image, extracts its CRDs, and generates schemas.
func (m *Manager) AddPackage(ctx context.Context, ref string, craneOpts ...crane.Option) error {
	resolved, err := m.ResolveRef(ref)
	if err != nil {
		return errors.Wrapf(err, "failed to resolve version constraint for %s", ref)
	}

	opts := append(m.craneOptions, craneOpts...) //nolint:gocritic // intentional append to new slice
	img, err := crane.Pull(resolved, opts...)
	if err != nil {
		return errors.Wrapf(err, "failed to pull image %s", resolved)
	}

	layers, err := img.Layers()
	if err != nil {
		return errors.Wrap(err, "failed to get image layers")
	}

	crdFS, err := extractCRDsFromLayers(layers)
	if err != nil {
		return errors.Wrap(err, "failed to extract CRDs from package")
	}

	digest, err := img.Digest()
	if err != nil {
		return errors.Wrap(err, "failed to get image digest")
	}

	source := smanager.NewXpkgSource(ref, digest.String(), crdFS)
	return m.schemas.Add(ctx, source)
}

// AddDependency adds a standard package dependency, fetching the package and
// generating schemas.
func (m *Manager) AddDependency(ctx context.Context, dep pkgmetav1.Dependency) error {
	pkg := dep.Package
	if pkg == nil {
		return errors.New("dependency has no package reference")
	}

	ref := *pkg
	if dep.Version != "" {
		ref = fmt.Sprintf("%s:%s", ref, dep.Version)
	}

	if err := m.AddPackage(ctx, ref); err != nil {
		return errors.Wrapf(err, "failed to add dependency %s", ref)
	}

	m.updateMutex.Lock()
	defer m.updateMutex.Unlock()

	upsertDependency(m.proj, dep)
	return project.Update(m.projFS, m.projFile, func(p *v1alpha1.Project) {
		upsertDependency(p, dep)
	})
}

// AddAPIDependency adds an API dependency and generates schemas for it.
func (m *Manager) AddAPIDependency(ctx context.Context, dep v1alpha1.APIDependencies) error {
	var source smanager.Source

	switch {
	case dep.Git != nil:
		source = smanager.NewGitSource(dep, m.gitCloner, m.gitAuthProvider)
	case dep.HTTP != nil:
		source = smanager.NewHTTPSource(dep)
	case dep.K8s != nil:
		source = smanager.NewK8sSource(dep)
	default:
		return errors.New("API dependency has no source configured")
	}

	if err := m.schemas.Add(ctx, source); err != nil {
		return errors.Wrapf(err, "failed to generate schemas for API dependency")
	}

	m.updateMutex.Lock()
	defer m.updateMutex.Unlock()

	upsertAPIDependency(m.proj, dep)
	return project.Update(m.projFS, m.projFile, func(p *v1alpha1.Project) {
		upsertAPIDependency(p, dep)
	})
}

// AddAll adds all dependencies configured in the project. If ch is non-nil,
// events will be sent for each dependency as it is processed.
func (m *Manager) AddAll(ctx context.Context, ch async.EventChannel) error {
	if m.proj.Spec == nil {
		return nil
	}

	eg, egCtx := errgroup.WithContext(ctx)

	for _, dep := range m.proj.Spec.DependsOn {
		desc := ""
		if dep.Package != nil {
			desc = *dep.Package
		}
		eg.Go(func() error {
			ch.SendEvent(desc, async.EventStatusStarted)
			if err := m.addDependencyNoWrite(egCtx, dep); err != nil {
				ch.SendEvent(desc, async.EventStatusFailure)
				return err
			}
			ch.SendEvent(desc, async.EventStatusSuccess)
			return nil
		})
	}

	for _, dep := range m.proj.Spec.APIDependencies {
		desc := GetSourceDescription(dep)
		eg.Go(func() error {
			ch.SendEvent(desc, async.EventStatusStarted)
			if err := m.addAPIDependencyNoWrite(egCtx, dep); err != nil {
				ch.SendEvent(desc, async.EventStatusFailure)
				return err
			}
			ch.SendEvent(desc, async.EventStatusSuccess)
			return nil
		})
	}

	return eg.Wait()
}

func (m *Manager) addDependencyNoWrite(ctx context.Context, dep pkgmetav1.Dependency) error {
	pkg := dep.Package
	if pkg == nil {
		return errors.New("dependency has no package reference")
	}

	ref := *pkg
	if dep.Version != "" {
		ref = fmt.Sprintf("%s:%s", ref, dep.Version)
	}

	return m.AddPackage(ctx, ref)
}

func (m *Manager) addAPIDependencyNoWrite(ctx context.Context, dep v1alpha1.APIDependencies) error {
	var source smanager.Source

	switch {
	case dep.Git != nil:
		source = smanager.NewGitSource(dep, m.gitCloner, m.gitAuthProvider)
	case dep.HTTP != nil:
		source = smanager.NewHTTPSource(dep)
	case dep.K8s != nil:
		source = smanager.NewK8sSource(dep)
	default:
		return errors.New("API dependency has no source configured")
	}

	return m.schemas.Add(ctx, source)
}

// Clean removes all generated schemas.
func (m *Manager) Clean() error {
	return m.projFS.RemoveAll("schemas")
}

// GetSourceDescription returns a human-readable description of an API dependency.
func GetSourceDescription(dep v1alpha1.APIDependencies) string {
	switch {
	case dep.Git != nil:
		desc := dep.Git.Repository
		if dep.Git.Ref != "" {
			desc += " (" + dep.Git.Ref + ")"
		}
		if dep.Git.Path != "" {
			desc += " at " + dep.Git.Path
		}
		return desc
	case dep.HTTP != nil:
		return dep.HTTP.URL
	case dep.K8s != nil:
		return "Kubernetes API " + dep.K8s.Version
	default:
		return "unknown source"
	}
}

func upsertDependency(proj *v1alpha1.Project, dep pkgmetav1.Dependency) {
	if proj.Spec == nil {
		proj.Spec = &v1alpha1.ProjectSpec{}
	}

	pkg := dep.Package
	if pkg == nil {
		return
	}

	// Find and update existing entry.
	for i, existing := range proj.Spec.DependsOn {
		if existing.Package != nil && *existing.Package == *pkg {
			proj.Spec.DependsOn[i] = dep
			return
		}
	}

	proj.Spec.DependsOn = append(proj.Spec.DependsOn, dep)
}

func upsertAPIDependency(proj *v1alpha1.Project, dep v1alpha1.APIDependencies) {
	if proj.Spec == nil {
		proj.Spec = &v1alpha1.ProjectSpec{}
	}

	// Find and update existing entry based on type + source.
	for i, existing := range proj.Spec.APIDependencies {
		if matchesAPIDependency(existing, dep) {
			proj.Spec.APIDependencies[i] = dep
			return
		}
	}

	proj.Spec.APIDependencies = append(proj.Spec.APIDependencies, dep)
}

func matchesAPIDependency(a, b v1alpha1.APIDependencies) bool {
	if a.Type != b.Type {
		return false
	}
	switch {
	case a.Git != nil && b.Git != nil:
		return a.Git.Repository == b.Git.Repository
	case a.HTTP != nil && b.HTTP != nil:
		return a.HTTP.URL == b.HTTP.URL
	case a.K8s != nil && b.K8s != nil:
		return true // Only one k8s dep makes sense.
	}
	return false
}

// parseAPIRef parses a package argument for API dependency type.
func parseAPIRef(pkg string) (v1alpha1.APIDependencies, error) { //nolint:unparam // future use
	if version, found := strings.CutPrefix(pkg, "k8s:"); found {
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

	return v1alpha1.APIDependencies{
		Type: v1alpha1.APIDependencyTypeCRD,
	}, nil
}
