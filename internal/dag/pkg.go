/*
Copyright 2025 The Crossplane Authors.

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

package dag

import (
	"github.com/crossplane/crossplane/v2/apis/pkg/v1beta1"
)

var (
	_ Node = &DependencyNode{}
	_ Node = &PackageNode{}
)

// DependencyNode is a DAG node representing a package dependency.
type DependencyNode struct {
	v1beta1.Dependency
}

// Children in is a no-op for dependencies because we are not yet aware of its
// dependencies.
func (d *DependencyNode) Children() []Node {
	return nil
}

// GetConstraints returns a dependency's constrain.
func (d *DependencyNode) GetConstraints() string {
	return d.Constraints
}

// PackageNode is a DAG node representing a package.
type PackageNode struct {
	v1beta1.LockPackage
}

// Children returns dependencies of a LockPackage.
func (l *PackageNode) Children() []Node {
	nodes := make([]Node, len(l.Dependencies))
	for i, r := range l.Dependencies {
		nodes[i] = &DependencyNode{Dependency: r}
	}

	return nodes
}

// GetConstraints returns the version of a LockPackage.
func (l *PackageNode) GetConstraints() string {
	return l.Version
}

// PackagesToNodes converts LockPackages to DAG nodes.
func PackagesToNodes(pkgs ...v1beta1.LockPackage) []Node {
	nodes := make([]Node, len(pkgs))
	for i, r := range pkgs {
		nodes[i] = &PackageNode{LockPackage: r}
	}

	return nodes
}
