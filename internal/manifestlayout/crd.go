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

// Package manifestlayout gives the destination of each generated manifest in the repository.
// controller-gen writes the CRDs to a staging directory. This package finds the destination of
// each CRD from a closed kind index. It does not read or write files.
package manifestlayout

import (
	"bytes"
	"errors"
	"io"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	serjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
)

const (
	crdGroup   = "core.koodam.dev"
	baseCRDDir = "config/crd/bases"
	appCRDDir  = "config/apps/crd/bases"
	nameChars  = "abcdefghijklmnopqrstuvwxyz0123456789._-"
)

// The errors have fixed text. They do not contain a value from the input.
var (
	ErrUnsafeName    = errors.New("manifestlayout: artifact name is not a safe file name")
	ErrDocumentCount = errors.New("manifestlayout: artifact must contain exactly one YAML document")
	ErrMalformed     = errors.New("manifestlayout: artifact is not strict valid YAML for a CRD")
	ErrNotCRD        = errors.New("manifestlayout: artifact is not an apiextensions.k8s.io/v1 CustomResourceDefinition")
	ErrWrongGroup    = errors.New("manifestlayout: CRD group is not core.koodam.dev")
	ErrUnknownKind   = errors.New("manifestlayout: CRD kind is not in the kind index")
	ErrWrongIdentity = errors.New("manifestlayout: CRD name or plural does not agree with the kind index")
	ErrNameMismatch  = errors.New("manifestlayout: artifact name is not the controller-gen file name of the kind")
	ErrDuplicate     = errors.New("manifestlayout: two artifacts have the same destination")
	errDecoder       = errors.New("manifestlayout: cannot make the CRD decoder")
)

// Artifact is one CRD file that controller-gen wrote to the staging directory.
type Artifact struct {
	// Name is the file name from controller-gen. It is not a path.
	Name string
	// Data is the full content of the file.
	Data []byte
}

// Placement is one CRD file and its destination.
type Placement struct {
	// Path is the destination, relative to the repository root, with "/" separators. Only the
	// kind index gives this value.
	Path string
	// Data is a copy of the artifact content. The bytes are the same.
	Data []byte
}

type route struct{ kind, plural, dir string }

// kindIndex gives the closed kind index. Each call makes a new slice, thus no caller can change
// the index.
func kindIndex() []route {
	return []route{
		{kind: "Agent", plural: "agents", dir: baseCRDDir},
		{kind: "Profile", plural: "profiles", dir: baseCRDDir},
		{kind: "TestKey", plural: "testkeys", dir: baseCRDDir},
		{kind: "Workspace", plural: "workspaces", dir: baseCRDDir},
		{kind: "App", plural: "apps", dir: appCRDDir},
		{kind: "AppVersion", plural: "appversions", dir: appCRDDir},
	}
}

// RouteCRDs gives the destination of each artifact, sorted by path. If one artifact is not
// valid, it gives no plan and an error. It does not change the input.
func RouteCRDs(artifacts []Artifact) ([]Placement, error) {
	// The decoder knows only the apiextensions.k8s.io/v1 types. It rejects unknown fields,
	// duplicate fields and fields with the wrong case. It does not apply defaults.
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, errDecoder
	}
	decoder := serjson.NewSerializerWithOptions(serjson.DefaultMetaFactory, scheme, scheme,
		serjson.SerializerOptions{Yaml: true, Strict: true})
	plan := make([]Placement, 0, len(artifacts))
	for _, a := range artifacts {
		p, err := place(decoder, a)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(plan, func(q Placement) bool { return q.Path == p.Path }) {
			return nil, ErrDuplicate
		}
		plan = append(plan, p)
	}
	slices.SortFunc(plan, func(x, y Placement) int { return strings.Compare(x.Path, y.Path) })
	return plan, nil
}

func place(decoder runtime.Decoder, a Artifact) (Placement, error) {
	if !safeName(a.Name) {
		return Placement{}, ErrUnsafeName
	}
	doc, err := singleDocument(a.Data)
	if err != nil {
		return Placement{}, err
	}
	crd, err := decodeCRD(decoder, doc)
	if err != nil {
		return Placement{}, err
	}
	if crd.Spec.Group != crdGroup {
		return Placement{}, ErrWrongGroup
	}
	index := kindIndex()
	i := slices.IndexFunc(index, func(r route) bool { return r.kind == crd.Spec.Names.Kind })
	if i < 0 {
		return Placement{}, ErrUnknownKind
	}
	r := index[i]
	if crd.Spec.Names.Plural != r.plural || crd.Name != r.plural+"."+crdGroup {
		return Placement{}, ErrWrongIdentity
	}
	file := crdGroup + "_" + r.plural + ".yaml"
	if a.Name != file {
		return Placement{}, ErrNameMismatch
	}
	return Placement{Path: path.Join(r.dir, file), Data: bytes.Clone(a.Data)}, nil
}

