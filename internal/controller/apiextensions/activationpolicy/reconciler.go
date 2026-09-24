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

// Package activationpolicy manages the lifecycle of MRAP controllers.
package activationpolicy

import (
	"context"
	"fmt"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/v2/pkg/conditions"
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/crossplane/crossplane/apis/v2/apiextensions/v1alpha1"
)

const (
	timeout = 2 * time.Minute

	// finalizer is removed only once this policy has dropped its activator
	// entry from every ManagedResourceDefinition, so that deleting a policy
	// deactivates the types only it was holding open.
	finalizer = "activators.apiextensions.crossplane.io"
)

// FieldOwner returns the server-side apply field manager a policy writes its
// own activator entry under. It is unique per policy, which is what makes
// removing an entry a pure omission rather than a read-modify-write against
// the other policies' entries.
func FieldOwner(mrapName string) client.FieldOwner {
	return client.FieldOwner("mrap/" + mrapName)
}

// Event reasons.
const (
	reasonPaused       event.Reason = "ReconciliationPaused"
	reasonActivatedMRD event.Reason = "ActivateManagedResourceDefinition"

	// Messages.
	reconcilePausedMsg          = "Reconciliation is paused via the pause annotation"
	reconcileActivateSuccessMsg = "Successfully activated ManagedResourceDefinition"
)

// A Reconciler reconciles ManagedResourceActivationPolicies.
type Reconciler struct {
	client.Client

	log        logging.Logger
	record     event.Recorder
	conditions conditions.Manager
}

// Reconcile a ManagedResourceActivationPolicy, matched ManagedResourceDefinitions are set to Active.
func (r *Reconciler) Reconcile(ogctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.log.WithValues("request", req)
	log.Debug("Reconciling")

	ctx, cancel := context.WithTimeout(ogctx, timeout)
	defer cancel()

	mrap := &v1alpha1.ManagedResourceActivationPolicy{}
	if err := r.Get(ctx, req.NamespacedName, mrap); err != nil {
		// In case object is not found, most likely the object was deleted and
		// then disappeared while the event was in the processing queue. We
		// don't need to take any action in that case.
		log.Debug("cannot get ManagedResourceActivationPolicy", "error", err)
		return reconcile.Result{}, errors.Wrap(resource.IgnoreNotFound(err), "cannot get ManagedResourceActivationPolicy")
	}

	status := r.conditions.For(mrap)

	log = log.WithValues(
		"uid", mrap.GetUID(),
		"version", mrap.GetResourceVersion(),
		"name", mrap.GetName(),
	)

	if meta.WasDeleted(mrap) {
		status.MarkConditions(v1alpha1.TerminatingActivationPolicy())
		if err := r.Status().Update(ogctx, mrap); err != nil {
			log.Debug("cannot update status of ManagedResourceActivationPolicy", "error", err)
			if kerrors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}
			return reconcile.Result{}, errors.Wrap(err, "cannot update status of ManagedResourceActivationPolicy")
		}

		// Drop our activator entry from every MRD before we go, so the types
		// we were the last holder of become eligible for removal.
		if err := r.releaseAll(ctx, mrap); err != nil {
			log.Debug("cannot release ManagedResourceDefinitions", "error", err)
			return reconcile.Result{}, errors.Wrap(err, "cannot release ManagedResourceDefinitions")
		}

		meta.RemoveFinalizer(mrap, finalizer)

		return reconcile.Result{}, errors.Wrap(resource.IgnoreNotFound(r.Update(ogctx, mrap)), "cannot remove finalizer")
	}

	if !meta.FinalizerExists(mrap, finalizer) {
		meta.AddFinalizer(mrap, finalizer)
		if err := r.Update(ctx, mrap); err != nil {
			log.Debug("cannot add finalizer to ManagedResourceActivationPolicy", "error", err)
			return reconcile.Result{}, errors.Wrap(err, "cannot add finalizer")
		}
	}
	// Check for pause annotation
	if meta.IsPaused(mrap) {
		log.Info("reconciliation is paused")
		r.record.Event(mrap, event.Normal(reasonPaused, reconcilePausedMsg))
		return reconcile.Result{}, nil
	}

	// List all MRDs
	mrds := &v1alpha1.ManagedResourceDefinitionList{}
	if err := r.List(ctx, mrds); err != nil {
		log.Debug("cannot list ManagedResourceDefinition", "error", err)

		status.MarkConditions(v1alpha1.BlockedActivationPolicy().WithMessage("cannot list ManagedResourceDefinition"))
		_ = r.Status().Update(ogctx, mrap)

		return reconcile.Result{}, errors.Wrap(err, "cannot list ManagedResourceDefinition")
	}

	// Start fresh.
	mrap.Status.ClearActivated()

	// For each, apply or omit our own activator entry. The API server adds,
	// updates or removes it according to who owns it, so we never need to read
	// another policy's entries or compute a union.
	var errs []error
	for i := range mrds.Items {
		mrd := &mrds.Items[i]
		activates := mrap.Activates(mrd.GetName())

		// Skip MRDs we have never claimed and do not claim now, so that an
		// apply from us doesn't take ownership of their (empty) activator list
		// for no reason.
		if !activates && !holds(mrd, mrap.GetName()) {
			continue
		}

		if err := r.apply(ctx, mrap, mrd, activates); err != nil {
			log.Debug("cannot apply activator entry", "mrd", mrd.GetName(), "error", err)
			errs = append(errs, err)
			r.record.Event(mrap, event.Warning(reasonActivatedMRD, err))
			continue
		}

		if !activates {
			continue
		}

		// Latch the MRD into policy management, always after the apply above.
		// The reverse order would leave a window in which the MRD is
		// PolicyManaged with an empty activator set - the shape the package
		// manager reads as "remove this".
		if !mrd.Spec.State.IsPolicyManaged() {
			if err := r.latch(ctx, mrd); err != nil {
				log.Debug("cannot latch ManagedResourceDefinition to PolicyManaged", "mrd", mrd.GetName(), "error", err)
				errs = append(errs, err)
				r.record.Event(mrap, event.Warning(reasonActivatedMRD, err))
				continue
			}
			r.record.Event(mrap, event.Normal(reasonActivatedMRD, reconcileActivateSuccessMsg))
		}

		mrap.Status.AppendActivated(mrd.GetName())
	}

	if errs != nil {
		status.MarkConditions(v1alpha1.Unhealthy().WithMessage(
			fmt.Sprintf("failed to activate %d of %d ManagedResourceDefinitions", len(errs), len(mrap.Status.Activated))))
	} else {
		// Only advance observedGeneration when every write implied by this
		// generation landed. The package manager treats generation ==
		// observedGeneration as "this policy is fully applied" and opens a
		// removal window on it, so advancing it after a partial failure would
		// act on half a policy change.
		mrap.Status.ObservedGeneration = mrap.GetGeneration()
		status.MarkConditions(v1alpha1.Healthy())
	}

	// TODO: we should really do a diff of the status to see if we should update or not.
	return reconcile.Result{}, errors.Wrap(r.Status().Update(ogctx, mrap), "cannot update status of ManagedResourceActivationPolicy")
}

