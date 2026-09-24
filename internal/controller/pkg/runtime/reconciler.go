/*
Copyright 2020 The Crossplane Authors.

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

// Package runtime implements "Deployment" runtime for Crossplane packages.
package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/v2/pkg/conditions"
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	extv1alpha1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1alpha1"
	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	v1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
	"github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"
	"github.com/crossplane/crossplane/v2/internal/controller/pkg/controller"
	"github.com/crossplane/crossplane/v2/internal/controller/pkg/revision"
	"github.com/crossplane/crossplane/v2/internal/features"
)

const (
	reconcileTimeout = 3 * time.Minute

	// deactivationPollInterval is how often we re-check a removal window that
	// is waiting on the runtime to stop or on the MRD controller to delete the
	// CRDs.
	deactivationPollInterval = 5 * time.Second

	// stragglerPollInterval is how often we re-check a type that has lost its
	// last activator but still has instances.
	stragglerPollInterval = 1 * time.Minute
)

const (
	errGetPackageRevision = "cannot get package revision"
	errUpdateStatus       = "cannot update package revision status"

	errGetPullConfig = "cannot get image pull secret from config"

	errManifestBuilderOptions = "cannot prepare runtime manifest builder options"
	errPreHook                = "pre establish runtime hook failed for package"
	errPostHook               = "post establish runtime hook failed for package"
	errDeactivateHook         = "deactivation runtime hook failed for package"

	errNoRuntimeConfig          = "no deployment runtime config set"
	errGetRuntimeConfig         = "cannot get referenced deployment runtime config"
	errUnknownKindRuntimeConfig = "runtime config is set but is an unknown apiVersion and kind"
	errGetServiceAccount        = "cannot get Crossplane service account"

	errListMRDs = "cannot list ManagedResourceDefinitions to determine whether the provider runtime can start"
)

// Event reasons.
const (
	reasonImageConfig event.Reason = "FetchResolvedImageConfig"
	reasonSync        event.Reason = "SyncPackage"
	reasonDeactivate  event.Reason = "DeactivateRevision"
)

// ReconcilerOption is used to configure the Reconciler.
type ReconcilerOption func(*Reconciler)

// WithNewPackageRevisionWithRuntimeFn determines the type of package being reconciled.
func WithNewPackageRevisionWithRuntimeFn(f func() v1.PackageRevisionWithRuntime) ReconcilerOption {
	return func(r *Reconciler) {
		r.newPackageRevisionWithRuntime = f
	}
}

// WithLogger specifies how the Reconciler should log messages.
func WithLogger(log logging.Logger) ReconcilerOption {
	return func(r *Reconciler) {
		r.log = log
	}
}

// WithRecorder specifies how the Reconciler should record Kubernetes events.
func WithRecorder(er event.Recorder) ReconcilerOption {
	return func(r *Reconciler) {
		r.record = er
	}
}

// WithRuntimeHooks specifies how the Reconciler should perform preparations
// (pre- and post-establishment) and cleanup (deactivate) for package runtime.
// The hooks are only used when the package has a runtime and the runtime is
// configured as Deployment.
func WithRuntimeHooks(h Hooks) ReconcilerOption {
	return func(r *Reconciler) {
		r.runtimeHook = h
	}
}

// WithNamespace specifies the namespace in which the Reconciler should create
// runtime resources.
func WithNamespace(n string) ReconcilerOption {
	return func(r *Reconciler) {
		r.namespace = n
	}
}

// WithServiceAccount specifies the core Crossplane ServiceAccount name.
func WithServiceAccount(sa string) ReconcilerOption {
	return func(r *Reconciler) {
		r.serviceAccount = sa
	}
}

// WithFeatureFlags specifies the feature flags to inject into the Reconciler.
func WithFeatureFlags(f *feature.Flags) ReconcilerOption {
	return func(r *Reconciler) {
		r.features = f
	}
}

// WithDeploymentSelectorMigrator specifies the deployment selector migrator
// to use for handling provider deployment selector migrations.
func WithDeploymentSelectorMigrator(m DeploymentSelectorMigrator) ReconcilerOption {
	return func(r *Reconciler) {
		r.migrator = m
	}
}

// WithUncachedReader specifies a reader that goes straight to the API server,
// for the live re-check a removal window makes with the runtime stopped.
func WithUncachedReader(rd client.Reader) ReconcilerOption {
	return func(r *Reconciler) {
		r.uncached = rd
	}
}

// WithConfigStore specifies how the Reconciler should access image config store.
func WithConfigStore(c xpkg.ConfigStore) ReconcilerOption {
	return func(r *Reconciler) {
		r.pkgConfig = c
	}
}

// Reconciler reconciles packages.
type Reconciler struct {
	client client.Client

	// uncached reads straight from the API server, for the live re-check the
	// removal window makes with the runtime stopped.
	uncached client.Reader

	log            logging.Logger
	runtimeHook    Hooks
	record         event.Recorder
	conditions     conditions.Manager
	features       *feature.Flags
	migrator       DeploymentSelectorMigrator
	namespace      string
	serviceAccount string
	pkgConfig      xpkg.ConfigStore

	newPackageRevisionWithRuntime func() v1.PackageRevisionWithRuntime
}

// SetupProviderRevision adds a controller that reconciles ProviderRevisions.
func SetupProviderRevision(mgr ctrl.Manager, o controller.Options) error {
	name := "package-runtime/" + strings.ToLower(v1.ProviderRevisionGroupKind)
	nr := func() v1.PackageRevisionWithRuntime { return &v1.ProviderRevision{} }

	log := o.Logger.WithValues("controller", name)
	cb := ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1.ProviderRevision{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Watches(&v1beta1.ImageConfig{}, revision.EnqueuePackageRevisionsForImageConfig(mgr.GetClient(), &v1.ProviderRevisionList{}, log))

	if o.Features.Enabled(features.EnableBetaDeploymentRuntimeConfigs) {
		cb = cb.Watches(&v1beta1.DeploymentRuntimeConfig{}, EnqueuePackageRevisionsForRuntimeConfig(mgr.GetClient(), &v1.ProviderRevisionList{}, log))
	}

	// Watch MRDs so we can scale up a safe-start provider's runtime the moment
	// its first MRD becomes active, and scale it down again when one becomes a
	// candidate for removal.
	cb = cb.Watches(&extv1alpha1.ManagedResourceDefinition{}, EnqueueProviderRevisionsForMRDs(log), builder.WithPredicates(mrdActivationChanged()))

	// Watch MRAPs too. When a policy settles, nothing about any MRD changes -
	// its activator writes all landed before observedGeneration advanced, so
	// those MRD events have already fired and found the window shut. The
	// policy's own status update is the only signal left. Its patterns aren't
	// evaluated here and it carries no owner reference to a revision, so the
	// handler can't narrow the fan-out - but revisions number in the handful.
	cb = cb.Watches(&extv1alpha1.ManagedResourceActivationPolicy{}, EnqueueProviderRevisionsForMRAPs(mgr.GetClient(), log))

	r := NewReconciler(mgr,
		WithNewPackageRevisionWithRuntimeFn(nr),
		WithLogger(log),
		WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name), o.EventFilterFunctions...)),
		WithNamespace(o.Namespace),
		WithServiceAccount(o.ServiceAccount),
		WithRuntimeHooks(NewProviderHooks(mgr.GetClient())),
		WithUncachedReader(mgr.GetAPIReader()),
		WithFeatureFlags(o.Features),
		WithDeploymentSelectorMigrator(NewDeletingDeploymentSelectorMigrator(mgr.GetClient(), log)),
		WithConfigStore(xpkg.NewImageConfigStore(mgr.GetClient(), o.Namespace)),
	)

	return cb.WithOptions(o.ForControllerRuntime()).
		Complete(errors.WithSilentRequeueOnConflict(r))
}

// SetupFunctionRevision adds a controller that reconciles FunctionRevisions.
func SetupFunctionRevision(mgr ctrl.Manager, o controller.Options) error {
	name := "package-runtime/" + strings.ToLower(v1.FunctionRevisionGroupKind)
	nr := func() v1.PackageRevisionWithRuntime { return &v1.FunctionRevision{} }

	log := o.Logger.WithValues("controller", name)
	cb := ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1.FunctionRevision{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Watches(&v1beta1.ImageConfig{}, revision.EnqueuePackageRevisionsForImageConfig(mgr.GetClient(), &v1.FunctionRevisionList{}, log))

	if o.Features.Enabled(features.EnableBetaDeploymentRuntimeConfigs) {
		cb = cb.Watches(&v1beta1.DeploymentRuntimeConfig{}, EnqueuePackageRevisionsForRuntimeConfig(mgr.GetClient(), &v1.FunctionRevisionList{}, log))
	}

	r := NewReconciler(mgr,
		WithNewPackageRevisionWithRuntimeFn(nr),
		WithLogger(log),
		WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name), o.EventFilterFunctions...)),
		WithNamespace(o.Namespace),
		WithServiceAccount(o.ServiceAccount),
		WithRuntimeHooks(NewFunctionHooks(mgr.GetClient())),
		WithFeatureFlags(o.Features),
		WithConfigStore(xpkg.NewImageConfigStore(mgr.GetClient(), o.Namespace)),
	)

	return cb.WithOptions(o.ForControllerRuntime()).
		Complete(errors.WithSilentRequeueOnConflict(r))
}

// NewReconciler creates a new package revision reconciler.
func NewReconciler(mgr manager.Manager, opts ...ReconcilerOption) *Reconciler {
	r := &Reconciler{
		client:     mgr.GetClient(),
		log:        logging.NewNopLogger(),
		record:     event.NewNopRecorder(),
		conditions: conditions.ObservedGenerationPropagationManager{},
		migrator:   NewNopDeploymentSelectorMigrator(),
	}

	for _, f := range opts {
		f(r)
	}

	return r
}

// Reconcile package revision.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.log.WithValues("request", req)
	log.Debug("Reconciling")

	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()

	pr := r.newPackageRevisionWithRuntime()
	if err := r.client.Get(ctx, req.NamespacedName, pr); err != nil {
		// There's no need to requeue if we no longer exist. Otherwise
		// we'll be requeued implicitly because we return an error.
		log.Debug(errGetPackageRevision, "error", err)
		return reconcile.Result{}, errors.Wrap(resource.IgnoreNotFound(err), errGetPackageRevision)
	}

	status := r.conditions.For(pr)

	log = log.WithValues(
		"uid", pr.GetUID(),
		"version", pr.GetResourceVersion(),
		"name", pr.GetName(),
	)

	// Check the pause annotation and return if it has the value "true"
	// after logging, publishing an event and updating the SYNC status condition
	if meta.IsPaused(pr) {
		log.Debug("Package revision is paused, skipping reconciliation")
		// Don't update conditions - other controllers manage Synced/ReconcilePaused conditions
		return reconcile.Result{}, nil
	}

	if meta.WasDeleted(pr) {
		log.Debug("Package revision is deleted, skipping reconciliation")
		return reconcile.Result{}, nil
	}

	var pullSecretFromConfig string
	// Read applied image config for SetImagePullSecret from the package
	// revision status, so that we can use the same pull secret without having
	// to resolve it again.
	for _, icr := range pr.GetAppliedImageConfigRefs() {
		if icr.Reason == v1.ImageConfigReasonSetPullSecret {
			// Get applied image config to find the pull secret.
			ic := &v1beta1.ImageConfig{}
			if err := r.client.Get(ctx, types.NamespacedName{Name: icr.Name}, ic); err != nil {
				err = errors.Wrap(err, errGetPullConfig)
				status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

				_ = r.client.Status().Update(ctx, pr)
				r.record.Event(pr, event.Warning(reasonImageConfig, err))

				return reconcile.Result{}, err
			}

			pullSecretFromConfig = ic.Spec.Registry.Authentication.PullSecretRef.Name

			break
		}
	}

	// Initialize the runtime manifest builder with the package revision
	opts, err := r.builderOptions(ctx, pr)
	if err != nil {
		log.Debug(errManifestBuilderOptions, "error", err)
		err = errors.Wrap(err, errManifestBuilderOptions)
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

		_ = r.client.Status().Update(ctx, pr)
		r.record.Event(pr, event.Warning(reasonSync, err))

		return reconcile.Result{}, err
	}

	if pullSecretFromConfig != "" {
		opts = append(opts, BuilderWithPullSecrets(pullSecretFromConfig))
	}

	ownedMRDs, err := r.ownedMRDs(ctx, pr)
	if err != nil {
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

		_ = r.client.Status().Update(ctx, pr)
		r.record.Event(pr, event.Warning(reasonSync, err))

		return reconcile.Result{}, err
	}

	// Work out whether any of our types are queued for removal, and whether
	// every policy implicated in that removal has finished reconciling.
	batch, err := r.planRemoval(ctx, ownedMRDs)
	if err != nil {
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

		_ = r.client.Status().Update(ctx, pr)
		r.record.Event(pr, event.Warning(reasonDeactivate, err))

		return reconcile.Result{}, err
	}

	opts = append(opts, BuilderWithMRDs(ownedMRDs))
	if batch.phase == phaseWindowOpen {
		// Stop the runtime for the length of the window. A provider that keeps
		// watching a type whose CRD has been removed errors continuously, and
		// controller-runtime has no supported way to stop a controller and
		// tear down its informers - so we stop the process instead.
		opts = append(opts, BuilderDeactivating())
	}
	builder := NewDeploymentRuntimeBuilder(pr, r.namespace, opts...)

	// Deactivate revision if it is inactive.
	if pr.GetDesiredState() == v1.PackageRevisionInactive {
		if err := r.runtimeHook.Deactivate(ctx, pr, builder); err != nil {
			if kerrors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}

			err = errors.Wrap(err, errDeactivateHook)
			// A revision whose deactivation fails still has a runtime deployment
			// running, so it keeps serving requests it should have handed over.
			// Say so, rather than leaving the conditions it reported while it
			// was still the active revision.
			status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

			_ = r.client.Status().Update(ctx, pr)
			r.record.Event(pr, event.Warning(reasonDeactivate, err))

			return reconcile.Result{}, err
		}

		status.MarkConditions(v1.RuntimeHealthy())

		return reconcile.Result{Requeue: false}, errors.Wrap(r.client.Status().Update(ctx, pr), errUpdateStatus)
	}

	// Migrate provider deployment selector, if needed.
	if err := r.migrator.MigrateDeploymentSelector(ctx, pr, builder); err != nil {
		err = errors.Wrap(err, "failed to run deployment selector migration")
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

		_ = r.client.Status().Update(ctx, pr)
		r.record.Event(pr, event.Warning(reasonSync, err))

		return reconcile.Result{}, err
	}

	// Run pre-establish hooks
	if err := r.runtimeHook.Pre(ctx, pr, builder); err != nil {
		if kerrors.IsConflict(err) {
			return reconcile.Result{Requeue: true}, nil
		}

		err = errors.Wrap(err, errPreHook)
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

		_ = r.client.Status().Update(ctx, pr)
		r.record.Event(pr, event.Warning(reasonSync, err))

		return reconcile.Result{}, err
	}

	// Wait for the package revision to be healthy before running the
	// post-establish hooks.
	if pr.GetCondition(v1.TypeRevisionHealthy).Status != corev1.ConditionTrue {
		log.Debug("Waiting for the package revision to be healthy before running post-establish hooks")
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage("Package revision is not healthy yet"))

		return reconcile.Result{}, errors.Wrap(r.client.Status().Update(ctx, pr), errUpdateStatus)
	}

	// Run post-establish hooks
	if err := r.runtimeHook.Post(ctx, pr, builder); err != nil {
		if kerrors.IsConflict(err) {
			return reconcile.Result{Requeue: true}, nil
		}

		err = errors.Wrap(err, errPostHook)
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

		_ = r.client.Status().Update(ctx, pr)
		r.record.Event(pr, event.Warning(reasonSync, err))

		return reconcile.Result{}, err
	}

	if pr.GetCondition(v1.TypeRuntimeHealthy).Status != corev1.ConditionTrue {
		// We don't want to spam the user with events if the package revision is
		// already healthy.
		r.record.Event(pr, event.Normal(reasonSync, "Successfully configured package revision"))
	}

	res, err := r.markRuntimeState(ctx, log, pr, builder, batch)
	if err != nil {
		status.MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))

		_ = r.client.Status().Update(ctx, pr)
		r.record.Event(pr, event.Warning(reasonDeactivate, err))

		return reconcile.Result{}, err
	}

	return res, errors.Wrap(r.client.Status().Update(ctx, pr), errUpdateStatus)
}

// markRuntimeState reports where the runtime is - up, scaled to zero awaiting
// its first activation, or part way through a removal window - and drives the
// window when one is open.
func (r *Reconciler) markRuntimeState(ctx context.Context, log logging.Logger, pr v1.PackageRevisionWithRuntime, build *DeploymentRuntimeBuilder, batch removalBatch) (reconcile.Result, error) {
	status := r.conditions.For(pr)

	if batch.phase == phaseWindowOpen {
		return r.removalWindow(ctx, log, pr, build, batch)
	}

	// A straggler - a type that lost its last activator but still has instances
	// - becomes eligible only when whoever is draining it finishes, which
	// nothing here can observe. Poll for it.
	//
	// TODO(POC): the design instead widens the MRD watch to carry the "instance
	// count reached zero" edge, which needs a watch on the instances rather
	// than on the MRD.
	res := reconcile.Result{Requeue: false}
	if batch.stragglers > 0 {
		res = reconcile.Result{RequeueAfter: stragglerPollInterval}
	}

	switch {
	case batch.phase == phaseAwaitingPolicy:
		status.MarkConditions(
			v1.RuntimeHealthy(),
			v1.RuntimeActive(),
			v1.Deactivating(v1.ReasonAwaitingPolicy).WithMessage(fmt.Sprintf("%d ManagedResourceDefinitions queued for removal; %s", len(batch.mrds), batch.message)),
		)

	case build.AwaitingActivation():
		status.MarkConditions(v1.RuntimeHealthy(), v1.RuntimeAwaitingActivation().WithMessage("Package runtime is scaled to zero; awaiting the first ManagedResourceDefinition to be activated"))
		markNotDeactivating(status, pr)

	case batch.stragglers > 0:
		status.MarkConditions(
			v1.RuntimeHealthy(),
			v1.RuntimeActive(),
			v1.Deactivating(v1.ReasonAwaitingPolicy).WithMessage(fmt.Sprintf("%d ManagedResourceDefinitions are queued for removal but still have instances", batch.stragglers)),
		)

	default:
		status.MarkConditions(v1.RuntimeHealthy(), v1.RuntimeActive())
		markNotDeactivating(status, pr)
	}

	return res, nil
}

// markNotDeactivating closes out a removal window that has finished. It only
// writes the condition when one was open, so that a revision that has never
// deactivated anything doesn't carry a permanent "Deactivating: False".
func markNotDeactivating(status conditions.ConditionSet, pr v1.PackageRevisionWithRuntime) {
	if pr.GetCondition(v1.TypeDeactivating).Status == corev1.ConditionTrue {
		status.MarkConditions(v1.NotDeactivating())
	}
}

// removalWindow runs the window once every implicated policy has settled: wait
// for the runtime to actually be down, re-check the batch against a live read,
// and release the survivors for the MRD controller to tear down.
//
// It does not need to sequence the scale-up. Once the CRDs are gone the MRDs
// drop out of the removal set, the next reconcile plans an idle batch, and the
// Deployment goes back to its configured replica count.
func (r *Reconciler) removalWindow(ctx context.Context, log logging.Logger, pr v1.PackageRevisionWithRuntime, build *DeploymentRuntimeBuilder, batch removalBatch) (reconcile.Result, error) {
	status := r.conditions.For(pr)

	// The post-establish hook has already applied the Deployment at zero
	// replicas. Wait for the pods to actually be gone before we delete
	// anything: the point of the stop is that nothing is reconciling the type
	// when its CRD disappears.
	sa := build.ServiceAccount()
	want := build.Deployment(sa.Name)

	d := &appsv1.Deployment{}
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: want.GetNamespace(), Name: want.GetName()}, d); err != nil {
		return reconcile.Result{}, errors.Wrap(resource.IgnoreNotFound(err), "cannot get package runtime deployment")
	}

	if d.Status.Replicas > 0 {
		log.Debug("Waiting for package runtime to stop before removing CRDs", "replicas", d.Status.Replicas)
		status.MarkConditions(
			v1.RuntimeHealthy(),
			v1.RuntimeDeactivating(),
			v1.Deactivating(v1.ReasonStopping).WithMessage(fmt.Sprintf("Stopping runtime before removing %d CRDs; %d replicas remain", len(batch.mrds), d.Status.Replicas)),
		)

		return reconcile.Result{RequeueAfter: deactivationPollInterval}, nil
	}

	released, err := r.release(ctx, batch)
	if err != nil {
		return reconcile.Result{}, err
	}

	log.Info("Runtime stopped; releasing ManagedResourceDefinitions for removal", "count", released)

	status.MarkConditions(
		v1.RuntimeHealthy(),
		v1.RuntimeDeactivating(),
		v1.Deactivating(v1.ReasonRemovingCRDs).WithMessage(fmt.Sprintf("Runtime stopped; deleting %d CRDs", released)),
	)

	return reconcile.Result{RequeueAfter: deactivationPollInterval}, nil
}

// ownedMRDs returns the ManagedResourceDefinitions controlled by pr.
// Returns nil without listing if pr does not have the safe-start capability.
func (r *Reconciler) ownedMRDs(ctx context.Context, pr v1.PackageRevisionWithRuntime) ([]extv1alpha1.ManagedResourceDefinition, error) {
	if !pkgmetav1.CapabilitiesContainFuzzyMatch(pr.GetCapabilities(), pkgmetav1.ProviderCapabilitySafeStart) {
		return nil, nil
	}

	mrds := &extv1alpha1.ManagedResourceDefinitionList{}
	if err := r.client.List(ctx, mrds); err != nil {
		return nil, errors.Wrap(err, errListMRDs)
	}

	var owned []extv1alpha1.ManagedResourceDefinition
	for i := range mrds.Items {
		if metav1.IsControlledBy(&mrds.Items[i], pr) {
			owned = append(owned, mrds.Items[i])
		}
	}
	return owned, nil
}

func (r *Reconciler) builderOptions(ctx context.Context, pwr v1.PackageRevisionWithRuntime) ([]BuilderOption, error) {
	var opts []BuilderOption

	if r.features.Enabled(features.EnableBetaDeploymentRuntimeConfigs) {
		rcRef := pwr.GetRuntimeConfigRef()
		if rcRef == nil {
			return nil, errors.New(errNoRuntimeConfig)
		}

		// Find any ImageConfigs that override the runtime config.
		configName, runtimeConfig, err := r.pkgConfig.RuntimeConfigFor(ctx, pwr.GetResolvedSource())
		if err != nil {
			err = errors.Wrapf(err, "failed to look up runtime ImageConfig for %s", pwr.GetResolvedSource())
			r.conditions.For(pwr).MarkConditions(v1.RuntimeUnhealthy().WithMessage(err.Error()))
			r.record.Event(pwr, event.Warning(reasonImageConfig, err))

			return nil, err
		}
		if runtimeConfig != nil && runtimeConfig.ConfigReference != nil {
			rcRef = &v1.RuntimeConfigReference{
				APIVersion: runtimeConfig.ConfigReference.APIVersion,
				Kind:       runtimeConfig.ConfigReference.Kind,
				Name:       runtimeConfig.ConfigReference.Name,
			}

			pwr.SetAppliedImageConfigRefs(v1.ImageConfigRef{
				Name:   configName,
				Reason: v1.ImageConfigReasonRuntime,
			})
		} else {
			pwr.ClearAppliedImageConfigRef(v1.ImageConfigReasonRuntime)
		}

		if rcRef.Kind != nil && rcRef.APIVersion != nil &&
			(*rcRef.Kind != v1beta1.DeploymentRuntimeConfigKind && *rcRef.APIVersion != v1beta1.SchemeGroupVersion.String()) {
			return nil, errors.New(errUnknownKindRuntimeConfig)
		}

		rc := &v1beta1.DeploymentRuntimeConfig{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: rcRef.Name}, rc); err != nil {
			return nil, errors.Wrap(err, errGetRuntimeConfig)
		}

		opts = append(opts, BuilderWithRuntimeConfig(rc))
	}

	sa := &corev1.ServiceAccount{}
	// Fetch XP ServiceAccount to get the ImagePullSecrets defined there.
	// We will append them to the list of ImagePullSecrets for the runtime
	// ServiceAccount.
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: r.serviceAccount}, sa); err != nil {
		return nil, errors.Wrap(err, errGetServiceAccount)
	}

	if len(sa.ImagePullSecrets) > 0 {
		opts = append(opts, BuilderWithServiceAccountPullSecrets(sa.ImagePullSecrets))
	}

	return opts, nil
}
