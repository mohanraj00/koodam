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
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

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
	// nullDoc is a document with only a literal null.
	nullDoc = "null\n"
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
		{"explicit string name", roleHead + "metadata:\n  name: !!str " + roleName + "\n" + roleRules},
		{"explicit string kind key", "apiVersion: rbac.authorization.k8s.io/v1\n!!str kind: ClusterRole\n" + roleMeta + rule},
		{"explicit map metadata", roleHead + "metadata: !!map\n  name: " + roleName + "\n" + roleRules},
		{"integer key", roleHead + roleMeta + "1: a\n" + rule},
		{"list document", "- " + roleName + "\n"},
		{"scalar document", "ClusterRole\n"},
		{"null document", nullDoc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { requireRoleRejected(t, []byte(tc.doc), roleName, ml.ErrNotRole) })
	}
	t.Run("no expected name", func(t *testing.T) {
		requireRoleRejected(t, []byte(roleHead+"metadata: {}\n"+roleRules), "", ml.ErrNotRole)
	})
}

// The parser does not mark the non-specific tag "!" on the node. The check finds it in the source
// on the identity values, the identity keys and the root and metadata mappings. A "!" in other
// content or in a comment is permitted.
func TestValidateRBACRoleBareTag(t *testing.T) {
	api := "apiVersion: rbac.authorization.k8s.io/v1\n"
	rejected := []roleDoc{
		{"tagged kind", api + "kind: ! ClusterRole\n" + roleMeta + roleRules},
		{"tagged quoted kind", api + "kind: ! \"ClusterRole\"\n" + roleMeta + roleRules},
		{"verbatim tagged kind", api + "kind: !<!> ClusterRole\n" + roleMeta + roleRules},
		{"tagged kind on next line", api + "kind:\n  ! ClusterRole\n" + roleMeta + roleRules},
		{"anchor then tag on kind", api + "kind: &k ! ClusterRole\n" + roleMeta + roleRules},
		{"tag then anchor on kind", api + "kind: ! &k ClusterRole\n" + roleMeta + roleRules},
		{"tag after anchor and comments", roleHead + "metadata:\n  name: &n # !\n    # !\n    ! " + roleName + "\n" + roleRules},
		{"tagged apiVersion", "apiVersion: ! rbac.authorization.k8s.io/v1\nkind: ClusterRole\n" + roleMeta + roleRules},
		{"tagged apiVersion after byte order mark", "\ufeffapiVersion: ! rbac.authorization.k8s.io/v1\nkind: ClusterRole\n" + roleMeta + roleRules},
		{"tagged name", roleHead + "metadata:\n  name: ! " + roleName + "\n" + roleRules},
		{"tagged name after wide characters", roleHead + "metadata: {labels: {a: ää}, name: ! " + roleName + "}\n" + roleRules},
		{"tagged kind key", api + "! kind: ClusterRole\n" + roleMeta + roleRules},
		{"tagged first key", "! apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\n" + roleMeta + roleRules},
		{"tagged rules key", roleHead + roleMeta + "! rules: []\n"},
		{"tagged name key", roleHead + "metadata:\n  ! name: " + roleName + "\n" + roleRules},
		{"tagged flow name key", roleHead + "metadata: {! name: " + roleName + "}\n" + roleRules},
		{"tagged root mapping", "--- !\n" + bareRole},
		{"anchored and tagged root mapping", "--- &r !\n" + bareRole},
		{"tagged metadata", roleHead + "metadata: !\n  name: " + roleName + "\n" + roleRules},
		{"tagged flow metadata", roleHead + "metadata: ! {name: " + roleName + "}\n" + roleRules},
	}
	accepted := []roleDoc{
		{"anchored kind", api + "kind: &k ClusterRole\n" + roleMeta + roleRules},
		{"anchored metadata", roleHead + "metadata: &m\n  name: " + roleName + "\n" + roleRules},
		{"anchored name after comments", roleHead + "metadata:\n  name: &n # !\n    # !\n    " + roleName + "\n" + roleRules},
		{"tag text in comments", "--- # !\n" + api + "kind: ClusterRole # !\n# !\n" + roleMeta + roleRules},
		{"tag text in values", roleHead + roleMeta + "  labels: {a: \"!\", b: c!}\n" + roleRules},
		{"wide characters", roleHead + "metadata: {labels: {a: ää}, name: " + roleName + "}\n" + roleRules},
		{"tag in rules", roleHead + roleMeta + "rules:\n- apiGroups: [! example.com]\n  resources: [gadgets]\n  verbs: [get]\n"},
		{"byte order mark", "\ufeff" + bareRole},
	}
	for _, e := range lineEndings {
		// This is the reproduction from the security review.
		t.Run(e.name+" rejects reproduction", func(t *testing.T) {
			requireRoleRejected(t, withBreaks("---\n"+api+"kind: ! ClusterRole\n"+roleMeta+roleRules, e.breaks), roleName, ml.ErrNotRole)
		})
		for _, tc := range rejected {
			t.Run(e.name+" rejects "+tc.name, func(t *testing.T) {
				requireRoleRejected(t, withBreaks(tc.doc, e.breaks), roleName, ml.ErrNotRole)
			})
		}
		for _, tc := range accepted {
			t.Run(e.name+" accepts "+tc.name, func(t *testing.T) {
				requireValidRole(t, withBreaks(tc.doc, e.breaks), roleName)
			})
		}
	}
}