// apply server-side applies this policy's activator entry on the MRD, or omits
// it when the policy no longer matches, under a field manager unique to the
// policy. An omission is what makes the API server drop the entry.
func (r *Reconciler) apply(ctx context.Context, mrap *v1alpha1.ManagedResourceActivationPolicy, mrd *v1alpha1.ManagedResourceDefinition, activates bool) error {
	patch := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.SchemeGroupVersion.String(),
		"kind":       v1alpha1.ManagedResourceDefinitionKind,
		"metadata":   map[string]any{"name": mrd.GetName()},
		"spec":       map[string]any{"activators": []any{}},
	}}

	if activates {
		patch.Object["spec"] = map[string]any{"activators": []any{
			map[string]any{
				"name":       mrap.GetName(),
				"kind":       v1alpha1.ManagedResourceActivationPolicyKind,
				"generation": mrap.GetGeneration(),
			},
		}}
	}

	//nolint:staticcheck // TODO(adamwg): Stop using client.Apply after the v2.2 release.
	return errors.Wrap(r.Patch(ctx, patch, client.Apply, client.ForceOwnership, FieldOwner(mrap.GetName())),
		"cannot apply activator entry")
}

// latch moves an MRD to PolicyManaged. It is a separate patch under a shared
// field manager rather than part of the apply above: spec.state is a single
// field that every matching policy would co-own under server-side apply, and
// the API server would remove it when the last of them dropped its pattern -
// defaulting it back to Inactive and unmarking precisely the MRDs meant to be
// removed. The CEL rule on spec.state makes this latch idempotent and one-way.
func (r *Reconciler) latch(ctx context.Context, mrd *v1alpha1.ManagedResourceDefinition) error {
	orig := mrd.DeepCopy()
	mrd.Spec.State = v1alpha1.ManagedResourceDefinitionPolicyManaged
	return errors.Wrap(r.Patch(ctx, mrd, client.MergeFrom(orig)), "cannot patch state")
}

// releaseAll drops this policy's activator entry from every MRD that holds
// one. It runs on deletion, before the finalizer is removed.
func (r *Reconciler) releaseAll(ctx context.Context, mrap *v1alpha1.ManagedResourceActivationPolicy) error {
	mrds := &v1alpha1.ManagedResourceDefinitionList{}
	if err := r.List(ctx, mrds); err != nil {
		return errors.Wrap(err, "cannot list ManagedResourceDefinition")
	}

	for i := range mrds.Items {
		if !holds(&mrds.Items[i], mrap.GetName()) {
			continue
		}
		if err := r.apply(ctx, mrap, &mrds.Items[i], false); err != nil {
			return err
		}
	}

	return nil
}

// holds reports whether the named policy has an activator entry on the MRD.
func holds(mrd *v1alpha1.ManagedResourceDefinition, name string) bool {
	for _, a := range mrd.Spec.Activators {
		if a.Name == name {
			return true
		}
	}
	return false
}
