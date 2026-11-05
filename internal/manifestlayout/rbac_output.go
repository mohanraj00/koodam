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

package manifestlayout

import (
	"errors"
	"strings"

	"go.yaml.in/yaml/v3"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	serjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
)

const roleKind = "ClusterRole"

// The errors have fixed text. They do not contain a role name, source text or decoder output.
var (
	ErrRoleDocumentCount = errors.New("manifestlayout: RBAC role output must contain exactly one YAML document")
	ErrRoleMalformed     = errors.New("manifestlayout: RBAC role output is not strict valid YAML for a ClusterRole")
	ErrNotRole           = errors.New("manifestlayout: RBAC role output is not one rbac.authorization.k8s.io/v1 ClusterRole with the expected name, no namespace and no generated name")
	errRoleDecoder       = errors.New("manifestlayout: cannot make the RBAC role decoder")
)

// ValidateRBACRole examines the full controller-gen output for one role. The stream must have
// exactly one YAML document. An empty document is also a document. The document must be one
// strict rbac.authorization.k8s.io/v1 ClusterRole with the expected name, no namespace field and
// no generateName field. The identity fields must be plain string fields with no duplicate, case
// variant, merge key, alias or explicit tag. The YAML-to-JSON converter must keep each map entry.
// The function examines these before the conversion. It does not change the data.
func ValidateRBACRole(data []byte, expectedName string) error {
	if expectedName == "" {
		return ErrNotRole
	}
	doc, err := singleDocument(data)
	switch {
	case errors.Is(err, ErrDocumentCount):
		return ErrRoleDocumentCount
	case err != nil:
		return ErrRoleMalformed
	}
	// singleDocument permits empty documents around the content. The role output does not.
	switch total, _, err := countDocuments(data); {
	case err != nil:
		return ErrRoleMalformed
	case total != 1:
		return ErrRoleDocumentCount
	}
	if !plainRoleIdentity(doc, expectedName) {
		return ErrNotRole
	}
	if !keepsAllKeys(doc) {
		return ErrRoleMalformed
	}
	decoder, err := roleDecoder()
	if err != nil {
		return err
	}
	obj, gvk, err := decoder.Decode(doc, nil, nil)
	if err != nil {
		return ErrRoleMalformed
	}
	role, ok := obj.(*rbacv1.ClusterRole)
	if !ok || gvk == nil || *gvk != rbacv1.SchemeGroupVersion.WithKind(roleKind) ||
		role.Name != expectedName || role.Namespace != "" || role.GenerateName != "" {
		return ErrNotRole
	}
	return nil
}

// roleDecoder gives a strict decoder that knows only the rbac.authorization.k8s.io/v1 types. It
// rejects unknown fields, duplicate fields and fields with the wrong case. It does not apply
// defaults.
func roleDecoder() (runtime.Decoder, error) {
	scheme := runtime.NewScheme()
	if err := rbacv1.AddToScheme(scheme); err != nil {
		return nil, errRoleDecoder
	}
	return serjson.NewSerializerWithOptions(serjson.DefaultMetaFactory, scheme, scheme,
		serjson.SerializerOptions{Yaml: true, Strict: true}), nil
}

// plainRoleIdentity is true if the document has the ClusterRole identity as plain fields. The
// meta factory of the decoder finds apiVersion and kind without the case, thus a case variant is
// ambiguous. The metadata must not have a namespace or generateName field in each case.
func plainRoleIdentity(doc []byte, name string) (ok bool) {
	// The parser can panic on some input. A panic is malformed output.
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	var root yaml.Node
	if yaml.Unmarshal(doc, &root) != nil || root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return false
	}
	top, ok := identityMap(root.Content[0])
	if !ok {
		return false
	}
	meta, ok := identityMap(top["metadata"])
	if !ok {
		return false
	}
	return identityString(top["apiVersion"]) == rbacv1.SchemeGroupVersion.String() &&
		identityString(top["kind"]) == roleKind && identityString(meta["name"]) == name &&
		!hasField(meta, "namespace") && !hasField(meta, "generateName")
}

// identityMap gives the entries of a mapping node by the exact key. The mapping node must not have
// an explicit tag. Each key must be a string with no explicit tag and must not be a merge key. Two
// keys must not be equal under Unicode case folding.
func identityMap(n *yaml.Node) (map[string]*yaml.Node, bool) {
	if n == nil || n.Kind != yaml.MappingNode || n.Tag != "!!map" || explicitTag(n) || len(n.Content)%2 != 0 {
		return nil, false
	}
	m := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || explicitTag(k) || k.Value == "<<" || hasField(m, k.Value) {
			return nil, false
		}
		m[k.Value] = n.Content[i+1]
	}
	return m, true
}

// hasField is true if m has a key that is equal to name under Unicode case folding.
func hasField(m map[string]*yaml.Node, name string) bool {
	for k := range m {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// identityString gives the value of a string scalar node with no explicit tag, or "" for each
// other node. An alias node is not a scalar node. A quoted string has no explicit tag.
func identityString(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" || explicitTag(n) {
		return ""
	}
	return n.Value
}

// explicitTag is true if the source has a tag on the node, for example !!str or !!map. The parser
// sets TaggedStyle for each tag except the non-specific tag "!".
func explicitTag(n *yaml.Node) bool {
	return n.Style&yaml.TaggedStyle != 0
}
