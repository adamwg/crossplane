# Managed Resource Definition Deactivation

* Owner: Adam Wolfe Gordon (@adamwg)
* Reviewers: Crossplane Maintainers
* Status: Draft

## Background

Crossplane v2 lets a `ManagedResourceActivationPolicy` (MRAP) promote a
`ManagedResourceDefinition` (MRD) from `Inactive` to `Active`, which causes the
MRD controller to create the corresponding CRD. The transition is one-way: a
CEL rule on `spec.state` rejects `Active -> Inactive`, and the MRAP controller
only ever sets MRDs to `Active`. Users who upgrade to v2 with the default `*`
MRAP end up with every provider CRD installed and no supported way to remove
them, even after narrowing their MRAPs ([#6984][issue-6984]).

Two things make the reverse transition hard ([#6803 comment][issue-6803],
[#7205 review][pr-7205]):

1. **Instances.** Deleting a CRD deletes every instance of it. Worse, MRs
   typically carry a provider finalizer; if the reconciler is already gone the
   finalizer is never removed and the CRD hangs in `Terminating` forever. And
   for MRs whose deletion policy is `Delete`, cascading deletion means deleting
   real external infrastructure.
2. **Reconcilers.** A provider that keeps watching a type whose CRD has been
   removed will error continuously. Providers use
   [`customresourcesgate`][gate] to *start* MR controllers when a CRD appears,
   but controller-runtime has no supported way to *stop* a controller and tear
   down its informers. Crossplane solved this for XRs with its own
   [controller engine][engine]; providers have no equivalent. Providers without
   the `safe-start` capability are worse still: they assume every CRD exists at
   startup and crash-loop if one does not.

This design deactivates a type by deleting its CRD while the provider process
that reconciles it is stopped, and batches the deletions caused by a single
policy change into one provider restart.

## Goals

* A type that no MRAP asks for has its CRD removed, without operator action.
* A bulk MRAP edit that deactivates many types costs one provider restart per
  provider, not one per type — for the types that are empty when the policy
  changes. A type still in use at that moment is a *straggler* and costs a
  further restart when it drains; see *Stragglers* under
  [the package manager](#package-manager).
* Nothing is destroyed as a side effect of a partially-applied or failing
  policy change.
* No new policy API. Deactivation follows from the MRAPs alone.

## Non-goals

* Deactivating a type that still has instances. The user drains it first.
* Supporting providers without the `safe-start` capability.
* Teaching providers to stop a controller in place. See
  [Future work](#future-work-safe-stop).

## Preconditions

Both are assumed throughout and are not the interesting part of the design:

* Deactivation is only offered for providers that advertise `safe-start`. For
  anything else it is refused and the reason is reported on the MRD.
* A CRD is only deleted after Crossplane has confirmed that no instances of it
  exist, re-checked against a live read while nothing is reconciling the type.

## Overview

Today `spec.state` on the MRD is a single bit that collapses "who wants this
type" into "someone did, once". That is why the transition cannot be reversed:
the bit records that an activation happened but not whether it is still
wanted, and it has no owner that can speak for the whole cluster — the MRAP
controller is keyed on one MRAP and can only see its own patterns.

Replace the bit with the set. Each MRD carries a list of **activators**, and
each MRAP writes only its own entry, via server-side apply, as its own field
manager. Dropping a pattern is an apply that omits the entry; the API server
removes it because that manager owned that field. No participant ever computes
a union, reads another MRAP, or writes a field it does not own. The union is
maintained by the API server, and "is this type still wanted" becomes a local
question answered by looking at one object.

`spec.state` stays, and gains a third value. `PolicyManaged` says that this
MRD's activation is decided by its activator set; `Active` keeps its present
meaning of *pinned by hand*. Only a `PolicyManaged` MRD is ever deactivated, so
a type that policy never claimed cannot be removed by one.

Removal is then batched by causality rather than by a timer. An MRAP that has
finished reconciling a generation has, by construction, finished emitting every
activator removal that generation implies, and it says so with
`status.observedGeneration`. The package manager waits for every MRAP
implicated in a pending removal to reach that point, then cycles the provider
once and deletes the CRDs while no pod is watching them.

Everything fails toward keeping the type. A leaked activator entry, an MRAP
that never settles, an unattributable MRD, and a Crossplane restart mid-flight
all stall a deactivation rather than completing one.

```
  per MRD:       Active --last activator removed--> PendingRemoval --window--> Inactive
                    ^                                     |
                    '--------- activator re-added --------'

  per revision:  Idle --removals pending, all implicated MRAPs settled--> Deactivating
                   ^                                                           |
                   '-------------- CRDs deleted, runtime back up --------------'
```

## API changes

### `ManagedResourceDefinition.spec.activators`

```yaml
apiVersion: apiextensions.crossplane.io/v1alpha1
kind: ManagedResourceDefinition
metadata:
  name: instances.ec2.aws.upbound.io
spec:
  activators:
  - name: default
    kind: ManagedResourceActivationPolicy
    generation: 3
  - name: ec2-only
    kind: ManagedResourceActivationPolicy
    generation: 7
```

`+listType=map`, `+listMapKey=name`, so the API server tracks ownership per
entry. Each MRAP applies its entry with a field manager unique to it
(`mrap/<name>`), which is what makes removal a pure omission rather than a
read-modify-write against other MRAPs.

`kind` distinguishes a policy-driven entry from one written by a human: a
person who applies an entry under their own field manager holds a
`PolicyManaged` type open without pinning it with `state: Active`, and
`kubectl get mrd -o yaml` shows who. `generation` is the writing MRAP's
`metadata.generation` at the time of the write. It is diagnostic — it tells you
at a glance whether an entry is current — and is deliberately not what the
removal window waits on; see [the package manager](#package-manager).

### `ManagedResourceDefinition.spec.state`

Not deprecated. It gains a third value, `PolicyManaged`, and becomes the
discriminator between the two activation regimes rather than a single bit:

```
+kubebuilder:validation:Enum=Active;Inactive;PolicyManaged
```

| `spec.state` | `spec.activators` | CRD |
| --- | --- | --- |
| `Inactive` | empty | absent |
| `Active` | any | present — pinned; never deactivated |
| `PolicyManaged` | non-empty | present |
| `PolicyManaged` | empty | eligible for removal |

`Active` therefore keeps exactly its current meaning, and a type somebody
turned on by hand is never removed from under them. Activity is now a question
about the pair:

```
effectiveState = (spec.state == Active) || len(spec.activators) > 0
```

`IsActive()` (`mrd_types.go:52`) becomes that expression, and its callers
(`managed/reconciler.go:120`, `pkg/runtime/runtime.go:277`,
`pkg/runtime/watch.go:64`, `pkg/revision/establisher.go:702`) all want it.

The field stays one-way. Its CEL rule (`mrd_types.go:36`) is widened rather
than dropped:

```
self == oldSelf || oldSelf == 'Inactive' || self == 'PolicyManaged'
```

`Inactive -> Active`, `Inactive -> PolicyManaged` and `Active -> PolicyManaged`
are allowed; `Active -> Inactive` is still refused, and nothing leaves
`PolicyManaged`. Handing a type to policy management is a decision you make
once and do not walk back, which is what makes `PolicyManaged` with an empty
activator set unambiguous rather than merely unclaimed-so-far. It also gives
users a manual deactivation path they do not have today: set `PolicyManaged` on
a type no MRAP matches and the same machinery removes it.

Because the rule accepts a superset of what it accepted before, widening it is
safe on a type already installed in real clusters — no stored object becomes
invalid. See [Migration](#migration).

### `ManagedResourceDefinition.status.activators`

A mirror of the last observed non-empty `spec.activators`, written by the MRD
controller. When the spec list empties, this retains the previous holders,
which is how a pending removal says who released it. Its staleness is the
point, and it is derivable from the MRAPs' patterns, so it is legitimately
status rather than spec.

### `ManagedResourceDefinition` conditions

`Established=False` gains the reason `PendingRemoval`, whose message carries
the live instance count and the MRAPs being waited on.

### `ManagedResourceActivationPolicy.status.observedGeneration`

New top-level field. `ManagedResourceActivationPolicyStatus` currently holds
only `ConditionedStatus` and `Activated` (`mrd_policy_types.go:46`), and the
reconciler's `ObservedGenerationPropagationManager` stamps the generation onto
conditions. Gating on an explicit field is better than digging a value out of a
condition, and its contract here is stricter than the usual convention — see
[the MRAP controller](#mrap-controller).

`status.activated` is unchanged and becomes purely informational: it is the
reverse index, useful to a human, and nothing keys off it.

### `ProviderRevision`

`RuntimeActive` becomes three-state — active, awaiting first activation,
deliberately down — because the scale-to-zero latch is now also a handshake.
A new `Deactivating` condition reports the removal window's progress, what it
is waiting on, and which MRAP is blocking it. Its reasons are `AwaitingPolicy`,
`Stopping`, `RemovingCRDs` and `Restarting`.

## User-facing workflow

A platform team upgraded to v2 with the default `*` MRAP and now wants only EC2
and S3 types from the AWS provider.

**1. Narrow the policy.** An ordinary edit, committed to Git and applied by
whatever applies it:

```yaml
apiVersion: apiextensions.crossplane.io/v1alpha1
kind: ManagedResourceActivationPolicy
metadata:
  name: default
spec:
  activate:
  - "*.ec2.aws.upbound.io"
  - "*.s3.aws.upbound.io"
```

Nothing else is required. There is no approval to grant, no annotation to set,
and no second resource to edit in a particular order — which matters when the
MRAP is managed by GitOps and a follow-up action would have to be encoded as a
dependency between resources.

**2. Watch the types queue up.**

```
$ kubectl get managedresourcedefinitions
NAME                              ESTABLISHED   AGE
instances.ec2.aws.upbound.io      True          30d
buckets.s3.aws.upbound.io         True          30d
clusters.rds.aws.upbound.io       False         30d
databases.rds.aws.upbound.io      False         30d

$ kubectl describe mrd clusters.rds.aws.upbound.io
Conditions:
  Type         Status  Reason          Message
  Established  False   PendingRemoval  No activators; 0 instances; waiting for
                                       ManagedResourceActivationPolicy/default
                                       to finish reconciling generation 4
```

**3. Drain anything still in use.** A type with live instances stays in
`PendingRemoval` indefinitely and is reported as such:

```
  Established  False   PendingRemoval  No activators; 3 instances must be
                                       deleted before this type is removed
```

It does not block the removal of its siblings. The rest of the batch is
removed, and this one follows on its own once the last instance is gone. The
user does not need to come back and trigger anything — but each type that
drains after the batch has gone through costs its own provider restart. A team
that cares about availability drains first and narrows the policy second, which
keeps the whole change to a single restart. See *Stragglers* under
[the package manager](#package-manager).

**4. One restart, then the CRDs are gone.** Once the MRAP has fully reconciled,
the provider is scaled to zero, the empty types' CRDs are deleted, and it comes
back up. The provider is unavailable for the length of one pod cycle, once, no
matter how many types were removed.

```
$ kubectl get providerrevision aws-provider-abc123
NAME                  HEALTHY   REVISION   STATE    AGE
aws-provider-abc123   True      1          Active   30d

$ kubectl describe providerrevision aws-provider-abc123
Conditions:
  Type          Status  Reason        Message
  Deactivating  True    RemovingCRDs  Runtime stopped; deleting 47 CRDs
```

**5. Undo, at any point before the window.** Restoring the pattern re-creates
the activator entry, the MRD leaves `PendingRemoval`, and there is nothing to
unwind. After the CRDs are deleted, re-adding the pattern is an ordinary
activation: the CRD is re-applied and the provider's `customresourcesgate`
opens for it again.
Only the instances are unrecoverable, and they had to be zero to get here.

**What the user does not get.** A deactivation is not instant. It waits for the
MRAP to be fully reconciled, and it waits for the type to be empty. If an MRAP
is unhealthy or paused, deactivations attributed to it stall, and the reason is
reported on the `ProviderRevision`. This is deliberate: acting on a policy
change that is only half-applied is how a bulk edit turns into a bulk deletion
of the wrong things.

## Controller operation

Three controllers participate, and each writes only what it is authoritative
about. The MRAP controller writes MRD spec, which is the boundary that already
exists today (`reconciler.go:128` patches `mrd.Spec.State`). The MRD controller
writes MRD status and owns the CRD. The package manager owns the Deployment and
decides when the window opens.

### MRAP controller

`internal/controller/apiextensions/activationpolicy`. Keyed on one MRAP, as
today. It gains no global view and reads no other MRAP.

For generation `N` of MRAP `X`:

1. List MRDs, as today.
2. For each, server-side apply `spec.activators` with field manager `mrap/X`,
   including the entry `{name: X, kind: ManagedResourceActivationPolicy,
   generation: N}` if the MRD matches a pattern, and omitting it if not. The
   API server adds, updates or removes the entry according to ownership. This
   replaces the `client.MergeFrom` patch of `spec.state` at `reconciler.go:128`;
   per-entry ownership requires SSA.
3. If the MRD matched and its `spec.state` is not already `PolicyManaged`,
   latch it there — **after** the apply in step 2, never before, and not as
   part of it. See [Migration](#migration).
4. **Only after every apply and latch has succeeded**, set
   `status.observedGeneration = N` along with `status.activated` and the health
   condition.

Step 4 is the contract the whole batching mechanism rests on, and it is
stricter than the usual meaning of `observedGeneration`. Today the reconciler
accumulates failures in `errs`, marks `Unhealthy` (`reconciler.go:139`) and
then updates status unconditionally (`reconciler.go:147`). Left that way, a
partial failure would advance `observedGeneration` and open the removal window
on an incomplete batch — the exact failure this design exists to prevent.
`observedGeneration` must advance only when `errs == nil`, so that
`generation == observedGeneration` means precisely: *every activator write
implied by this generation has landed in etcd.*

**Deletion.** The MRAP needs a finalizer it does not have today. The current
deletion path only marks `Terminating` (`reconciler.go:86`). It must instead
remove its entry from every MRD — an apply with its field manager omitting the
entry — and only then remove its own finalizer. Deleting every MRAP in a
cluster therefore deactivates every empty type, which is the correct reading of
the intent and is worth a release note.

### MRD controller

`internal/controller/apiextensions/managed`. Keyed on one MRD. It is the CRD's
field manager and owns everything Crossplane runs for the type.

On each reconcile:

* **Activators non-empty.** Apply the CRD as today, mirror `spec.activators`
  into `status.activators`, and report `Established=True`. The mirror is
  refreshed on every reconcile while the set is populated; this is what leaves
  a usable record behind when it empties.
* **Activators empty on a `PolicyManaged` MRD, CRD exists.** Do nothing
  destructive. Stop re-applying the CRD, mark
  `Established=False/PendingRemoval`, and record the live instance count in the
  message. Leave the CRD, the protection controller, the watch and the provider
  entirely alone — the protection controller is the oracle for whether the type
  is in use, and tearing it down here would destroy the information the next
  step needs.
* **Released for deletion.** When the package manager sets the per-MRD release
  (a field on the MRD spec, written by the package manager, or an annotation —
  either way a distinct field from `activators`, which belongs to the MRAPs),
  and the runtime is confirmed down, tear down the type: stop the protection
  controller and its watch, delete the `ClusterUsage`, drop RBAC, delete the
  CRD. Then report `Established=False/Inactive`.

The order is load-bearing in both directions: tearing down earlier loses the
emptiness oracle, deleting the CRD earlier wedges it in `Terminating` behind
provider finalizers on the MRs. Drain, then stop, then delete — never stop,
then drain.

An MRD with `state: Active` never enters `PendingRemoval`, whatever its
activator set does; it is pinned, and the MRD controller keeps applying its
CRD.

`PendingRemoval` is a report, not a trigger. The package manager does not key
off it; see below for why.

### Package manager

`internal/controller/pkg/revision` and `internal/controller/pkg/runtime`,
keyed on one `ProviderRevision`. It already lists the MRDs its revision
controls, via the controller owner reference every MRD carries, in order to
decide about scaling to zero.

**Determining that the batch is complete.** Per reconcile of revision `R`:

1. List the MRDs whose controller owner reference is `R`.
2. **Removal set**: those whose `spec.state` is `PolicyManaged`, whose
   `spec.activators` is empty, whose CRD still exists, and which are believed
   to have no instances (from the protection controller's `ClusterUsage`, or a
   list).
3. If the removal set is empty, stay `Idle`.
4. **Implicated MRAPs**: the union of the names in those MRDs'
   `status.activators`.
5. Point-read each one. It is settled iff
   `metadata.generation == status.observedGeneration`. An MRAP that no longer
   exists is settled — its finalizer removed its entries before it went.
6. If every implicated MRAP is settled, open the removal window. Otherwise
   report what is being waited on and requeue.

Two details in that sequence matter.

**Step 2 reads `spec`, not the `PendingRemoval` condition.** The condition is
written by the MRD controller, so gating on it would make the completeness of
the batch depend on *that* controller having caught up — a lag the MRAP's
`observedGeneration` says nothing about. If an MRAP releases 200 MRDs and the
MRD controller has processed 50, a condition-based test sees 50 pending, sees
the MRAP settled, and removes a quarter of the batch. `spec.activators` is
the authoritative record and is already in the informer cache, so reading it
directly makes MRD-controller lag irrelevant to the decision. The MRD
controller remains the actor that tears the type down; it stops being a
dependency of the decision to open the window.

**Step 4 relies on the mirror being stale.** `status.activators` is written
while the spec list is non-empty, so for an MRD that just emptied it holds the
previous holders — exactly the question being asked. It does not require the
MRD controller to have observed the emptying, only to have been current before
it. An MRD in the removal set with no mirror at all is unattributable; hold the
window shut on it and surface it rather than guessing.

**The removal window.** Once it opens:

1. Mark the revision `Deactivating`, scale the Deployment to zero, and wait
   until it reports no available replicas.
2. Re-check each MRD in the removal set against a **live, uncached** read:
   `spec.state` still `PolicyManaged`, `spec.activators` still empty, and still
   no instances. Anything that acquired any of the three drops out of the
   window and back to `PendingRemoval`. This is also what makes an abort safe
   after the window has been entered.
3. Write the per-MRD release on each survivor and wait for the MRD controller
   to report the CRD gone.
4. Scale back up. The provider's `customresourcesgate` opens only for the types
   that remain.

**What stopping the runtime does and does not buy.** It does not make creation
impossible. The provider is not the only writer of its own MRs — a user, a
composition, or any other client with access can create one at any point,
including between the live read in step 2 and the `DELETE` of the CRD.
Kubernetes offers no "delete this CRD only if it has no instances" and no
moment at which nothing can create one, so this race cannot be closed, only
made harmless.

What the stop guarantees is that an instance created in that gap is never
reconciled. Nothing claims it, nothing puts a provider finalizer on it, and no
external infrastructure comes to exist behind it. Deleting it along with the
CRD destroys a Kubernetes object that never became real, in a cluster whose
policy says this type should not exist and whose operator asked for it to be
removed. That is the acceptable loss. The outcomes the stop actually prevents —
orphaning live infrastructure, and wedging the CRD in `Terminating` behind a
finalizer no running reconciler will ever remove — are the ones that are not
recoverable by re-applying a pattern.

The live re-check still earns its place, because the usual case is not a race
but a lag: minutes or hours can pass between an MRD emptying and its window
opening, and a composition that still renders the type, or a user who did not
finish draining, will have created instances in that time. The read catches
that and narrows the genuine race to the interval between the read and the
delete. [Unserved versions](#open-questions) would narrow it further.

**Stragglers.** A type that still has instances when its last activator goes
away does not travel with the batch. It sits in `PendingRemoval` while the
batch's window opens, runs and closes around it, and becomes eligible only when
its instance count reaches zero — which may be minutes or months later, and is
driven by whoever is deleting the instances rather than by anything Crossplane
controls.

Such a type is handled individually. The removal set is recomputed from scratch
on each reconcile of the revision, so a drained straggler simply appears in it,
finds its implicated MRAPs long since settled, and opens a window of its own:
another scale to zero, one CRD deleted, another scale up. **The cost is one
additional provider restart per straggler**, or per group of stragglers that
happen to be empty at the same reconcile — draining several types together
still batches them, because the set is a snapshot of what is eligible now, not
a queue built when the policy changed.

This is accepted rather than solved. The alternative is to hold the batch open
until every straggler drains, which turns a bounded number of restarts into an
unbounded delay before any CRD is removed, and makes one undrained type block
every other type on the provider — the thing the batch was meant to avoid. A
debounce or coalescing window would only move the trade-off around; see [Open
questions](#open-questions).

The remedy is procedural and belongs in the documentation: **delete the
instances first, then narrow the policy.** A user who does that has no
stragglers and pays exactly one restart. A user who does not still converges on
the right end state, and pays a restart each time a type drains. Neither path
loses data or leaves a type half-removed; the difference is availability, and
it is visible in advance — the `PendingRemoval` messages name every type that
is going to straggle, before any window opens.

`safe-stop` ([Future work](#future-work-safe-stop)) removes the cost entirely
rather than mitigating it: with no scale-to-zero in the window, a straggler's
removal is just a CRD deletion, and stragglers stop being a category worth
naming.

**Waking up.** When an MRAP settles, nothing about any MRD changes — its
activator writes all landed *before* `observedGeneration` advanced, so those
MRD events have already fired and found the window shut. The revision
controller must therefore be woken by the MRAP status update itself: watch
MRAPs and enqueue every `ProviderRevision`. An MRAP carries no owner reference
to a revision and its patterns are not evaluated here, so the handler cannot
narrow the fan-out — but revisions number in the handful, so enqueuing all of
them is cheap, and it beats backoff-polling.

This is not a new direction of dependency. `pkg/runtime` already watches MRDs
and enqueues the owning revision (`pkg/runtime/reconciler.go:192`), which is
what lets a safe-start provider scale up the moment its first MRD activates.
The MRAP watch is another watch of the same shape; the package manager still
never evaluates a pattern or reads policy state, only `observedGeneration`.

What does have to change is the existing MRD watch, which is deliberately
one-directional today. `mrdActivated()` (`pkg/runtime/watch.go:88`) passes
create events only for active MRDs and update events only on the
inactive-to-active edge, to keep status churn out of the queue, and
`EnqueueProviderRevisionsForMRDs` returns nothing for an MRD that is not active
(`pkg/runtime/watch.go:64`) — precisely the MRDs the removal set is made of.
Both need widening to carry the reverse edges: an MRD whose activator set just
emptied, and a drained straggler whose instance count reached zero. That
straggler edge is what lets it be removed on its own, without the user
returning to trigger anything.

## State and ownership

| State | Object | Written by | Meaning |
| --- | --- | --- | --- |
| `spec.activators[i]` | MRD | that MRAP, via SSA field manager `mrap/<name>` | this policy wants this type |
| `spec.state` | MRD | user, or MRAP controller latching `PolicyManaged` | which activation regime this type is under |
| `status.activators` | MRD | MRD controller | who last held this type |
| `Established` | MRD | MRD controller | observed state and phase |
| release field | MRD | package manager | cleared to delete the CRD |
| `status.observedGeneration` | MRAP | MRAP controller | every write for this generation landed |
| `status.activated` | MRAP | MRAP controller | informational reverse index |
| `RuntimeActive` | ProviderRevision | package manager | three-state runtime latch |
| `Deactivating` | ProviderRevision | package manager | window phase and what it waits on |
| replica count | Deployment | package manager | the stop itself |
| the CRD | — | MRD controller | the terminal fact |

No field has two controllers writing it. `spec.state` is the one field a user
and a controller both touch, and the controller's only write is a one-way latch
to `PolicyManaged` that the CEL rule makes idempotent. Nothing that a
deactivation depends on lives in process memory, so an interruption at any
point resumes by re-reading these.

## Migration

Existing clusters have MRDs with `spec.state: Active`, written by the old MRAP
controller, and no activators. Read literally they are all pinned and become
permanently immune to deactivation — the feature would ship and do nothing for
the users who reported the bug.

The MRAP controller latches `spec.state: PolicyManaged` on every MRD it
activates, whatever the value it finds. An MRD the old controller set to
`Active` is matched by the same MRAP that set it, so it moves to
`PolicyManaged` on the first reconcile after the upgrade and becomes eligible.
An MRD that no MRAP matches keeps `Active` and stays pinned, which is the
correct reading: nothing in the cluster's policy asks for it, so nothing but a
human can have turned it on. The distinction needs no global read and no "have
all MRAPs reconciled yet" test, and the residue errs toward staying active.

**Order.** The activator entry is applied first, the state latched second. The
reverse order leaves a window in which the MRD is `PolicyManaged` with an empty
activator set — exactly the shape the package manager reads as *remove this* —
and nothing would hold the removal window shut over it, because an MRAP
re-reconciling without a spec change has `generation == observedGeneration`
throughout and reads as settled the whole time.

**The latch is not part of the apply.** `spec.state` is a single field that
several MRAPs would co-own under server-side apply. When the last of them
dropped its pattern, the API server would remove the field, it would default
back to `Inactive`, and the write would be a `PolicyManaged -> Inactive`
transition that CEL refuses — wedging the apply, and, were it to succeed,
unmarking precisely the MRDs meant to be removed. So `spec.activators` is the
only thing applied per-MRAP; `spec.state` is latched with a separate patch
under a shared field manager and never retracted.

`+kubebuilder:default=Inactive` is unchanged, and a never-activated MRD still
reads `Inactive`.

`spec.state` carries forward into `v1beta1` unchanged, with the same three
values, so there is no conversion to write and no synthetic activator entry to
manufacture for a pinned type.

## Failure modes

* **An MRAP that never settles** — paused, or `Unhealthy` because it cannot
  list MRDs — holds the removal window shut on every revision its MRDs touch.
  This is the correct behavior, since acting on demonstrably incomplete intent
  is the failure being prevented, but it must be reported in the revision's
  `Deactivating` condition naming the MRAP, or it is an unexplainable stall.
* **A leaked activator entry**, from an MRAP force-deleted past its finalizer,
  holds a type open forever. Entries are derivable from MRAP patterns, so this
  is collectable, and until it is collected the type stays active.
* **An unattributable MRD** in the removal set — no mirror, because the MRD
  controller never reconciled it while claimed — holds the window shut.
* **A second MRAP edit** arriving after the first has settled produces a second
  window. Under an assumption of infrequent policy changes this is correct:
  they are genuinely two changes.
* **A provider brought up mid-window** by a rolling restart, runtime config
  change or competing reconcile would reopen a watch on a type being deleted.
  The `Deactivating` phase has to suppress the normal scale-up reconcile, which
  is why the one-way `RuntimeActive` latch becomes three-state.

## Open questions

* **Deletion protection is feature-gated.** The protection controller's
  `ClusterUsage` is the natural oracle for "is this type in use". Deactivation
  either depends on that gate, duplicates the watch, or the feature is
  promoted.
* **Where the per-MRD release lives.** A spec field on the MRD written by the
  package manager is explicit but adds a third writer to MRD spec; an
  annotation is lighter and less discoverable.
* **Unserved versions as a belt-and-braces measure** before deletion, to close
  the create-during-window race even further. Whether the API server accepts a
  CRD with no served version needs confirming.
* **Whether `kind: Manual` entries are worth having**, given that
  `state: Active` already pins a type. They are not redundant — pinning via
  `Active` is irreversible, while a manual activator entry can be withdrawn
  later — but they are a second way to say a similar thing.
* **RBAC teardown ordering** relative to CRD deletion, given that the provider
  is down and cannot be the one to notice.
* **Whether stragglers deserve a coalescing delay.** Holding a drained
  straggler for a short period before opening its window would batch a cluster
  that is draining several types by hand into fewer restarts, at the cost of a
  tunable nobody can set correctly and a delay on the common case of a single
  straggler. The alternative — an operator-triggered "remove everything that is
  ready now" — reintroduces the manual step this design set out to avoid.

## Future work: `safe-stop`

The end state is providers that can stop an MR controller in place: the
stoppable controller engine Crossplane uses for XRs moves into
crossplane-runtime, the `Gate` interface grows a shutdown half, and providers
advertising a `safe-stop` capability put a finalizer on the CRDs they
reconcile. Crossplane deletes the CRD, the provider sees the deletion
timestamp, drains and releases its informer, and removes its finalizer. No
restart, no availability gap, and an ordering guarantee rather than a timing
argument. The same handshake is wanted today for provider uninstall, for
upgrades that drop a type, and for per-type pause.

This design does not foreclose it and composes with it cleanly. The activator
set, the `observedGeneration` handshake and the per-MRD release are unchanged; a
`safe-stop` provider simply skips the scale-to-zero and scale-up steps of the
window and has its types removed in place with the runtime running. The restart
degrades into a fallback for providers that cannot stop cleanly — which will be
needed regardless, since the benefit of `safe-stop` arrives only as providers
rebuild and re-release.

[issue-6984]: https://github.com/crossplane/crossplane/issues/6984
[issue-6803]: https://github.com/crossplane/crossplane/issues/6803#issuecomment-3975763586
[pr-7205]: https://github.com/crossplane/crossplane/pull/7205#pullrequestreview-3932449446
[gate]: https://github.com/crossplane/crossplane-runtime/blob/main/pkg/reconciler/customresourcesgate/reconciler.go
[engine]: https://github.com/crossplane/crossplane/blob/main/internal/engine/engine.go
