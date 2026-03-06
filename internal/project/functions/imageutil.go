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

package functions

import (
	"strings"

	"github.com/crossplane/crossplane/apis/v2/dev/v1alpha1"
)

// RewriteImage applies imageConfig rules to rewrite the given image string.
func RewriteImage(image string, configs []v1alpha1.ImageConfig) string {
	var bestMatchPrefix, replacementPrefix string

	for _, config := range configs {
		for _, match := range config.MatchImages {
			if strings.HasPrefix(image, match.Prefix) {
				if len(match.Prefix) > len(bestMatchPrefix) {
					bestMatchPrefix = match.Prefix
					replacementPrefix = config.RewriteImage.Prefix
				}
			}
		}
	}

	return replacementPrefix + strings.TrimPrefix(image, bestMatchPrefix)
}
