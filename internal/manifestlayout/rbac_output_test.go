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

package manifestlayout_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ml "github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	roleName = "koodam-app"
	roleHead = "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\n"
	roleMeta = "metadata:\n  name: " + roleName + "\n"
	// The rules have synthetic values. They are not grants of this project.
	roleRules = "rules:\n- apiGroups:\n  - example.com\n  resources:\n  - gadgets\n  verbs:\n  - get\n"
	// bareRole is a synthetic App role without the initial separator.
	bareRole = roleHead + roleMeta + roleRules
)

// roleDoc is one named test input.
type roleDoc struct{ name, doc string }

// requireValidRole examines that ValidateRBACRole accepts the data and does not change it.
func requireValidRole(t *testing.T, data []byte, name string) {
	t.Helper()
	before := bytes.Clone(data)
	if err := ml.ValidateRBACRole(data, name); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("ValidateRBACRole changed the input")
	}
}

// requireRoleRejected examines that ValidateRBACRole gives the fixed error and does not change
// the data.
func requireRoleRejected(t *testing.T, data []byte, name string, want error) {
	t.Helper()
	before := bytes.Clone(data)
	err := ml.ValidateRBACRole(data, name)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if err.Error() != want.Error() {
		t.Fatalf("error text %q is not the fixed reason", err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("ValidateRBACRole changed the input")
	}
}

// The current controller-gen base role is valid only with its own name.
func TestValidateRBACRoleCurrentBase(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	requireValidRole(t, data, "manager-role")
	requireRoleRejected(t, data, roleName, ml.ErrNotRole)
}

func TestValidateRBACRoleSyntheticApp(t *testing.T) {
	for _, e := range lineEndings {
		t.Run(e.name, func(t *testing.T) {
			requireValidRole(t, withBreaks("---\n"+bareRole, e.breaks), roleName)
		})
	}
	inputs := []roleDoc{
		{"no separator", bareRole},
		{"flow metadata", roleHead + "metadata: {name: " + roleName + "}\n" + roleRules},
		{"quoted name", roleHead + "metadata:\n  name: \"" + roleName + "\"\n" + roleRules},
		{"metadata after rules", roleHead + roleRules + roleMeta},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) { requireValidRole(t, []byte(tc.doc), roleName) })
	}
}

