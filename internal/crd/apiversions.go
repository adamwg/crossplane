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

package crd

// KnownAPIVersions is a list of known Kubernetes API versions.
var KnownAPIVersions = []string{
	"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9", "v10",
	"v1alpha1", "v1alpha2", "v1alpha3", "v1alpha4", "v1alpha5",
	"v2alpha1", "v2alpha2", "v2alpha3", "v2alpha4", "v2alpha5",
	"v3alpha1", "v3alpha2", "v3alpha3", "v3alpha4", "v3alpha5",
	"v1beta1", "v1beta2", "v1beta3", "v1beta4", "v1beta5",
	"v2beta1", "v2beta2", "v2beta3", "v2beta4", "v2beta5",
	"v3beta1", "v3beta2", "v3beta3", "v3beta4", "v3beta5",
}

// IsKnownAPIVersion checks if a segment is a known API version.
func IsKnownAPIVersion(segment string) bool {
	for _, v := range KnownAPIVersions {
		if v == segment {
			return true
		}
	}
	return false
}
