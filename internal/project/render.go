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
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	devv1alpha1 "github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
	"github.com/crossplane/crossplane/v2/internal/async"
	"github.com/crossplane/crossplane/v2/internal/xpkg"
)

// A RefResolver resolves an OCI reference with a version constraint to a
// concrete tag.
type RefResolver interface {
	ResolveRef(ref string) (string, error)
}

// IsFunctionDependency returns true if the dependency represents a Function.
func IsFunctionDependency(dep pkgmetav1.Dependency) bool {
	apiVersion := ptr.Deref(dep.APIVersion, "")
	kind := ptr.Deref(dep.Kind, "")

	isV1 := apiVersion == pkgv1.FunctionGroupVersionKind.GroupVersion().String() && kind == pkgv1.FunctionKind
	isV1Beta1 := apiVersion == pkgv1beta1.FunctionGroupVersionKind.GroupVersion().String() && kind == pkgv1beta1.FunctionKind

	// Legacy shorthand: Function field set directly.
	isLegacy := dep.Function != nil

	return isV1 || isV1Beta1 || isLegacy
}

// FunctionPackageRef returns the OCI ref for a function dependency.
func FunctionPackageRef(dep pkgmetav1.Dependency) string {
	if dep.Function != nil {
		return *dep.Function
	}
	if dep.Package == nil {
		return ""
	}
	ref := *dep.Package
	if dep.Version != "" {
		ref = fmt.Sprintf("%s:%s", ref, dep.Version)
	}
	return ref
}

// LoadProjectFunctions loads function manifests from a project's DependsOn
// list, resolving version constraints using the provided resolver.
func LoadProjectFunctions(resolver RefResolver, proj *devv1alpha1.Project) ([]pkgv1.Function, error) {
	if proj.Spec == nil {
		return nil, nil
	}

	var fns []pkgv1.Function
	for _, dep := range proj.Spec.DependsOn {
		if !IsFunctionDependency(dep) {
			continue
		}

		ref := FunctionPackageRef(dep)
		if ref == "" {
			continue
		}

		resolved, err := resolver.ResolveRef(ref)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot resolve function dependency %s", ref)
		}

		repo, _, _ := strings.Cut(ref, ":")

		fnRepo, err := name.NewRepository(repo, name.StrictValidation)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot parse function repository %s", repo)
		}

		f := pkgv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: xpkg.ToDNSLabel(fnRepo.RepositoryStr()),
			},
			Spec: pkgv1.FunctionSpec{
				PackageSpec: pkgv1.PackageSpec{
					Package: resolved,
				},
			},
		}
		fns = append(fns, f)
	}

	return fns, nil
}

// BuildAndPushEmbeddedFunctions builds embedded functions from a project and
// pushes matching images to the Docker daemon. It returns Function manifests
// for use with the render pipeline.
func BuildAndPushEmbeddedFunctions(ctx context.Context, proj *devv1alpha1.Project, projFS afero.Fs, projDir string, maxConcurrency uint, eventCh async.EventChannel) ([]pkgv1.Function, error) {
	b := NewBuilder(
		BuildWithMaxConcurrency(maxConcurrency),
	)

	imgMap, err := b.Build(ctx, proj, projFS,
		BuildWithProjectBasePath(projDir),
		BuildWithEventChannel(eventCh),
	)
	if err != nil {
		return nil, errors.Wrap(err, "cannot build project")
	}

	eventCh.SendEvent("Pushing embedded functions to daemon", async.EventStatusStarted)
	fns, err := embeddedFunctionsToDaemon(ctx, imgMap)
	if err != nil {
		eventCh.SendEvent("Pushing embedded functions to daemon", async.EventStatusFailure)
		return nil, err
	}
	eventCh.SendEvent("Pushing embedded functions to daemon", async.EventStatusSuccess)
	return fns, nil
}

// embeddedFunctionsToDaemon loads each compatible image in the ImageTagMap into
// the Docker daemon and returns Function manifests.
func embeddedFunctionsToDaemon(ctx context.Context, imageMap ImageTagMap) ([]pkgv1.Function, error) {
	targetArch := getDockerDaemonArchitecture(ctx)

	var fns []pkgv1.Function
	for tag, img := range imageMap {
		cfgFile, err := img.ConfigFile()
		if err != nil {
			return nil, errors.Wrapf(err, "cannot get platform info for image %s", tag)
		}

		if cfgFile.Architecture != targetArch {
			continue
		}

		if _, err := daemon.Write(tag, img); err != nil {
			return nil, errors.Wrapf(err, "cannot push image %s to daemon", tag)
		}

		fns = append(fns, pkgv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: xpkg.ToDNSLabel(tag.Context().RepositoryStr()),
			},
			Spec: pkgv1.FunctionSpec{
				PackageSpec: pkgv1.PackageSpec{
					Package: tag.Name(),
				},
			},
		})
	}

	return fns, nil
}

// getDockerDaemonArchitecture detects the Docker daemon's architecture.
func getDockerDaemonArchitecture(ctx context.Context) string {
	dockerHost := os.Getenv("DOCKER_HOST")

	if dockerHost == "" || strings.HasPrefix(dockerHost, "unix://") {
		return runtime.GOARCH
	}

	cli, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation(), client.FromEnv)
	if err != nil {
		return runtime.GOARCH
	}
	defer cli.Close() //nolint:errcheck // best effort

	info, err := cli.Info(ctx)
	if err != nil {
		return runtime.GOARCH
	}

	return normalizeArchitecture(info.Architecture)
}

// normalizeArchitecture converts Docker's architecture naming to Go's GOARCH format.
func normalizeArchitecture(dockerArch string) string {
	switch dockerArch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return dockerArch
	}
}
