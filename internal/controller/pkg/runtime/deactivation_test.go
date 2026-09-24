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

package runtime

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	extv1alpha1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1alpha1"
)

func TestPlanRemoval(t *testing.T) {
	// An MRD whose last activator has gone away, with a storage version so we
	// can list its instances.
	released := func(name string, mirror ...string) extv1alpha1.ManagedResourceDefinition {
		mrd := extv1alpha1.ManagedResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: extv1alpha1.ManagedResourceDefinitionSpec{
				State: extv1alpha1.ManagedResourceDefinitionPolicyManaged,
				CustomResourceDefinitionSpec: extv1alpha1.CustomResourceDefinitionSpec{
					Group: "example.org",
					Names: extv1.CustomResourceDefinitionNames{Kind: "Thing"},
					Versions: []extv1alpha1.CustomResourceDefinitionVersion{
						{Name: "v1", Storage: true},
					},
				},
			},
		}
		for _, m := range mirror {
			mrd.Status.Activators = append(mrd.Status.Activators, extv1alpha1.Activator{Name: m})
		}
		return mrd
	}

	held := func(name string) extv1alpha1.ManagedResourceDefinition {
		mrd := released(name, "default")
		mrd.Spec.Activators = []extv1alpha1.Activator{{Name: "default"}}
		return mrd
	}

	// getFn serves the CRD (so the type looks installed) and the named MRAPs.
	getFn := func(policies map[string]int64) test.MockGetFn {
		return func(_ context.Context, key client.ObjectKey, obj client.Object) error {
			switch o := obj.(type) {
			case *extv1.CustomResourceDefinition:
				_ = o
				return nil
			case *extv1alpha1.ManagedResourceActivationPolicy:
				observed, ok := policies[key.Name]
				if !ok {
					return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
				}
				o.SetName(key.Name)
				o.SetGeneration(7)
				o.Status.ObservedGeneration = observed
				return nil
			}
			return nil
		}
	}

	// listFn reports the given number of instances for any managed resource type.
	listFn := func(instances int) test.MockListFn {
		return func(_ context.Context, obj client.ObjectList, _ ...client.ListOption) error {
			l, ok := obj.(*kunstructured.UnstructuredList)
			if !ok {
				return nil
			}
			l.Items = make([]kunstructured.Unstructured, instances)
			return nil
		}
	}

	type want struct {
		phase      removalPhase
		mrds       int
		stragglers int
	}

	cases := map[string]struct {
		reason    string
		mrds      []extv1alpha1.ManagedResourceDefinition
		policies  map[string]int64
		instances int
		want      want
	}{
		"NothingQueued": {
			reason:   "An MRD that still has an activator is not a candidate for removal.",
			mrds:     []extv1alpha1.ManagedResourceDefinition{held("a.example.org")},
			policies: map[string]int64{"default": 7},
			want:     want{phase: phaseIdle},
		},
		"SettledPolicyOpensWindow": {
			reason:   "Once every implicated policy has fully reconciled, the window opens.",
			mrds:     []extv1alpha1.ManagedResourceDefinition{released("a.example.org", "default")},
			policies: map[string]int64{"default": 7},
			want:     want{phase: phaseWindowOpen, mrds: 1},
		},
		"UnsettledPolicyHoldsWindowShut": {
			reason:   "A policy that has not finished emitting this generation's removals holds the window shut, so a bulk edit can't be acted on half-applied.",
			mrds:     []extv1alpha1.ManagedResourceDefinition{released("a.example.org", "default")},
			policies: map[string]int64{"default": 6},
			want:     want{phase: phaseAwaitingPolicy, mrds: 1},
		},
		"DeletedPolicyIsSettled": {
			reason:   "A policy that no longer exists removed its entries via its finalizer before it went, so it counts as settled.",
			mrds:     []extv1alpha1.ManagedResourceDefinition{released("a.example.org", "gone")},
			policies: map[string]int64{},
			want:     want{phase: phaseWindowOpen, mrds: 1},
		},
		"UnattributableHoldsWindowShut": {
			reason:   "Without a mirror we can't say which policies to wait on, so we hold rather than guess.",
			mrds:     []extv1alpha1.ManagedResourceDefinition{released("a.example.org")},
			policies: map[string]int64{"default": 7},
			want:     want{phase: phaseAwaitingPolicy, mrds: 1},
		},
		"StragglerDoesNotTravelWithTheBatch": {
			reason:    "A type that still has instances is excluded from the batch and reported separately.",
			mrds:      []extv1alpha1.ManagedResourceDefinition{released("a.example.org", "default")},
			policies:  map[string]int64{"default": 7},
			instances: 3,
			want:      want{phase: phaseIdle, stragglers: 1},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &Reconciler{
				client:   &test.MockClient{MockGet: getFn(tc.policies)},
				uncached: &test.MockClient{MockList: listFn(tc.instances)},
			}

			got, err := r.planRemoval(context.Background(), tc.mrds)
			if err != nil {
				t.Fatalf("r.planRemoval(...): unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.want.phase, got.phase); diff != "" {
				t.Errorf("\n%s\nr.planRemoval(...) phase: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.mrds, len(got.mrds)); diff != "" {
				t.Errorf("\n%s\nr.planRemoval(...) removal set size: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.stragglers, got.stragglers); diff != "" {
				t.Errorf("\n%s\nr.planRemoval(...) stragglers: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
