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

package function

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	apiextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"
	v1alpha1 "github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
)

func testProject() *v1alpha1.Project {
	p := &v1alpha1.Project{
		Spec: &v1alpha1.ProjectSpec{
			Paths: &v1alpha1.ProjectPaths{
				Functions: "functions",
			},
		},
	}
	return p
}

func TestGenerateGoTemplatingFiles(t *testing.T) {
	c := &generateCmd{
		Name:      "my-func",
		schemasFS: afero.NewMemMapFs(),
		fsPath:    "functions/my-func",
	}
	fs := afero.NewMemMapFs()
	if err := c.generateGoTemplatingFiles(fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"00-prelude.yaml.gotmpl", "01-compose.yaml.gotmpl"} {
		exists, err := afero.Exists(fs, f)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("expected file %q to exist", f)
		}
	}
}

func TestGenerateGoTemplatingFilesWithSchema(t *testing.T) {
	schemasFS := afero.NewMemMapFs()
	_ = schemasFS.MkdirAll("json", 0o755)
	_ = afero.WriteFile(schemasFS, "json/index.schema.json", []byte("{}"), 0o644)

	c := &generateCmd{
		Name:      "my-func",
		schemasFS: schemasFS,
		fsPath:    "functions/my-func",
	}
	fs := afero.NewMemMapFs()
	if err := c.generateGoTemplatingFiles(fs); err != nil {
		t.Fatal(err)
	}

	data, err := afero.ReadFile(fs, "01-compose.yaml.gotmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("yaml-language-server")) {
		t.Errorf("expected schema modeline in compose template, got:\n%s", data)
	}
}

func TestGenerateKCLFiles(t *testing.T) {
	c := &generateCmd{
		Name:      "my-func",
		schemasFS: afero.NewMemMapFs(),
	}
	fs := afero.NewMemMapFs()
	if err := c.generateKCLFiles(fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"main.k", "kcl.mod", "kcl.mod.lock"} {
		exists, err := afero.Exists(fs, f)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("expected file %q to exist", f)
		}
	}

	data, err := afero.ReadFile(fs, "kcl.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`name = "my-func"`)) {
		t.Errorf("expected kcl.mod to contain function name, got:\n%s", data)
	}
	// No schemas => no model dependency.
	if bytes.Contains(data, []byte("[dependencies]")) {
		t.Errorf("expected kcl.mod to not contain dependencies without schemas, got:\n%s", data)
	}
}

func TestGenerateKCLFilesWithSchemas(t *testing.T) {
	schemasFS := afero.NewMemMapFs()
	_ = schemasFS.MkdirAll("kcl/io/upbound/aws/ec2/v1beta1", 0o755)
	_ = afero.WriteFile(schemasFS, "kcl/io/upbound/aws/ec2/v1beta1/res.k", []byte("schema Bucket:"), 0o644)
	_ = schemasFS.MkdirAll("kcl/io/upbound/aws/s3/v1beta2", 0o755)
	_ = afero.WriteFile(schemasFS, "kcl/io/upbound/aws/s3/v1beta2/res.k", []byte("schema Bucket:"), 0o644)

	c := &generateCmd{
		Name:      "my-func",
		schemasFS: schemasFS,
	}
	fs := afero.NewMemMapFs()
	if err := c.generateKCLFiles(fs); err != nil {
		t.Fatal(err)
	}

	mainK, err := afero.ReadFile(fs, "main.k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mainK, []byte("import models.io.upbound.aws.ec2.v1beta1 as ec2v1beta1")) {
		t.Errorf("expected main.k to contain ec2 import, got:\n%s", mainK)
	}
	if !bytes.Contains(mainK, []byte("import models.io.upbound.aws.s3.v1beta2 as s3v1beta2")) {
		t.Errorf("expected main.k to contain s3 import, got:\n%s", mainK)
	}

	kclMod, err := afero.ReadFile(fs, "kcl.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(kclMod, []byte(`models = { path = "./model" }`)) {
		t.Errorf("expected kcl.mod to contain model dependency, got:\n%s", kclMod)
	}
}

func TestGeneratePythonFiles(t *testing.T) {
	c := &generateCmd{Name: "my-func"}
	fs := afero.NewMemMapFs()
	if err := c.generatePythonFiles(fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"main.py", "requirements.txt"} {
		exists, err := afero.Exists(fs, f)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("expected file %q to exist", f)
		}
	}
}

func TestGenerateGoFiles(t *testing.T) {
	c := &generateCmd{
		Name:          "my-func",
		projectSource: "github.com/example/my-project",
		schemasFS:     afero.NewMemMapFs(),
		proj:          testProject(),
	}
	fs := afero.NewMemMapFs()
	if err := c.generateGoFiles(fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"main.go", "fn.go", "fn_test.go", "go.mod", "go.sum"} {
		exists, err := afero.Exists(fs, f)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("expected file %q to exist", f)
		}
	}

	data, err := afero.ReadFile(fs, "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("github.com/example/my-project")) {
		t.Errorf("expected go.mod to contain module path, got:\n%s", data)
	}
}

