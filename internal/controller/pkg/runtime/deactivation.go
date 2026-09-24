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
	"fmt"
	"sort"
	"strings"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	kmeta "k8s.io/apimachinery/pkg/api/meta"
	kunstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	extv1alpha1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1alpha1"
)

// A removalPhase is where a revision is in the removal window.
type removalPhase int

const (
	// phaseIdle means nothing is queued for removal.
	phaseIdle removalPhase = iota

	// phaseAwaitingPolicy means removals are queued, but at least one
	// implicated activation policy has not finished reconciling. The window
	// stays shut: acting on a policy change that is only half-applied is how a
	// bulk edit turns into a bulk deletion of the wrong things.
	phaseAwaitingPolicy

	// phaseWindowOpen means every implicated policy has settled, so the
	// runtime may be stopped and the CRDs deleted.
	phaseWindowOpen
)

// A removalBatch is the set of types a revision may deactivate in one window,
// and what that window is currently waiting on.
type removalBatch struct {
	phase   removalPhase
	mrds    []extv1alpha1.ManagedResourceDefinition
	message string

	// stragglers counts types that have lost their last activator but still
	// have instances. They don't travel with the batch; each becomes eligible
	// on its own once it drains, at the cost of a further restart.
	stragglers int
}

// planRemoval computes the removal set for a revision and decides whether its
// window may open.
//
// The removal set is read from MRD spec rather than from the MRD controller's
// PendingRemoval condition. Gating on the condition would make the
// completeness of the batch depend on that controller having caught up - a lag
// the policy's observedGeneration says nothing about. If a policy releases 200
// MRDs and the MRD controller has processed 50, a condition-based test sees 50
// pending, sees the policy settled, and removes a quarter of the batch.
func (r *Reconciler) planRemoval(ctx context.Context, mrds []extv1alpha1.ManagedResourceDefinition) (removalBatch, error) {
	var set []extv1alpha1.ManagedResourceDefinition

	stragglers := 0

	for i := range mrds {
		mrd := mrds[i]
		if !mrd.IsPendingRemoval() {
			continue
		}

		// Already torn down.
		if err := r.client.Get(ctx, types.NamespacedName{Name: mrd.GetName()}, &extv1.CustomResourceDefinition{}); err != nil {
			if kerrors.IsNotFound(err) {
				continue
			}
			return removalBatch{}, errors.Wrap(err, "cannot get CustomResourceDefinition")
		}

		// A type still in use is a straggler. It doesn't travel with the
		// batch; it becomes eligible on its own once it drains, and costs a
		// further restart then.
		empty, err := r.typeIsEmpty(ctx, &mrd)
		if err != nil {
			return removalBatch{}, err
		}
		if !empty {
			stragglers++
			continue
		}

		set = append(set, mrd)
	}

	if len(set) == 0 {
		return removalBatch{phase: phaseIdle, stragglers: stragglers}, nil
	}

	// The implicated policies are the union of the names in the removal set's
	// status.activators. That mirror is written while the spec list is
	// non-empty, so for an MRD that just emptied it holds the previous holders
	// - exactly the question being asked.
	implicated := map[string]bool{}
	var unattributable []string

	for i := range set {
		if len(set[i].Status.Activators) == 0 {
			unattributable = append(unattributable, set[i].GetName())
			continue
		}
		for _, a := range set[i].Status.Activators {
			implicated[a.Name] = true
		}
	}

	if len(unattributable) > 0 {
		// We can't say which policies to wait on, so we can't know the batch is
		// complete. Hold the window shut and surface it rather than guessing.
		sort.Strings(unattributable)
		return removalBatch{
			phase:      phaseAwaitingPolicy,
			mrds:       set,
			stragglers: stragglers,
			message:    fmt.Sprintf("cannot determine which ManagedResourceActivationPolicies released %s", strings.Join(unattributable, ", ")),
		}, nil
	}

	var unsettled []string

	for name := range implicated {
		mrap := &extv1alpha1.ManagedResourceActivationPolicy{}
		err := r.client.Get(ctx, types.NamespacedName{Name: name}, mrap)
		switch {
		case kerrors.IsNotFound(err):
			// A policy that no longer exists is settled: its finalizer removed
			// its entries before it went.
			continue
		case err != nil:
			return removalBatch{}, errors.Wrap(err, "cannot get ManagedResourceActivationPolicy")
		case mrap.GetGeneration() != mrap.Status.ObservedGeneration:
			unsettled = append(unsettled, fmt.Sprintf("%s (generation %d, observed %d)", name, mrap.GetGeneration(), mrap.Status.ObservedGeneration))
		}
	}

	if len(unsettled) > 0 {
		sort.Strings(unsettled)
		return removalBatch{
			phase:      phaseAwaitingPolicy,
			mrds:       set,
			stragglers: stragglers,
			message:    "waiting for " + strings.Join(unsettled, ", ") + " to finish reconciling",
		}, nil
	}

	return removalBatch{phase: phaseWindowOpen, mrds: set, stragglers: stragglers}, nil
}

