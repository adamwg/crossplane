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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// ManagedResourceDefinitionSpec specifies the desired state of the resource definition.
type ManagedResourceDefinitionSpec struct {
	// Inline the minor fork of upstream's CustomResourceDefinitionSpec.
	CustomResourceDefinitionSpec `json:",inline"`

	// ConnectionDetails is an array of connection detail keys and descriptions.
	ConnectionDetails []ConnectionDetail `json:"connectionDetails,omitempty"`

	// State toggles whether the underlying CRD is created or not.
	//
	// Active pins the type on: its CRD is created and never removed by
	// policy. PolicyManaged hands the decision to spec.activators; the CRD is
	// created while at least one activator holds the type, and removed once
	// the last one lets go.
	// +kubebuilder:validation:Enum=Active;Inactive;PolicyManaged
	// +kubebuilder:default=Inactive
	// +kubebuilder:validation:XValidation:rule="self == oldSelf || oldSelf == 'Inactive' || self == 'PolicyManaged'",message="state cannot be changed once it becomes Active, except to PolicyManaged"
	State ManagedResourceDefinitionState `json:"state,omitempty"`

	// Activators names the objects that currently want this type to exist.
	// Each ManagedResourceActivationPolicy server-side applies only its own
	// entry, under its own field manager, so dropping a pattern removes the
	// entry without any participant computing a union.
	//
	// It is only consulted when state is PolicyManaged.
	// +optional
	// +listType=map
	// +listMapKey=name
	Activators []Activator `json:"activators,omitempty"`
}

// An Activator is an object that wants a ManagedResourceDefinition's type to
// exist.
type Activator struct {
	// Name of the activating object.
	Name string `json:"name"`

	// Kind of the activating object. Entries written by the activation policy
	// controller use ManagedResourceActivationPolicy; anything else was
	// written by hand.
	// +optional
	Kind string `json:"kind,omitempty"`

	// Generation of the activating object when it wrote this entry. It is
	// diagnostic only - nothing waits on it.
	// +optional
	Generation int64 `json:"generation,omitempty"`
}

// ManagedResourceDefinitionState is the state of the resource definition.
type ManagedResourceDefinitionState string

const (
	// ManagedResourceDefinitionActive is an active resource definition.
	ManagedResourceDefinitionActive ManagedResourceDefinitionState = "Active"

	// ManagedResourceDefinitionInactive is an inactive resource definition.
	ManagedResourceDefinitionInactive ManagedResourceDefinitionState = "Inactive"

	// ManagedResourceDefinitionPolicyManaged is a resource definition whose
	// activation is decided by its activator set.
	ManagedResourceDefinitionPolicyManaged ManagedResourceDefinitionState = "PolicyManaged"
)

// IsActive returns if this ManagedResourceDefinitionState is "Active".
func (s ManagedResourceDefinitionState) IsActive() bool {
	return s == ManagedResourceDefinitionActive
}

// IsPolicyManaged returns if this ManagedResourceDefinitionState is
// "PolicyManaged".
func (s ManagedResourceDefinitionState) IsPolicyManaged() bool {
	return s == ManagedResourceDefinitionPolicyManaged
}

// ConnectionDetail holds keys and descriptions of connection secrets.
type ConnectionDetail struct {
	// Name of the key.
	Name string `json:"name"`
	// Description of how the key is used.
	Description string `json:"description"`
}

// ManagedResourceDefinitionStatus shows the observed state of the resource definition.
type ManagedResourceDefinitionStatus struct {
	xpv2.ConditionedStatus `json:",inline"`

	// Activators mirrors the last observed non-empty spec.activators. When the
	// spec list empties this retains the previous holders, which is how a
	// pending removal says who released it.
	// +optional
	// +listType=map
	// +listMapKey=name
	Activators []Activator `json:"activators,omitempty"`
}

// +kubebuilder:object:root=true
// +genclient
// +genclient:nonNamespaced

// A ManagedResourceDefinition defines the schema for a new custom Kubernetes API.
//
// +kubebuilder:printcolumn:name="STATE",type="string",JSONPath=".spec.state"
// +kubebuilder:printcolumn:name="ESTABLISHED",type="string",JSONPath=".status.conditions[?(@.type=='Established')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=crossplane,shortName=mrd;mrds
type ManagedResourceDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedResourceDefinitionSpec   `json:"spec,omitempty"`
	Status ManagedResourceDefinitionStatus `json:"status,omitempty"`
}

// GetCondition of this ManagedResourceDefinition.
func (p *ManagedResourceDefinition) GetCondition(ct xpv2.ConditionType) xpv2.Condition {
	return p.Status.GetCondition(ct)
}

// SetConditions of this ManagedResourceDefinition.
func (p *ManagedResourceDefinition) SetConditions(c ...xpv2.Condition) {
	p.Status.SetConditions(c...)
}

// IsActive returns whether this ManagedResourceDefinition's type should exist:
// either it is pinned by hand, or at least one activator still wants it.
func (p *ManagedResourceDefinition) IsActive() bool {
	return p.Spec.State.IsActive() || len(p.Spec.Activators) > 0
}

// IsPendingRemoval returns whether this ManagedResourceDefinition is managed by
// policy and no longer has any activator, making it a candidate for removal.
func (p *ManagedResourceDefinition) IsPendingRemoval() bool {
	return p.Spec.State.IsPolicyManaged() && len(p.Spec.Activators) == 0
}

// +kubebuilder:object:root=true

// ManagedResourceDefinitionList contains a list of ManagedResourceDefinitions.
type ManagedResourceDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ManagedResourceDefinition `json:"items"`
}

// AnnotationKeyReleasedForRemoval is set on a ManagedResourceDefinition by the
// package manager, once the runtime that reconciles the type is confirmed
// down, to release the MRD controller to tear the type down and delete its
// CRD. It is deliberately a distinct field from spec.activators, which belongs
// to the activation policies.
//
// TODO(POC): the design leaves open whether this should be a spec field
// written by the package manager instead.
const AnnotationKeyReleasedForRemoval = "apiextensions.crossplane.io/released-for-removal"

// IsReleasedForRemoval returns whether the package manager has released this
// ManagedResourceDefinition for teardown.
func (p *ManagedResourceDefinition) IsReleasedForRemoval() bool {
	return p.GetAnnotations()[AnnotationKeyReleasedForRemoval] == "true"
}
