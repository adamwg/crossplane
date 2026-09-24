package runtime

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	extv1alpha1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1alpha1"
	v1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	"github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
)

// EnqueuePackageRevisionsForRuntimeConfig enqueues a reconcile for all package
// revisions that use a ControllerConfig or DeploymentRuntimeConfig.
func EnqueuePackageRevisionsForRuntimeConfig(kube client.Client, l v1.PackageRevisionList, log logging.Logger) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		rc, ok := o.(*v1beta1.DeploymentRuntimeConfig)
		if !ok {
			return nil
		}

		rl := l.DeepCopyObject().(v1.PackageRevisionList) //nolint:forcetypeassert // Guaranteed to be PackageRevisionList.
		if err := kube.List(ctx, rl); err != nil {
			log.Debug("Cannot list package revisions while attempting to enqueue from runtime config", "error", err)
			return nil
		}

		var matches []reconcile.Request

		for _, rev := range rl.GetRevisions() {
			rt, ok := rev.(v1.PackageRevisionWithRuntime)
			if !ok {
				continue
			}

			ref := rt.GetRuntimeConfigRef()
			if ref != nil && ref.Name == rc.GetName() {
				matches = append(matches, reconcile.Request{NamespacedName: types.NamespacedName{Name: rev.GetName()}})
			}
		}

		return matches
	})
}

// EnqueueProviderRevisionsForMRDs enqueues a reconcile for the provider
// revision that controls a ManagedResourceDefinition, when that MRD is either
// active or a candidate for removal.
func EnqueueProviderRevisionsForMRDs(log logging.Logger) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		mrd, ok := o.(*extv1alpha1.ManagedResourceDefinition)
		if !ok {
			return nil
		}

		// An MRD pending removal is precisely one that is not active, so the
		// old "active only" test would drop exactly the MRDs the removal set is
		// made of.
		if !mrd.IsActive() && !mrd.IsPendingRemoval() {
			return nil
		}

		owner := metav1.GetControllerOf(mrd)
		if owner == nil || owner.Kind != v1.ProviderRevisionKind {
			return nil
		}

		if gv, err := schema.ParseGroupVersion(owner.APIVersion); err != nil || gv.Group != v1.Group {
			return nil
		}

		log.Debug("Enqueuing provider revision for managed resource definition", "mrd", mrd.GetName(), "provider-revision", owner.Name)

		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: owner.Name}}}
	})
}

// EnqueueProviderRevisionsForMRAPs enqueues a reconcile for every provider
// revision when a ManagedResourceActivationPolicy changes.
//
// The fan-out cannot be narrowed: a policy carries no owner reference to a
// revision, and the package manager deliberately never evaluates a policy's
// patterns - it only reads observedGeneration. Revisions number in the
// handful, so enqueuing all of them beats backoff-polling.
func EnqueueProviderRevisionsForMRAPs(kube client.Client, log logging.Logger) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		if _, ok := o.(*extv1alpha1.ManagedResourceActivationPolicy); !ok {
			return nil
		}

		l := &v1.ProviderRevisionList{}
		if err := kube.List(ctx, l); err != nil {
			log.Debug("Cannot list provider revisions while attempting to enqueue from activation policy", "error", err)
			return nil
		}

		matches := make([]reconcile.Request, 0, len(l.Items))
		for i := range l.Items {
			matches = append(matches, reconcile.Request{NamespacedName: types.NamespacedName{Name: l.Items[i].GetName()}})
		}

		return matches
	})
}

// mrdActivationChanged returns a predicate that lets through events for
// ManagedResourceDefinitions the package manager has something to do about:
// one that is active, and one that is a candidate for removal.
//
// Update events require the interesting bit to have changed, to keep MRD
// status churn out of the queue. Both edges matter: the forward one scales a
// safe-start provider up on its first activation, and the reverse one - an MRD
// whose activator set just emptied - opens a removal window. Create events for
// either shape replay after an informer restart.
func mrdActivationChanged() predicate.Funcs {
	interesting := func(o client.Object) bool {
		mrd, ok := o.(*extv1alpha1.ManagedResourceDefinition)
		return ok && (mrd.IsActive() || mrd.IsPendingRemoval())
	}

	active := func(o client.Object) bool {
		mrd, ok := o.(*extv1alpha1.ManagedResourceDefinition)
		return ok && mrd.IsActive()
	}

	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return interesting(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return active(e.ObjectOld) != active(e.ObjectNew) && interesting(e.ObjectNew)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
