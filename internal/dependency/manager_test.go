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
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
)

func TestResolveRef(t *testing.T) {
	fakeTags := func(src string, opt ...crane.Option) ([]string, error) {
		return []string{"v1.0.0", "v1.1.0", "v2.0.0", "latest", "invalid"}, nil
	}

	m := NewManager(
		&v1alpha1.Project{
			Spec: &v1alpha1.ProjectSpec{},
		},
		afero.NewMemMapFs(),
		WithTagLister(fakeTags),
	)

	tests := map[string]struct {
		ref     string
		want    string
		wantErr bool
	}{
		"ExactVersion": {
			ref:  "foo:v1.2.3",
			want: "foo:v1.2.3",
		},
		"NoTag": {
			ref:  "foo",
			want: "foo",
		},
		"DigestRef": {
			ref:  "foo@sha256:abc123",
			want: "foo@sha256:abc123",
		},
		"ConstraintRange": {
			ref:  "foo:>=v1.0.0, <v2.0.0",
			want: "foo:v1.1.0",
		},
		"ConstraintCaret": {
			ref:  "foo:^v1.0.0",
			want: "foo:v1.1.0",
		},
		"NoMatchingVersion": {
			ref:     "foo:>=v3.0.0",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := m.ResolveRef(tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveRef(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}
