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

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// readVerbs are the only verbs that a role of the chart, other than the manager role, can give
// on a SpendBudget.
var readVerbs = strings.Fields("get list watch")

// TestSpendBudgetRolesOnlyRead proves that only the controller can change a SpendBudget. The
// users of a namespace get the viewer role, which can only read. No role of the chart has an
// aggregate label, so the edit and admin roles of Kubernetes get no SpendBudget rule.
func TestSpendBudgetRolesOnlyRead(t *testing.T) {
	for _, values := range []string{"", "apps:\n  enabled: true\n"} {
		out, err := helmTemplate(t, values)
		if err != nil {
			t.Fatalf("helm template failed: %v\n%s", err, out)
		}
		viewer := false
		for _, kind := range []string{"ClusterRole", "Role"} {
			for _, doc := range objects(t, out, kind) {
				var role rbacv1.ClusterRole
				if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
					t.Fatal(err)
				}
				for label := range role.Labels {
					if strings.Contains(label, "aggregate-to") {
						t.Errorf("%s %s has the aggregate label %s", kind, role.Name, label)
					}
				}
				if strings.HasSuffix(role.Name, "-manager-role") {
					continue
				}
				for _, rule := range role.Rules {
					if !budgetRule(rule) {
						continue
					}
					for _, verb := range rule.Verbs {
						if !slices.Contains(readVerbs, verb) {
							t.Errorf("%s %s gives the verb %q on SpendBudget", kind, role.Name, verb)
						}
					}
				}
				viewer = viewer || role.Name == "koodam-spendbudget-viewer" && hasRead(role.Rules)
			}
		}
		if !viewer {
			t.Errorf("the chart has no viewer role that reads SpendBudget (values %q)", values)
		}
	}
}

// budgetRule is true if the rule can match the SpendBudget objects or their status.
func budgetRule(rule rbacv1.PolicyRule) bool {
	group := slices.Contains(rule.APIGroups, "core.koodam.dev") || slices.Contains(rule.APIGroups, "*")
	if !group {
		return false
	}
	for _, r := range rule.Resources {
		if r == "*" || r == "spendbudgets" || strings.HasPrefix(r, "spendbudgets/") {
			return true
		}
	}
	return false
}

func hasRead(rules []rbacv1.PolicyRule) bool {
	for _, rule := range rules {
		if slices.Contains(rule.Resources, "spendbudgets") {
			got := slices.Clone(rule.Verbs)
			slices.Sort(got)
			return slices.Equal(got, readVerbs)
		}
	}
	return false
}
