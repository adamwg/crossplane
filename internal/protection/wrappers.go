//go:build !goverter

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

package protection

import (
	xpv1 "github.com/crossplane/crossplane/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	legacy "github.com/crossplane/crossplane/v2/apis/apiextensions/v1beta1"
	"github.com/crossplane/crossplane/v2/apis/protection/v1beta1"
)

// UsageWrapper wraps a Usage to implement the internal interface.
//
// +kubebuilder:object:root=true
type UsageWrapper struct {
	v1beta1.Usage
}

func (u *UsageWrapper) Unwrap() resource.Object {
	return &u.Usage
}

// GetUserOf gets the resource this Usage indicates a use of.
func (u *UsageWrapper) GetUserOf() Resource {
	conv := GeneratedNamespacedResourceConverter{}
	return conv.ToInternal(u.Spec.Of)
}

// SetUserOf sets the resource this Usage indicates a use of.
func (u *UsageWrapper) SetUserOf(r Resource) {
	conv := GeneratedNamespacedResourceConverter{}
	u.Spec.Of = conv.FromInternal(r)
}

// GetUsedBy gets the resource this Usage indicates a use by.
func (u *UsageWrapper) GetUsedBy() *Resource {
	if u.Spec.By == nil {
		return nil
	}

	conv := GeneratedResourceConverter{}
	out := conv.ToInternal(*u.Spec.By)

	return &out
}

// SetUsedBy sets the resource this Usage indicates a use by.
func (u *UsageWrapper) SetUsedBy(r *Resource) {
	if r == nil {
		u.Spec.By = nil
		return
	}

	conv := GeneratedResourceConverter{}
	out := conv.FromInternal(*r)
	u.Spec.By = &out
}

// GetReason gets the reason this Usage exists.
func (u *UsageWrapper) GetReason() *string {
	return u.Spec.Reason
}

// SetReason sets the reason this Usage exists.
func (u *UsageWrapper) SetReason(reason *string) {
	u.Spec.Reason = reason
}

// GetReplayDeletion gets a boolean that indicates whether deletion of the used
// resource will be replayed when this Usage is deleted.
func (u *UsageWrapper) GetReplayDeletion() *bool {
	return u.Spec.ReplayDeletion
}

// SetReplayDeletion specifies whether deletion of the used resource will be
// replayed when this Usage is deleted.
func (u *UsageWrapper) SetReplayDeletion(replay *bool) {
	u.Spec.ReplayDeletion = replay
}

// GetCondition of this Usage.
func (u *UsageWrapper) GetCondition(ct xpv1.ConditionType) xpv1.Condition {
	return u.Status.GetCondition(ct)
}

// SetConditions of this Usage.
func (u *UsageWrapper) SetConditions(c ...xpv1.Condition) {
	u.Status.SetConditions(c...)
}

// ClusterUsageWrapper wraps a ClusterUsage to implement the internal interface.
//
// +kubebuilder:object:root=true
type ClusterUsageWrapper struct {
	v1beta1.ClusterUsage
}

func (u *ClusterUsageWrapper) Unwrap() resource.Object {
	return &u.ClusterUsage
}

// GetUserOf gets the resource this ClusterUsage indicates a use of.
func (u *ClusterUsageWrapper) GetUserOf() Resource {
	conv := GeneratedResourceConverter{}
	return conv.ToInternal(u.Spec.Of)
}

// SetUserOf sets the resource this ClusterUsage indicates a use of.
func (u *ClusterUsageWrapper) SetUserOf(r Resource) {
	conv := GeneratedResourceConverter{}
	u.Spec.Of = conv.FromInternal(r)
}

// GetUsedBy gets the resource this ClusterUsage indicates a use by.
func (u *ClusterUsageWrapper) GetUsedBy() *Resource {
	if u.Spec.By == nil {
		return nil
	}

	conv := GeneratedResourceConverter{}
	out := conv.ToInternal(*u.Spec.By)

	return &out
}

