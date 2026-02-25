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
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/crossplane/v2/apis/pkg/v1beta1"
)

var (
	_ Node = &DependencyNode{}
	_ Node = &PackageNode{}
)

// DependencyNode is a DAG node representing a package dependency.
type DependencyNode struct {
	Deps []v1beta1.Dependency
}

// Identifier identifies a dependency node. Since all Dependencies in the node
// must have the same source/identifier, we just return the first one.
func (d *DependencyNode) Identifier() string {
	return d.Deps[0].Identifier()
}

// Children in is a no-op for dependencies because we are not yet aware of its
// dependencies.
func (d *DependencyNode) Children() []Node {
	return nil
}

// GetConstraints returns a dependency's constrain.
func (d *DependencyNode) GetConstraints() []string {
	cs := make([]string, len(d.Deps))
	for i, dep := range d.Deps {
		cs[i] = dep.Constraints
	}

	return cs
}

func (d *DependencyNode) Merge(n Node) (Node, error) {
	if n.Identifier() != d.Identifier() {
		return nil, errors.Errorf("cannot merge node %s into node %s with mismatched identifier", n.Identifier(), d.Identifier())
	}

	dn, ok := n.(*DependencyNode)
	if !ok {
		return nil, errors.Errorf("cannot merge node of type %T into DependencyNode", n)
	}

	d.Deps = append(d.Deps, dn.Deps...)

	return d, nil
}

// PackageNode is a DAG node representing a package.
type PackageNode struct {
	Pkgs []v1beta1.LockPackage
}

// Identifier identifies a package node. Since all LockPackages in the node must
// have the same source/identifier, we just return the first one.
func (l *PackageNode) Identifier() string {
	return l.Pkgs[0].Identifier()
}

// Children returns the union of the dependencies of the LockPackages in a node.
func (l *PackageNode) Children() []Node {
	deps := make(map[string]*DependencyNode)

	for _, pkg := range l.Pkgs {
		for _, dep := range pkg.Dependencies {
			if node, ok := deps[dep.Package]; ok {
				node.Deps = append(node.Deps, dep)
				continue
			}

			deps[dep.Package] = &DependencyNode{
				Deps: []v1beta1.Dependency{dep},
			}
		}
	}

	nodes := make([]Node, 0, len(deps))
	for _, dep := range deps {
		nodes = append(nodes, dep)
	}

	return nodes
}

// GetConstraints returns the version of a LockPackage.
func (l *PackageNode) GetConstraints() []string {
	cs := make([]string, len(l.Pkgs))
	for i, pkg := range l.Pkgs {
		cs[i] = pkg.Version
	}

	return cs
}

func (l *PackageNode) Merge(n Node) (Node, error) {
	if n.Identifier() != l.Identifier() {
		return nil, errors.Errorf("cannot merge node %s into node %s with mismatched identifier", n.Identifier(), l.Identifier())
	}

	pn, ok := n.(*PackageNode)
	if !ok {
		return nil, errors.Errorf("cannot merge node of type %T into DependencyNode", n)
	}

	l.Pkgs = append(l.Pkgs, pn.Pkgs...)

	return l, nil
}

// PackagesToNodes converts LockPackages to DAG nodes.
func PackagesToNodes(pkgs ...v1beta1.LockPackage) []Node {
	pkgNodes := make(map[string]*PackageNode)

	for _, r := range pkgs {
		if node, ok := pkgNodes[r.Source]; ok {
			node.Pkgs = append(node.Pkgs, r)
			continue
		}

		pkgNodes[r.Source] = &PackageNode{
			Pkgs: []v1beta1.LockPackage{r},
		}
	}

	nodes := make([]Node, 0, len(pkgNodes))
	for _, node := range pkgNodes {
		nodes = append(nodes, node)
	}

	return nodes
}
