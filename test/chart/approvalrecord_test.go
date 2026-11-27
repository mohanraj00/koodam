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

// TestApprovalRecordRoles proves that the chart role of the approver integration gives only the
// create of an ApprovalRecord, and that no other role of the chart, other than the manager role,
// has a rule for this resource.
func TestApprovalRecordRoles(t *testing.T) {
	for _, set := range [][]string{nil, {setApps}} {
		out, err := helmTemplate(t, "", set...)
		if err != nil {
			t.Fatalf("helm template failed: %v\n%s", err, out)
		}
		creator := false
		for _, kind := range []string{"Cluster" + kindRole, kindRole} {
			for _, doc := range objects(t, out, kind) {
				var role rbacv1.ClusterRole
				if err := yaml.Unmarshal([]byte(doc), &role); err != nil {
					t.Fatal(err)
				}
				manager := strings.HasSuffix(role.Name, "-manager-role")
				for _, rule := range role.Rules {
					if !approvalRecordRule(rule) {
						continue
					}
					switch {
					case manager:
						if slices.ContainsFunc(rule.Verbs, func(v string) bool { return !slices.Contains(readVerbs, v) }) {
							t.Errorf("%s %s gives the verbs %v on ApprovalRecord", kind, role.Name, rule.Verbs)
						}
					case role.Name == "koodam-approvalrecord-creator" && slices.Equal(rule.Verbs, []string{"create"}) &&
						slices.Equal(rule.Resources, []string{"approvalrecords"}):
						creator = true
					default:
						t.Errorf("%s %s gives the verbs %v on %v", kind, role.Name, rule.Verbs, rule.Resources)
					}
				}
			}
		}
		if !creator {
			t.Errorf("the chart has no role that creates ApprovalRecord (set %q)", set)
		}
	}
}

// approvalRecordRule is true if the rule can match the ApprovalRecord objects.
func approvalRecordRule(rule rbacv1.PolicyRule) bool {
	if !slices.Contains(rule.APIGroups, "core.koodam.dev") && !slices.Contains(rule.APIGroups, "*") {
		return false
	}
	return slices.ContainsFunc(rule.Resources, func(r string) bool {
		return r == "*" || r == "approvalrecords" || strings.HasPrefix(r, "approvalrecords/")
	})
}
