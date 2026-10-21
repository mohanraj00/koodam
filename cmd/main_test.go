/*
Copyright 2026 Mohan Raj.

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

package main

import "testing"

// TestWorkspaceImageFor checks the tag rule of the default workspace image.
func TestWorkspaceImageFor(t *testing.T) {
	const repo, latest = "ghcr.io/mohanraj00/koodam-workspace:", "latest"
	for v, tag := range map[string]string{
		"v0.2.0":                               "0.2.0",
		"v0.2.0-rc.1":                          "0.2.0-rc.1",
		"v0.2.0+dirty":                         latest,
		"v0.0.0-20261021000000-abcdef123456":   latest,
		"v0.2.1-0.20261021000000-abcdef123456": latest,
		"(devel)":                              latest,
		"":                                     latest,
		"0.2.0":                                latest,
	} {
		if got := workspaceImageFor(v); got != repo+tag {
			t.Errorf("workspaceImageFor(%q) = %q, want %q", v, got, repo+tag)
		}
	}
}