// release re-checks each MRD in the removal set against a live read and, for
// the survivors, hands it to the MRD controller to tear down.
//
// Stopping the runtime does not make creation impossible - the provider is not
// the only writer of its own managed resources, and Kubernetes offers no
// "delete this CRD only if it has no instances". What the stop guarantees is
// that an instance created in the gap is never reconciled: nothing claims it,
// nothing puts a provider finalizer on it, and no external infrastructure
// comes to exist behind it. The live read is here for the usual case, which is
// not a race but a lag: minutes or hours can pass between an MRD emptying and
// its window opening.
func (r *Reconciler) release(ctx context.Context, batch removalBatch) (int, error) {
	released := 0

	for i := range batch.mrds {
		mrd := &extv1alpha1.ManagedResourceDefinition{}
		if err := r.uncached.Get(ctx, types.NamespacedName{Name: batch.mrds[i].GetName()}, mrd); err != nil {
			return released, errors.Wrap(client.IgnoreNotFound(err), "cannot get ManagedResourceDefinition")
		}

		// Anything that reacquired an activator, left PolicyManaged, or gained
		// an instance drops out of the window and back to PendingRemoval. This
		// is also what makes an abort safe after the window has been entered.
		if !mrd.IsPendingRemoval() {
			continue
		}

		empty, err := r.typeIsEmpty(ctx, mrd)
		if err != nil {
			return released, err
		}
		if !empty {
			continue
		}

		if mrd.IsReleasedForRemoval() {
			released++
			continue
		}

		orig := mrd.DeepCopy()
		if mrd.Annotations == nil {
			mrd.Annotations = map[string]string{}
		}
		mrd.Annotations[extv1alpha1.AnnotationKeyReleasedForRemoval] = "true"

		if err := r.client.Patch(ctx, mrd, client.MergeFrom(orig)); err != nil {
			return released, errors.Wrap(err, "cannot release ManagedResourceDefinition for removal")
		}

		released++
	}

	return released, nil
}

// typeIsEmpty reports whether an MRD's type has no instances. It reads
// uncached, both because the batch must not act on a stale count and because
// listing through the cache would spin up an informer per managed resource
// type.
//
// TODO(POC): the design prefers the protection controller's ClusterUsage as
// the oracle here, but that is behind a feature gate. Listing works either
// way.
func (r *Reconciler) typeIsEmpty(ctx context.Context, mrd *extv1alpha1.ManagedResourceDefinition) (bool, error) {
	var storage string
	for _, v := range mrd.Spec.Versions {
		if v.Storage {
			storage = v.Name
			break
		}
	}

	list := &kunstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   mrd.Spec.Group,
		Version: storage,
		Kind:    mrd.Spec.Names.Kind + "List",
	})

	if err := r.uncached.List(ctx, list, client.Limit(1)); err != nil {
		// If the type isn't served, there can be no instances of it.
		if kerrors.IsNotFound(err) || kmeta.IsNoMatchError(err) {
			return true, nil
		}
		return false, errors.Wrap(err, "cannot list managed resources")
	}

	return len(list.Items) == 0, nil
}