// safeName accepts only a plain file name with the characters that controller-gen uses.
func safeName(name string) bool {
	bad := func(c rune) bool { return !strings.ContainsRune(nameChars, c) }
	return name != "" && name[0] != '.' && !strings.ContainsFunc(name, bad)
}

// singleDocument gives the original bytes of the only YAML document that has content. An empty
// document (only blank lines, comments or a separator) is not content. A literal null, an empty
// map and all other scalars are content. A document end marker ("...") is not permitted.
func singleDocument(data []byte) ([]byte, error) {
	chunks, err := splitDocuments(data)
	if err != nil {
		return nil, err
	}
	// The YAML parser reads the full stream to the end, thus it finds each document.
	switch _, content, err := countDocuments(data); {
	case err != nil:
		return nil, err
	case content != 1:
		return nil, ErrDocumentCount
	}
	// The parser must find the same document in the chunk. The strict decoder gets this chunk.
	var doc []byte
	for _, chunk := range chunks {
		total, content, err := countDocuments(chunk)
		switch {
		case err != nil:
			return nil, err
		case total > 1, content > 0 && doc != nil:
			return nil, ErrDocumentCount
		case content > 0:
			doc = chunk
		}
	}
	if doc == nil {
		return nil, ErrDocumentCount
	}
	return doc, nil
}

// lineBreaks are the YAML line breaks. CRLF is one line break, thus it comes before CR.
var lineBreaks = [][]byte{[]byte("\r\n"), []byte("\r"), []byte("\n"), []byte("\xc2\x85"), []byte("\xe2\x80\xa8"), []byte("\xe2\x80\xa9")}

// breakLen gives the length of the line break at the start of b, or 0.
func breakLen(b []byte) int {
	for _, lb := range lineBreaks {
		if bytes.HasPrefix(b, lb) {
			return len(lb)
		}
	}
	return 0
}

// splitDocuments cuts the data before each line that starts with a "---" marker. The marker line
// stays in its chunk. It does not change or copy the bytes. A line that starts with "..." is an
// error. The parser ignores a byte order mark at the start, thus this function also ignores it.
func splitDocuments(data []byte) ([][]byte, error) {
	var chunks [][]byte
	start := 0
	for i := 0; ; {
		line := data[i:]
		if i == 0 {
			line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
		}
		if bytes.HasPrefix(line, []byte("...")) {
			return nil, ErrDocumentCount
		}
		marker := bytes.HasPrefix(line, []byte("---")) &&
			(len(line) == 3 || line[3] == ' ' || line[3] == '\t' || breakLen(line[3:]) > 0)
		if marker && i > start {
			chunks = append(chunks, data[start:i])
			start = i
		}
		end := i
		for end < len(data) && breakLen(data[end:]) == 0 {
			end++
		}
		if end == len(data) {
			break
		}
		i = end + breakLen(data[end:])
	}
	return append(chunks, data[start:]), nil
}

// countDocuments reads the full YAML stream to the end. It gives the number of documents and the
// number of documents with content. It does not change the data.
func countDocuments(data []byte) (total, content int, err error) {
	// The parser can panic on some input. A panic is malformed input.
	defer func() {
		if recover() != nil {
			total, content, err = 0, 0, ErrMalformed
		}
	}()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		switch derr := decoder.Decode(&doc); {
		case errors.Is(derr, io.EOF):
			return total, content, nil
		case derr != nil:
			return 0, 0, ErrMalformed
		}
		total++
		if !emptyDocument(&doc) {
			content++
		}
	}
}

// emptyDocument is true only for the empty node that the parser makes for a document without
// content. A literal null ("null" or "~") has a value, thus it is not empty.
func emptyDocument(doc *yaml.Node) bool {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return false
	}
	n := doc.Content[0]
	return n.Kind == yaml.ScalarNode && n.Style == 0 && n.Tag == "!!null" && n.Value == "" && n.Anchor == ""
}

func decodeCRD(decoder runtime.Decoder, doc []byte) (*apiextensionsv1.CustomResourceDefinition, error) {
	obj, _, err := decoder.Decode(doc, nil, nil)
	if err != nil && !runtime.IsNotRegisteredError(err) && !runtime.IsMissingKind(err) && !runtime.IsMissingVersion(err) {
		return nil, ErrMalformed
	}
	if crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok && err == nil {
		return crd, nil
	}
	return nil, ErrNotCRD
}