func TestGenerateGoFilesWithSchemas(t *testing.T) {
	schemasFS := afero.NewMemMapFs()
	_ = schemasFS.MkdirAll("go", 0o755)

	c := &generateCmd{
		Name:          "my-func",
		projectSource: "github.com/example/my-project",
		schemasFS:     schemasFS,
		proj:          testProject(),
	}
	fs := afero.NewMemMapFs()
	if err := c.generateGoFiles(fs); err != nil {
		t.Fatal(err)
	}

	data, err := afero.ReadFile(fs, "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("dev.crossplane.io/models")) {
		t.Errorf("expected go.mod to contain models import, got:\n%s", data)
	}
	if !bytes.Contains(data, []byte("replace dev.crossplane.io/models")) {
		t.Errorf("expected go.mod to contain replace directive, got:\n%s", data)
	}
}

func newTestKongContext(t *testing.T, buf *bytes.Buffer) *kong.Context {
	t.Helper()
	app, err := kong.New(&struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	kctx, err := app.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	kctx.Stdout = buf
	return kctx
}

func TestRunInvalidName(t *testing.T) {
	c := &generateCmd{
		Name:        "INVALID_NAME",
		functionsFS: afero.NewMemMapFs(),
		projFS:      afero.NewMemMapFs(),
	}
	var buf bytes.Buffer
	kctx := newTestKongContext(t, &buf)
	if err := c.Run(kctx); err == nil {
		t.Fatal("expected error for invalid DNS name")
	}
}

func TestRunDirectoryNotEmpty(t *testing.T) {
	functionsFS := afero.NewMemMapFs()
	_ = functionsFS.MkdirAll("my-func", 0o755)
	_ = afero.WriteFile(functionsFS, "my-func/existing.txt", []byte("data"), 0o644)

	c := &generateCmd{
		Name:        "my-func",
		Language:    "go-templating",
		functionsFS: functionsFS,
		projFS:      afero.NewMemMapFs(),
	}
	var buf bytes.Buffer
	kctx := newTestKongContext(t, &buf)
	err := c.Run(kctx)
	if err == nil {
		t.Fatal("expected error for non-empty directory")
	}
	if !strings.Contains(err.Error(), "already exists and is not empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAddCompositionStep(t *testing.T) {
	comp := &apiextv1.Composition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apiextensions.crossplane.io/v1",
			Kind:       "Composition",
		},
		Spec: apiextv1.CompositionSpec{
			Pipeline: []apiextv1.PipelineStep{
				{
					Step:        "existing",
					FunctionRef: apiextv1.FunctionReference{Name: "existing-fn"},
				},
			},
		},
	}

	if err := addCompositionStep(comp, "my-func", "my-fn-ref"); err != nil {
		t.Fatal(err)
	}

	if len(comp.Spec.Pipeline) != 2 {
		t.Fatalf("expected 2 pipeline steps, got %d", len(comp.Spec.Pipeline))
	}
	if comp.Spec.Pipeline[0].Step != "my-func" {
		t.Errorf("expected first step to be %q, got %q", "my-func", comp.Spec.Pipeline[0].Step)
	}
	if comp.Spec.Pipeline[0].FunctionRef.Name != "my-fn-ref" {
		t.Errorf("expected functionRef %q, got %q", "my-fn-ref", comp.Spec.Pipeline[0].FunctionRef.Name)
	}
}

func TestAddCompositionStepDedup(t *testing.T) {
	comp := &apiextv1.Composition{
		Spec: apiextv1.CompositionSpec{
			Pipeline: []apiextv1.PipelineStep{
				{
					Step:        "my-func",
					FunctionRef: apiextv1.FunctionReference{Name: "my-fn-ref"},
				},
			},
		},
	}

	if err := addCompositionStep(comp, "my-func", "my-fn-ref"); err != nil {
		t.Fatal(err)
	}

	if len(comp.Spec.Pipeline) != 1 {
		t.Errorf("expected dedup to keep 1 step, got %d", len(comp.Spec.Pipeline))
	}
}

func TestAddStepToCompositionFile(t *testing.T) {
	comp := &apiextv1.Composition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apiextensions.crossplane.io/v1",
			Kind:       "Composition",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-comp",
		},
		Spec: apiextv1.CompositionSpec{
			CompositeTypeRef: apiextv1.TypeReference{
				APIVersion: "example.org/v1",
				Kind:       "XExample",
			},
			Mode: apiextv1.CompositionModePipeline,
			Pipeline: []apiextv1.PipelineStep{
				{
					Step:        "auto-ready",
					FunctionRef: apiextv1.FunctionReference{Name: "crossplane-contrib-function-auto-ready"},
				},
			},
		},
	}

	compYAML, err := yaml.Marshal(comp)
	if err != nil {
		t.Fatal(err)
	}

	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "composition.yaml", compYAML, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := addStepToComposition(fs, "composition.yaml", "my-func", "my-fn-ref"); err != nil {
		t.Fatal(err)
	}

	data, err := afero.ReadFile(fs, "composition.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var result apiextv1.Composition
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	if len(result.Spec.Pipeline) != 2 {
		t.Fatalf("expected 2 pipeline steps, got %d", len(result.Spec.Pipeline))
	}
	if diff := cmp.Diff("my-func", result.Spec.Pipeline[0].Step); diff != "" {
		t.Errorf("first step name mismatch (-want +got):\n%s", diff)
	}
}
