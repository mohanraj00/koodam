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

package chart

import (
	"slices"
	"strings"
	"testing"
)

// nookkuImageArgs returns each value of the flag --nookku-image.
func nookkuImageArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--nookku-image"); ok {
			out = append(out, v)
		}
	}
	return out
}

// TestNookkuImage checks the chart value nookku.image (#83). An empty value gives no flag. A value
// with a digest gives the flag --nookku-image.
func TestNookkuImage(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	cases := map[string]struct {
		values string
		set    []string
		want   []string
	}{
		"default":     {},
		"empty":       {set: []string{"nookku.image="}},
		"null nookku": {values: "nookku: null\n"},
		"digest": {
			set:  []string{"nookku.image=ghcr.io/mohanraj00/koodam-nookku" + digest},
			want: []string{"=ghcr.io/mohanraj00/koodam-nookku" + digest},
		},
		"tag and digest": {
			set:  []string{"nookku.image=registry.example/n:0.5.0" + digest},
			want: []string{"=registry.example/n:0.5.0" + digest},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := helmTemplate(t, c.values, c.set...)
			if err != nil {
				t.Fatalf("helm template failed: %v\n%s", err, out)
			}
			if got := nookkuImageArgs(managerArgs(t, out)); !slices.Equal(got, c.want) {
				t.Errorf("the --nookku-image flags are %q, want %q", got, c.want)
			}
		})
	}
}

// TestNookkuImageRefusesNoDigest checks that the chart refuses a nookku.image with no valid digest.
func TestNookkuImageRefusesNoDigest(t *testing.T) {
	for name, image := range map[string]string{
		"tag":          "ghcr.io/mohanraj00/koodam-nookku:0.5.0",
		"short digest": "ghcr.io/mohanraj00/koodam-nookku@sha256:" + strings.Repeat("a", 63),
		"upper case":   "ghcr.io/mohanraj00/koodam-nookku@sha256:" + strings.Repeat("A", 64),
		"no name":      "@sha256:" + strings.Repeat("a", 64),
		"space":        "ghcr.io/a b@sha256:" + strings.Repeat("a", 64),
		"two digests":  "ghcr.io/a@sha256:" + strings.Repeat("a", 64) + "@sha256:" + strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := helmTemplate(t, "", "nookku.image="+image)
			if err == nil {
				t.Fatal("helm template must fail")
			}
			if !strings.Contains(out, "nookku.image must end with a digest @sha256:<64 hex characters>.") {
				t.Errorf("the error does not give the value name:\n%s", out)
			}
		})
	}
}