// roleEncodings are the encodings that the YAML parsers find from a byte order mark. Each one
// encodes the UTF-8 text, and a U+FEFF in the text becomes the mark of that encoding.
var roleEncodings = []struct {
	name   string
	encode func([]byte) []byte
}{
	{"UTF-8", func(b []byte) []byte { return b }},
	{"UTF-16LE", func(b []byte) []byte { return utf16Text(b, binary.LittleEndian) }},
	{"UTF-16BE", func(b []byte) []byte { return utf16Text(b, binary.BigEndian) }},
}

// utf16Text encodes the UTF-8 text as UTF-16 in the given byte order.
func utf16Text(b []byte, order binary.AppendByteOrder) []byte {
	units := utf16.Encode([]rune(string(b)))
	out := make([]byte, 0, 2*len(units))
	for _, u := range units {
		out = order.AppendUint16(out, u)
	}
	return out
}

// Two leading byte order marks make the YAML scanners skip the first character of each line. Thus
// they ignore the explicit tags. The check refuses the data before each parser. One mark is valid.
func TestValidateRBACRoleRepeatedByteOrderMark(t *testing.T) {
	// This is the reproduction from the security review.
	const reproduction = "\ufeff\ufeff apiVersion: rbac.authorization.k8s.io/v1\n" +
		"! kind: ClusterRole\n" +
		"! metadata:\n" +
		"    name: koodam-app\n" +
		"! rules: []\n"
	for _, enc := range roleEncodings {
		for _, e := range lineEndings {
			t.Run(enc.name+" "+e.name+" rejects reproduction", func(t *testing.T) {
				requireRoleRejected(t, enc.encode(withBreaks(reproduction, e.breaks)), roleName, ml.ErrRoleMalformed)
			})
			t.Run(enc.name+" "+e.name+" rejects two marks", func(t *testing.T) {
				requireRoleRejected(t, enc.encode(withBreaks("\ufeff\ufeff"+bareRole, e.breaks)), roleName, ml.ErrRoleMalformed)
			})
			t.Run(enc.name+" "+e.name+" accepts one mark", func(t *testing.T) {
				requireValidRole(t, enc.encode(withBreaks("\ufeff"+bareRole, e.breaks)), roleName)
			})
		}
	}
	for _, e := range lineEndings {
		t.Run(e.name+" accepts no mark", func(t *testing.T) {
			requireValidRole(t, withBreaks(bareRole, e.breaks), roleName)
		})
	}
}

// A document end marker is refused in each encoding. The check reads the decoded text, thus a
// UTF-16 marker is also refused. One mark without the marker is valid.
func TestValidateRBACRoleDocumentEndAllEncodings(t *testing.T) {
	for _, enc := range roleEncodings {
		for _, e := range lineEndings {
			// This is the reproduction from the review.
			t.Run(enc.name+" "+e.name+" rejects document end marker", func(t *testing.T) {
				requireRoleRejected(t, enc.encode(withBreaks("\ufeff"+bareRole+"...\n", e.breaks)), roleName, ml.ErrRoleDocumentCount)
			})
			t.Run(enc.name+" "+e.name+" accepts one mark", func(t *testing.T) {
				requireValidRole(t, enc.encode(withBreaks("\ufeff"+bareRole, e.breaks)), roleName)
			})
		}
	}
}

