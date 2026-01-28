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

	// ParentConstraints is a list of constraints that are passed down from the
	// parent package to the dependency.
	ParentConstraints []string
}

// Neighbors in is a no-op for dependencies because we are not yet aware of its
// dependencies.
func (d *DependencyNode) Neighbors() []Node {
	return nil
}

// AddNeighbors adds parent constraints to a dependency in the DAG.
func (d *DependencyNode) AddNeighbors(nodes ...Node) error {
	for _, n := range nodes {
		n.AddParentConstraints([]string{d.Constraints})
	}

	return nil
}

// GetConstraints returns a dependency's constrain.
func (d *DependencyNode) GetConstraints() string {
	return d.Constraints
}

// GetParentConstraints returns a dependency's parent constraints.
func (d *DependencyNode) GetParentConstraints() []string {
	return d.ParentConstraints
}

// AddParentConstraints appends passed constraints to the existing parent constraints.
func (d *DependencyNode) AddParentConstraints(pc []string) {
	d.ParentConstraints = append(d.ParentConstraints, pc...)
}

// PackageNode is a DAG node representing a package.
type PackageNode struct {
	v1beta1.LockPackage

	// ParentConstraints is a list of constraints that are passed down from the
	// parent package to the dependency.
	ParentConstraints []string
}

// Neighbors returns dependencies of a LockPackage.
func (l *PackageNode) Neighbors() []Node {
	nodes := make([]Node, len(l.Dependencies))
	for i, r := range l.Dependencies {
		nodes[i] = &DependencyNode{Dependency: r}
	}

	return nodes
}

// AddNeighbors adds dependencies to a LockPackage and
// updates the parent constraints of the dependencies in the DAG.
func (l *PackageNode) AddNeighbors(nodes ...Node) error {
	for _, n := range nodes {
		for _, dep := range l.Dependencies {
			if dep.Identifier() == n.Identifier() {
				n.AddParentConstraints([]string{dep.Constraints})
				break
			}
		}
	}

	return nil
}

// GetConstraints returns the version of a LockPackage.
func (l *PackageNode) GetConstraints() string {
	return l.Version
}

// GetParentConstraints returns the parent constraints of a LockPackage.
func (l *PackageNode) GetParentConstraints() []string {
	return l.ParentConstraints
}

// AddParentConstraints appends passed constraints to the existing parent constraints.
func (l *PackageNode) AddParentConstraints(pc []string) {
	l.ParentConstraints = append(l.ParentConstraints, pc...)
}

// PackagesToNodes converts LockPackages to DAG nodes.
func PackagesToNodes(pkgs ...v1beta1.LockPackage) []Node {
	nodes := make([]Node, len(pkgs))
	for i, r := range pkgs {
		nodes[i] = &PackageNode{LockPackage: r}
	}

	return nodes
}
