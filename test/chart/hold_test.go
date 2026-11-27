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
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// TestHoldRolesOnlyRead proves that no role of the chart, other than the manager role, can
// write a Hold. Only a cluster admin can stop or quarantine a workload. The chart has a viewer
// role that reads the Holds.
func TestHoldRolesOnlyRead(t *testing.T) {
	for _, values := range []string{"", "apps:\n  enabled: true\n"} {
		out, err := helmTemplate(t, values)
		if err != nil {
			t.Fatalf("helm template failed: %v\n%s", err, out)
		}
		viewer := false
		for _, kind := range []string{"ClusterRole", kindRole} {
			for _, doc := range objects(t, out, kind) {
				var role rbacv1.ClusterRole
				if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(role.Name, "-manager-role") {
					continue
				}
				for _, rule := range role.Rules {
					if !holdRule(rule) {
						continue
					}
					for _, verb := range rule.Verbs {
						if !slices.Contains(readVerbs, verb) {
							t.Errorf("%s %s gives the verb %q on Hold", kind, role.Name, verb)
						}
					}
				}
				viewer = viewer || role.Name == "koodam-hold-viewer" && hasHoldRead(role.Rules)
			}
		}
		if !viewer {
			t.Errorf("the chart has no viewer role that reads Hold (values %q)", values)
		}
	}
}

// TestHoldPolicy proves that the chart has the admission policy of the Hold from
// config/hold/admission, with the same rules and the name <fullname>-hold, in each install.
func TestHoldPolicy(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "config", "hold", "admission", "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join("..", "..", "charts", "koodam", "files", "hold-admission-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != string(copied) {
		t.Error("charts/koodam/files/hold-admission-policy.yaml is not config/hold/admission/policy.yaml. " +
			"Run make helm-sync.")
	}
	want := map[string]map[string]any{}
	for doc := range strings.SplitSeq(string(src), "\n---\n") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		want[obj["kind"].(string)] = obj["spec"].(map[string]any)
	}
	for _, values := range []string{"", "apps: {enabled: true, sharedResources: false}\n"} {
		out, err := helmTemplate(t, values)
		if err != nil {
			t.Fatalf("helm template failed: %v\n%s", err, out)
		}
		for kind, spec := range want {
			found := 0
			for _, doc := range objects(t, out, kind) {
				var obj struct {
					Metadata struct{ Name string } `json:"metadata"`
					Spec     map[string]any        `json:"spec"`
				}
				if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
					t.Fatal(err)
				}
				if obj.Metadata.Name != "koodam-hold" {
					continue
				}
				found++
				if kind == "ValidatingAdmissionPolicyBinding" {
					spec = maps.Clone(spec)
					spec["policyName"] = "koodam-hold"
				}
				if !reflect.DeepEqual(obj.Spec, spec) {
					t.Errorf("values %q: the %s koodam-hold of the chart is not the one of config/hold/admission", values, kind)
				}
			}
			if found != 1 {
				t.Errorf("values %q: got %d %s koodam-hold, want 1", values, found, kind)
			}
		}
	}
}

// kindRole is the kind of a namespaced role.
const kindRole = "Role"

// holdRule is true if the rule can match the Hold objects or their status.
func holdRule(rule rbacv1.PolicyRule) bool {
	if !slices.Contains(rule.APIGroups, "core.koodam.dev") && !slices.Contains(rule.APIGroups, "*") {
		return false
	}
	for _, r := range rule.Resources {
		if r == "*" || r == "holds" || strings.HasPrefix(r, "holds/") {
			return true
		}
	}
	return false
}

func hasHoldRead(rules []rbacv1.PolicyRule) bool {
	for _, rule := range rules {
		if slices.Contains(rule.Resources, "holds") {
			got := slices.Clone(rule.Verbs)
			slices.Sort(got)
			return slices.Equal(got, readVerbs)
		}
	}
	return false
}