// In UTF-16, the bytes of some comment characters look like a line break and a "..." or "---"
// marker. These comments are valid YAML. The check reads the decoded text, thus it accepts them.
// A real document start is valid once, and an extra document is refused.
func TestValidateRBACRoleUTF16MarkerBytes(t *testing.T) {
	comments := []roleDoc{
		// In UTF-16LE, these bytes are a LF and a "..." marker.
		{"UTF-16LE end bytes", "# \u2e0a\u2e2e\n"},
		// In UTF-16LE, these bytes are a LF and a "---" marker with a space.
		{"UTF-16LE start bytes", "# \u2d0a\u2d2d \n"},
		// In UTF-16BE, these bytes are a LF and a "..." marker.
		{"UTF-16BE end bytes", "# \u0a2e\u2e2e\n"},
		// In UTF-16BE, these bytes are a LF and a "---" marker with a space.
		{"UTF-16BE start bytes", "# \u0a2d\u2d2d\u2000\n"},
	}
	for _, enc := range roleEncodings {
		for _, e := range lineEndings {
			for _, c := range comments {
				t.Run(enc.name+" "+e.name+" accepts "+c.name, func(t *testing.T) {
					requireValidRole(t, enc.encode(withBreaks("\ufeff"+c.doc+bareRole, e.breaks)), roleName)
				})
				t.Run(enc.name+" "+e.name+" accepts "+c.name+" before document start", func(t *testing.T) {
					requireValidRole(t, enc.encode(withBreaks("\ufeff"+c.doc+"---\n"+bareRole, e.breaks)), roleName)
				})
				t.Run(enc.name+" "+e.name+" rejects "+c.name+" and extra document", func(t *testing.T) {
					requireRoleRejected(t, enc.encode(withBreaks("\ufeff"+c.doc+bareRole+"---\n"+bareRole, e.breaks)), roleName, ml.ErrRoleDocumentCount)
				})
				t.Run(enc.name+" "+e.name+" rejects "+c.name+" and document end marker", func(t *testing.T) {
					requireRoleRejected(t, enc.encode(withBreaks("\ufeff"+c.doc+bareRole+"...\n", e.breaks)), roleName, ml.ErrRoleDocumentCount)
				})
			}
			t.Run(enc.name+" "+e.name+" accepts one document start", func(t *testing.T) {
				requireValidRole(t, enc.encode(withBreaks("\ufeff---\n"+bareRole, e.breaks)), roleName)
			})
			t.Run(enc.name+" "+e.name+" rejects extra document", func(t *testing.T) {
				requireRoleRejected(t, enc.encode(withBreaks("\ufeff---\n"+bareRole+"---\n"+bareRole, e.breaks)), roleName, ml.ErrRoleDocumentCount)
			})
			t.Run(enc.name+" "+e.name+" rejects empty document after", func(t *testing.T) {
				requireRoleRejected(t, enc.encode(withBreaks("\ufeff"+bareRole+"---\n", e.breaks)), roleName, ml.ErrRoleDocumentCount)
			})
			t.Run(enc.name+" "+e.name+" rejects empty document before", func(t *testing.T) {
				requireRoleRejected(t, enc.encode(withBreaks("\ufeff---\n---\n"+bareRole, e.breaks)), roleName, ml.ErrRoleDocumentCount)
			})
		}
	}
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
		{"other object", "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: other}\n"},
		{"second role", bareRole},
		{"null", nullDoc},
		{"empty map", "{}\n"},
	}
	base := []roleDoc{
		{"empty document before", "---\n---\n" + bareRole},
		{"empty document after", bareRole + "---\n"},
		{"comment document after", bareRole + "---\n# tail\n"},
		{"null on separator line", bareRole + "--- null\n"},
		{"document end marker", bareRole + "...\n"},
		{"no content", ""},
		{"only comment", "# only\n"},
		{"only separator", "---\n"},
	}
	docs := make([]roleDoc, 0, len(base)+2*len(extras))
	docs = append(docs, base...)
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
