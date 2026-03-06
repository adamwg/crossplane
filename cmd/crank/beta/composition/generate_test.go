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
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	apiextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
)

const testProjectYAML = `apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: test-project
spec:
  paths:
    apis: apis
`

const testXRDYAML = `apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: xexamples.example.org
spec:
  group: example.org
  names:
    kind: XExample
    plural: xexamples
  versions:
  - name: v1alpha1
    served: true
    referenceable: true
    schema:
      openAPIV3Schema:
        type: object
`

// testProjectWithAutoReady returns a Project that already has
// function-auto-ready in dependsOn, so ensureFunctionAutoReady is a no-op
// without needing a real dependency manager.
func testProjectWithAutoReady() *v1alpha1.Project {
	return &v1alpha1.Project{
		Spec: &v1alpha1.ProjectSpec{
			Paths: &v1alpha1.ProjectPaths{
				APIs: "apis",
			},
			DependsOn: []pkgmetav1.Dependency{
				{
					Package: ptr.To(functionAutoReadyPackage),
					Version: ">=v0.0.0",
				},
			},
		},
	}
}

func setupTestFS(t *testing.T) (afero.Fs, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "crossplane-project.yaml", []byte(testProjectYAML), 0o644)
	_ = fs.MkdirAll("apis/xexamples", 0o755)
	_ = afero.WriteFile(fs, "apis/xexamples/definition.yaml", []byte(testXRDYAML), 0o644)
	return fs, afero.NewBasePathFs(fs, "apis")
}

func runCmd(t *testing.T, cmd *generateCmd) error {
	t.Helper()
	var buf bytes.Buffer
	app, err := kong.New(&struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	app.Stdout = &buf
	kctx, err := app.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Run(kctx)
}

func TestGenerateComposition(t *testing.T) {
	fs, apisFS := setupTestFS(t)

	cmd := &generateCmd{
		Resource: "apis/xexamples/definition.yaml",
		projFS:   fs,
		apisFS:   apisFS,
		proj:     testProjectWithAutoReady(),
	}

	if err := runCmd(t, cmd); err != nil {
		t.Fatal(err)
	}

	exists, err := afero.Exists(apisFS, "xexamples/composition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected composition file to be created")
	}

	data, err := afero.ReadFile(apisFS, "xexamples/composition.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var comp apiextv1.Composition
	if err := yaml.Unmarshal(data, &comp); err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff("xexamples.example.org", comp.Name); diff != "" {
		t.Errorf("unexpected name (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("example.org/v1alpha1", comp.Spec.CompositeTypeRef.APIVersion); diff != "" {
		t.Errorf("unexpected apiVersion (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("XExample", comp.Spec.CompositeTypeRef.Kind); diff != "" {
		t.Errorf("unexpected kind (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(apiextv1.CompositionModePipeline, comp.Spec.Mode); diff != "" {
		t.Errorf("unexpected mode (-want +got):\n%s", diff)
	}
	if len(comp.Spec.Pipeline) != 1 {
		t.Fatalf("expected 1 pipeline step, got %d", len(comp.Spec.Pipeline))
	}
	if diff := cmp.Diff(functionAutoReadyName, comp.Spec.Pipeline[0].Step); diff != "" {
		t.Errorf("unexpected step name (-want +got):\n%s", diff)
	}
}

func TestGenerateCompositionWithName(t *testing.T) {
	fs, apisFS := setupTestFS(t)

	cmd := &generateCmd{
		Resource: "apis/xexamples/definition.yaml",
		Name:     "aws",
		projFS:   fs,
		apisFS:   apisFS,
		proj:     testProjectWithAutoReady(),
	}

	if err := runCmd(t, cmd); err != nil {
		t.Fatal(err)
	}

	exists, err := afero.Exists(apisFS, "xexamples/composition-aws.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected composition-aws.yaml to be created")
	}

	data, err := afero.ReadFile(apisFS, "xexamples/composition-aws.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var comp apiextv1.Composition
	if err := yaml.Unmarshal(data, &comp); err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff("aws.xexamples.example.org", comp.Name); diff != "" {
		t.Errorf("unexpected name (-want +got):\n%s", diff)
	}
}

func TestGenerateCompositionWithCustomPlural(t *testing.T) {
	fs, apisFS := setupTestFS(t)

	cmd := &generateCmd{
		Resource: "apis/xexamples/definition.yaml",
		Plural:   "xthings",
		projFS:   fs,
		apisFS:   apisFS,
		proj:     testProjectWithAutoReady(),
	}

	if err := runCmd(t, cmd); err != nil {
		t.Fatal(err)
	}

	exists, err := afero.Exists(apisFS, "xthings/composition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected xthings/composition.yaml to be created")
	}

	data, err := afero.ReadFile(apisFS, "xthings/composition.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var comp apiextv1.Composition
	if err := yaml.Unmarshal(data, &comp); err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff("xthings.example.org", comp.Name); diff != "" {
		t.Errorf("unexpected name (-want +got):\n%s", diff)
	}
}

func TestGenerateCompositionFileExists(t *testing.T) {
	fs, apisFS := setupTestFS(t)
	_ = afero.WriteFile(fs, "apis/xexamples/composition.yaml", []byte("existing"), 0o644)

	cmd := &generateCmd{
		Resource: "apis/xexamples/definition.yaml",
		projFS:   fs,
		apisFS:   apisFS,
		proj:     testProjectWithAutoReady(),
	}

	err := runCmd(t, cmd)
	if err == nil {
		t.Fatal("expected error when file already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %s", err.Error())
	}
}

func TestEnsureFunctionAutoReadyAlreadyExists(t *testing.T) {
	proj := testProjectWithAutoReady()
	cmd := &generateCmd{
		proj: proj,
		// depManager is nil — if ensureFunctionAutoReady doesn't short-circuit,
		// it will panic, which is the desired failure mode for this test.
	}

	if err := cmd.ensureFunctionAutoReady(context.Background()); err != nil {
		t.Fatalf("expected no error when dependency already exists, got: %v", err)
	}
}