// The identity fields must be plain and not ambiguous. The checks come before the conversion.
func TestValidateRBACRoleRejectsIdentity(t *testing.T) {
	rule := "rules:\n- apiGroups: [example.com]\n  resources: [gadgets]\n  verbs: [get]\n"
	cases := []roleDoc{
		{"beta version", "apiVersion: rbac.authorization.k8s.io/v1beta1\nkind: ClusterRole\n" + roleMeta + roleRules},
		{"core version", "apiVersion: v1\nkind: ClusterRole\n" + roleMeta + roleRules},
		{"Role kind", "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\n" + roleMeta + roleRules},
		{"no kind", "apiVersion: rbac.authorization.k8s.io/v1\n" + roleMeta + roleRules},
		{"wrong name", roleHead + "metadata:\n  name: other\n" + roleRules},
		{"no name", roleHead + "metadata:\n  labels: {a: b}\n" + roleRules},
		{"no metadata", roleHead + roleRules},
		{"namespace", roleHead + roleMeta + "  namespace: default\n" + roleRules},
		{"empty namespace", roleHead + roleMeta + "  namespace: \"\"\n" + roleRules},
		{"null namespace", roleHead + roleMeta + "  namespace: null\n" + roleRules},
		{"case variant namespace", roleHead + roleMeta + "  Namespace: default\n" + roleRules},
		{"folded namespace", roleHead + roleMeta + "  nameſpace: default\n" + roleRules},
		{"generated name", roleHead + roleMeta + "  generateName: koodam-\n" + roleRules},
		{"case variant generated name", roleHead + roleMeta + "  generatename: koodam-\n" + roleRules},
		{"duplicate name", roleHead + roleMeta + "  name: other\n" + roleRules},
		{"same duplicate name", roleHead + roleMeta + "  name: " + roleName + "\n" + roleRules},
		{"duplicate kind", roleHead + "kind: ClusterRole\n" + roleMeta + roleRules},
		{"duplicate metadata", roleHead + roleMeta + roleMeta + roleRules},
		{"case variant kind", roleHead + "Kind: Role\n" + roleMeta + roleRules},
		{"only case variant kind", "apiVersion: rbac.authorization.k8s.io/v1\nKind: ClusterRole\n" + roleMeta + roleRules},
		{"Kelvin sign kind", roleHead + "Kind: Role\n" + roleMeta + roleRules},
		{"case variant apiVersion", roleHead + "apiversion: v1\n" + roleMeta + roleRules},
		{"case variant metadata", roleHead + roleMeta + "Metadata: {name: other}\n" + roleRules},
		{"case variant name", roleHead + roleMeta + "  Name: other\n" + roleRules},
		{"merge metadata", roleHead + "metadata:\n  <<: {name: " + roleName + "}\n" + roleRules},
		{"merge top", "<<: {kind: ClusterRole}\napiVersion: rbac.authorization.k8s.io/v1\n" + roleMeta + roleRules},
		{"alias name", roleHead + "rules:\n- apiGroups: [&n " + roleName + "]\n  resources: [gadgets]\n  verbs: [get]\nmetadata:\n  name: *n\n"},
		{"alias kind", "apiVersion: rbac.authorization.k8s.io/v1\n" + "rules:\n- apiGroups: [&k ClusterRole]\n  resources: [gadgets]\n  verbs: [get]\nkind: *k\n" + roleMeta},
		{"tagged kind", "apiVersion: rbac.authorization.k8s.io/v1\nkind: !custom ClusterRole\n" + roleMeta + rule},
		{"integer key", roleHead + roleMeta + "1: a\n" + rule},
		{"list document", "- " + roleName + "\n"},
		{"scalar document", "ClusterRole\n"},
		{"null document", "null\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { requireRoleRejected(t, []byte(tc.doc), roleName, ml.ErrNotRole) })
	}
	t.Run("no expected name", func(t *testing.T) {
		requireRoleRejected(t, []byte(roleHead+"metadata: {}\n"+roleRules), "", ml.ErrNotRole)
	})
}

// The strict decoder and the key check reject the content after the identity check.
func TestValidateRBACRoleRejectsMalformed(t *testing.T) {
	cases := []roleDoc{
		{"syntax", "---\n" + roleHead + roleMeta + "rules: [\n"},
		{"wrong indent", roleHead + "metadata:\n  name: " + roleName + "\n   labels: {a: b}\n" + roleRules},
		{"unknown metadata field", roleHead + roleMeta + "  colour: red\n" + roleRules},
		{"unknown top field", bareRole + "extra: 1\n"},
		{"wrong rules type", roleHead + roleMeta + "rules: 7\n"},
		{"case variant rule field", roleHead + roleMeta + "rules:\n- apiGroups: [example.com]\n  Resources: [gadgets]\n  verbs: [get]\n"},
		{"duplicate rule field", roleHead + roleMeta + "rules:\n- apiGroups: [example.com]\n  resources: [gadgets]\n  verbs: [get]\n  verbs: [list]\n"},
		{"integer key collision", roleHead + roleMeta + "  labels:\n    1: a\n    \"1\": b\n" + roleRules},
		{"boolean key collision", roleHead + roleMeta + "  labels: {yes: a, \"true\": b}\n" + roleRules},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { requireRoleRejected(t, []byte(tc.doc), roleName, ml.ErrRoleMalformed) })
	}
}

// The full stream must have exactly one document. An empty document also counts.
func TestValidateRBACRoleRejectsExtraDocuments(t *testing.T) {
	extras := []roleDoc{
		{"ConfigMap", "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: other}\n"},
		{"second role", bareRole},
		{"null", "null\n"},
		{"empty map", "{}\n"},
	}
	docs := []roleDoc{
		{"empty document before", "---\n---\n" + bareRole},
		{"empty document after", bareRole + "---\n"},
		{"comment document after", bareRole + "---\n# tail\n"},
		{"null on separator line", bareRole + "--- null\n"},
		{"document end marker", bareRole + "...\n"},
		{"no content", ""},
		{"only comment", "# only\n"},
		{"only separator", "---\n"},
	}
	for _, x := range extras {
		docs = append(docs, roleDoc{x.name + " after", bareRole + "---\n" + x.doc}, roleDoc{x.name + " before", x.doc + "---\n" + bareRole})
	}
	for _, e := range lineEndings {
		for _, d := range docs {
			t.Run(e.name+" "+d.name, func(t *testing.T) {
				requireRoleRejected(t, withBreaks(d.doc, e.breaks), roleName, ml.ErrRoleDocumentCount)
			})
		}
	}
}
