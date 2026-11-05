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
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"

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
// variant, merge key, alias or explicit tag. The non-specific tag "!" is also an explicit tag. The
// root and metadata mappings and their keys have the same rule. The YAML-to-JSON converter must
// keep each map entry. The data must not start with two byte order marks.
// The function examines these before the conversion. It does not change the data.
func ValidateRBACRole(data []byte, expectedName string) error {
	if expectedName == "" {
		return ErrNotRole
	}
	// This check comes before each parser. Only the first chunk can start with a byte order mark,
	// because each other chunk starts with "---". Thus a check of the data start is sufficient.
	if repeatedByteOrderMark(data) {
		return ErrRoleMalformed
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

// byteOrderMarks are the encodings of U+FEFF in UTF-8, UTF-16LE and UTF-16BE. No mark is a prefix
// of a different mark.
var byteOrderMarks = [][]byte{[]byte("\xef\xbb\xbf"), []byte("\xff\xfe"), []byte("\xfe\xff")}

// repeatedByteOrderMark is true if the data starts with a byte order mark and a second byte order
// mark in the same encoding follows it. The YAML parsers remove only the first mark. After that,
// the scanners find the second mark at the start of the buffer, not at the current position, and
// skip the first character of each line. Thus they can ignore an explicit tag.
func repeatedByteOrderMark(data []byte) bool {
	for _, mark := range byteOrderMarks {
		if bytes.HasPrefix(data, mark) {
			return bytes.HasPrefix(data[len(mark):], mark)
		}
	}
	return false
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
	src := newYAMLText(doc)
	top, ok := src.identityMap(root.Content[0])
	if !ok {
		return false
	}
	meta, ok := src.identityMap(top["metadata"])
	if !ok {
		return false
	}
	return src.identityString(top["apiVersion"]) == rbacv1.SchemeGroupVersion.String() &&
		src.identityString(top["kind"]) == roleKind && src.identityString(meta["name"]) == name &&
		!hasField(meta, "namespace") && !hasField(meta, "generateName")
}

// identityMap gives the entries of a mapping node by the exact key. The mapping node must not have
// an explicit tag. Each key must be a string with no explicit tag and must not be a merge key. Two
// keys must not be equal under Unicode case folding.
func (src yamlText) identityMap(n *yaml.Node) (map[string]*yaml.Node, bool) {
	if n == nil || n.Kind != yaml.MappingNode || n.Tag != "!!map" || src.explicitTag(n) || len(n.Content)%2 != 0 {
		return nil, false
	}
	m := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || src.explicitTag(k) || k.Value == "<<" || hasField(m, k.Value) {
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
func (src yamlText) identityString(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" || src.explicitTag(n) {
		return ""
	}
	return n.Value
}

// yamlText is the document text as the YAML parser reads it. The parser removes a byte order mark
// at the start and decodes UTF-16. It counts each character as one column. CRLF is one line break.
type yamlText struct {
	text  []rune
	lines []int // The index in text of the start of each line.
}

func newYAMLText(doc []byte) yamlText {
	var src yamlText
	if len(doc) >= 2 && (doc[0] == 0xff && doc[1] == 0xfe || doc[0] == 0xfe && doc[1] == 0xff) {
		var order binary.ByteOrder = binary.BigEndian
		if doc[0] == 0xff {
			order = binary.LittleEndian
		}
		units := make([]uint16, (len(doc)-2)/2)
		for i := range units {
			units[i] = order.Uint16(doc[2+2*i:])
		}
		src.text = utf16.Decode(units)
	} else {
		src.text = []rune(string(bytes.TrimPrefix(doc, []byte("\xef\xbb\xbf"))))
	}
	src.lines = []int{0}
	for i := 0; i < len(src.text); i++ {
		if src.text[i] == '\r' && i+1 < len(src.text) && src.text[i+1] == '\n' {
			i++
		}
		if breakRune(src.text[i]) {
			src.lines = append(src.lines, i+1)
		}
	}
	return src
}

// at gives the character at index i, or 0 after the end of the text.
func (src yamlText) at(i int) rune {
	if i < 0 || i >= len(src.text) {
		return 0
	}
	return src.text[i]
}

// explicitTag is true if the source has a tag on the node, for example !!str, !!map or the
// non-specific tag "!". The parser sets TaggedStyle for each tag except "!". Thus this function
// also examines the text at the start position of the node. If the node has properties, the parser
// puts this position at the first property. The tag can come before or after the anchor. Spaces,
// tabs, comments and line breaks can be between the two properties. A plain scalar cannot start
// with "!", thus a "!" at the start of the node or after the anchor is always a tag. If the start
// of the node is not in the text, the function refuses the node.
func (src yamlText) explicitTag(n *yaml.Node) bool {
	if n.Style&yaml.TaggedStyle != 0 {
		return true
	}
	if n.Line < 1 || n.Line > len(src.lines) || n.Column < 1 {
		return true
	}
	i := src.lines[n.Line-1] + n.Column - 1
	if src.at(i) != '&' {
		return src.at(i) == '!'
	}
	i++
	for anchorChar(src.at(i)) {
		i++
	}
	for {
		switch r := src.at(i); {
		case r == '#':
			for i < len(src.text) && !breakRune(src.text[i]) {
				i++
			}
		// The parser ignores a byte order mark only at the start of a line.
		case r == ' ' || r == '\t' || breakRune(r) || r == '\ufeff' && breakRune(src.at(i-1)):
			i++
		default:
			return r == '!'
		}
	}
}

// breakRune is true for each YAML line break character.
func breakRune(r rune) bool {
	return r == '\n' || r == '\r' || r == '\u0085' || r == '\u2028' || r == '\u2029'
}

// anchorChar is true for each character that the parser permits in an anchor name.
func anchorChar(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || r == '-'
}
