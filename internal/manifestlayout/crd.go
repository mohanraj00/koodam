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
	"bufio"
	"bytes"
	"errors"
	"io"
	"path"
	"slices"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	serjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
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

// singleDocument gives the only YAML document that has content. Blank lines, comments and
// separators are not content. A document end marker ("...") is not permitted.
func singleDocument(data []byte) ([]byte, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var doc []byte
	for {
		chunk, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, ErrMalformed
		}
		switch content, err := hasContent(chunk); {
		case err != nil, content && doc != nil:
			return nil, ErrDocumentCount
		case content:
			doc = chunk
		}
	}
	if doc == nil {
		return nil, ErrDocumentCount
	}
	return doc, nil
}

// hasContent examines all lines of the chunk, thus it finds a document end marker after content.
func hasContent(doc []byte) (content bool, err error) {
	for line := range bytes.Lines(doc) {
		trimmed := bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(line, []byte("...")):
			return false, ErrDocumentCount
		case len(trimmed) > 0 && trimmed[0] != '#' && !bytes.HasPrefix(line, []byte("---")):
			content = true
		}
	}
	return content, nil
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