// SetUsedBy sets the resource this ClusterUsage indicates a use by.
func (u *ClusterUsageWrapper) SetUsedBy(r *Resource) {
	if r == nil {
		u.Spec.By = nil
		return
	}

	conv := GeneratedResourceConverter{}
	out := conv.FromInternal(*r)
	u.Spec.By = &out
}

// GetReason gets the reason this ClusterUsage exists.
func (u *ClusterUsageWrapper) GetReason() *string {
	return u.Spec.Reason
}

// SetReason sets the reason this ClusterUsage exists.
func (u *ClusterUsageWrapper) SetReason(reason *string) {
	u.Spec.Reason = reason
}

// GetReplayDeletion gets a boolean that indicates whether deletion of the used
// resource will be replayed when this ClusterUsage is deleted.
func (u *ClusterUsageWrapper) GetReplayDeletion() *bool {
	return u.Spec.ReplayDeletion
}

// SetReplayDeletion specifies whether deletion of the used resource will be
// replayed when this ClusterUsage is deleted.
func (u *ClusterUsageWrapper) SetReplayDeletion(replay *bool) {
	u.Spec.ReplayDeletion = replay
}

// GetCondition of this ClusterUsage.
func (u *ClusterUsageWrapper) GetCondition(ct xpv1.ConditionType) xpv1.Condition {
	return u.Status.GetCondition(ct)
}

// SetConditions of this ClusterUsage.
func (u *ClusterUsageWrapper) SetConditions(c ...xpv1.Condition) {
	u.Status.SetConditions(c...)
}

// LegacyUsageWrapper wraps a legacy Usage to implement the internal interface.
//
// +kubebuilder:object:root=true
type LegacyUsageWrapper struct {
	legacy.Usage
}

func (u *LegacyUsageWrapper) Unwrap() resource.Object {
	return &u.Usage
}

// GetUserOf gets the resource this Usage indicates a use of.
func (u *LegacyUsageWrapper) GetUserOf() Resource {
	conv := GeneratedLegacyResourceConverter{}
	return conv.ToInternal(u.Spec.Of)
}

// SetUserOf sets the resource this Usage indicates a use of.
func (u *LegacyUsageWrapper) SetUserOf(r Resource) {
	conv := GeneratedLegacyResourceConverter{}
	u.Spec.Of = conv.FromInternal(r)
}

// GetUsedBy gets the resource this Usage indicates a use by.
func (u *LegacyUsageWrapper) GetUsedBy() *Resource {
	if u.Spec.By == nil {
		return nil
	}

	conv := GeneratedLegacyResourceConverter{}
	out := conv.ToInternal(*u.Spec.By)

	return &out
}

// SetUsedBy sets the resource this Usage indicates a use by.
func (u *LegacyUsageWrapper) SetUsedBy(r *Resource) {
	if r == nil {
		u.Spec.By = nil
		return
	}

	conv := GeneratedLegacyResourceConverter{}
	out := conv.FromInternal(*r)
	u.Spec.By = &out
}

// GetReason gets the reason this Usage exists.
func (u *LegacyUsageWrapper) GetReason() *string {
	return u.Spec.Reason
}

// SetReason sets the reason this Usage exists.
func (u *LegacyUsageWrapper) SetReason(reason *string) {
	u.Spec.Reason = reason
}

// GetReplayDeletion gets a boolean that indicates whether deletion of the used
// resource will be replayed when this Usage is deleted.
func (u *LegacyUsageWrapper) GetReplayDeletion() *bool {
	return u.Spec.ReplayDeletion
}

// SetReplayDeletion specifies whether deletion of the used resource will be
// replayed when this Usage is deleted.
func (u *LegacyUsageWrapper) SetReplayDeletion(replay *bool) {
	u.Spec.ReplayDeletion = replay
}

// GetCondition of this Usage.
func (u *LegacyUsageWrapper) GetCondition(ct xpv1.ConditionType) xpv1.Condition {
	return u.Status.GetCondition(ct)
}

// SetConditions of this Usage.
func (u *LegacyUsageWrapper) SetConditions(c ...xpv1.Condition) {
	u.Status.SetConditions(c...)
}
