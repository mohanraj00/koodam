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
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"

	ml "github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	// The markers have synthetic values. They are not grants of this project.
	statusMarker = "// +kubebuilder:rbac:groups=example.com,resources=widgets/status,verbs=update"
	coreMarker   = `// +kubebuilder:rbac:groups="",resources=gadgets,verbs=list`
	pkgClause    = "package controller\n\n"
	// funcDoc comes after a marker group. One blank line separates them.
	funcDoc = "\n\n// Reconcile does the work.\nfunc Reconcile() {}\n"
)

// packageSource gives a small Go file with the marker group in the supported placement.
func packageSource(markers ...string) []byte {
	return []byte(pkgClause + "type reconciler struct{}\n\n" + strings.Join(markers, "\n") + funcDoc)
}

// markerLines gives each line of the data that starts with the RBAC marker, in the file order.
func markerLines(data []byte) []string {
	out := make([]string, 0, 8)
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, "// +kubebuilder:rbac") {
			out = append(out, strings.TrimSuffix(line, "\n"))
		}
	}
	return out
}

func stagedText(lines ...string) string {
	return strings.Join(lines, "\n") + "\npackage rbacmarkers\n"
}

func roles(out []ml.MarkerPackage) []ml.RoleGroup {
	r := make([]ml.RoleGroup, 0, len(out))
	for _, p := range out {
		r = append(r, p.Role)
	}
	return r
}

// requireStaged examines that the Go parser finds the lines as the package documentation comment.
// The staged file has no other comment and no declaration.
func requireStaged(t *testing.T, data []byte, lines []string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "staged.go", data, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if file.Name.Name != "rbacmarkers" || file.Doc == nil || len(file.Comments) != 1 || len(file.Decls) != 0 {
		t.Fatal("the markers are not only the package documentation comment")
	}
	got := make([]string, 0, len(file.Doc.List))
	for _, c := range file.Doc.List {
		got = append(got, c.Text)
	}
	if !slices.Equal(got, lines) {
		t.Fatalf("package comment %q, want %q", got, lines)
	}
}

// The current Agent, Profile and Workspace sources give one base package. The lines are the
// original marker lines in the index order and then in the file order.
func TestExtractRBACMarkersCurrentLayout(t *testing.T) {
	in := controllerSources(t)
	all := make([]string, 0, 16)
	for _, s := range in {
		lines := markerLines(s.Data)
		if len(lines) == 0 {
			t.Fatalf("%s has no RBAC marker", s.Path)
		}
		all = append(all, lines...)
		out, err := ml.ExtractRBACMarkers([]ml.Source{s})
		if err != nil {
			t.Fatalf("%s: %v", s.Path, err)
		}
		if len(out) != 1 || out[0].Role != ml.RoleBase || string(out[0].Data) != stagedText(lines...) {
			t.Fatalf("%s: the staged package is not the original marker lines", s.Path)
		}
	}
	before := cloneSources(in)
	reversed := slices.Clone(in)
	slices.Reverse(reversed)
	for _, src := range [][]ml.Source{in, reversed} {
		out, err := ml.ExtractRBACMarkers(src)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || out[0].Role != ml.RoleBase || string(out[0].Data) != stagedText(all...) {
			t.Fatalf("roles %v: the staged package is not the original marker lines in the index order", roles(out))
		}
		requireStaged(t, out[0].Data, all)
	}
	if !sameSources(in, before) {
		t.Fatal("ExtractRBACMarkers changed the input")
	}
}

// Synthetic sources give the exact text for both roles. A file can have more than one group.
func TestExtractRBACMarkersExactText(t *testing.T) {
	twoGroups := pkgClause + "import \"context\"\n\n" + syntheticMarker + "\n\n// Reconcile does the work.\n" +
		"func Reconcile(context.Context) {}\n" + statusMarker + "\n\n// Setup adds the controller.\nfunc Setup() {}\n"
	in := []ml.Source{
		{Path: appVersionSource, Data: packageSource(syntheticMarker, coreMarker)},
		{Path: workspaceSource, Data: goSource("// Reconcile does the work.", "")},
		{Path: "internal/controller/helpers.go", Data: goSource("// +kubebuilder:object:root=true", "")},
		{Path: profileSource, Data: packageSource(coreMarker)},
		{Path: appSource, Data: packageSource(statusMarker)},
		{Path: agentSource, Data: []byte(twoGroups)},
	}
	before := cloneSources(in)
	reversed := slices.Clone(in)
	slices.Reverse(reversed)
	want := []ml.MarkerPackage{
		{Role: ml.RoleBase, Data: []byte(stagedText(syntheticMarker, statusMarker, coreMarker))},
		{Role: ml.RoleApp, Data: []byte(stagedText(statusMarker, syntheticMarker, coreMarker))},
	}
	for _, src := range [][]ml.Source{in, reversed, in} {
		out, err := ml.ExtractRBACMarkers(src)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(out, want, func(x, y ml.MarkerPackage) bool { return x.Role == y.Role && bytes.Equal(x.Data, y.Data) }) {
			t.Fatalf("roles %v: the staged text is not the exact text", roles(out))
		}
		requireStaged(t, out[0].Data, []string{syntheticMarker, statusMarker, coreMarker})
		requireStaged(t, out[1].Data, []string{statusMarker, syntheticMarker, coreMarker})
	}
	if !sameSources(in, before) {
		t.Fatal("ExtractRBACMarkers changed the input")
	}
}

// requireMarkersRejected examines that ExtractRBACMarkers gives no result, the fixed error and no
// input change. A source with a supported group comes first, thus a partial result is not empty.
func requireMarkersRejected(t *testing.T, bad []ml.Source, want error) {
	t.Helper()
	in := append([]ml.Source{{Path: agentSource, Data: packageSource(syntheticMarker)}}, bad...)
	before := cloneSources(in)
	out, err := ml.ExtractRBACMarkers(in)
	if out != nil {
		t.Fatalf("got a partial result with roles %v", roles(out))
	}
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if err.Error() != want.Error() {
		t.Fatal("the error text is not the fixed reason")
	}
	if !sameSources(in, before) {
		t.Fatal("ExtractRBACMarkers changed the input")
	}
}

func TestExtractRBACMarkersRejectsWithoutPartialResult(t *testing.T) {
	ws := func(b string) []ml.Source { return []ml.Source{{Path: workspaceSource, Data: []byte(b)}} }
	marker := func(m string) []ml.Source { return ws(pkgClause + m + funcDoc) }
	values := "groups=example.com,resources=widgets,verbs=get"
	cases := []struct {
		name string
		bad  []ml.Source
		want error
	}{
		{"unknown source", []ml.Source{markedSource(unknownSource)}, ml.ErrUnindexedSource},
		{"unknown source with supported group", []ml.Source{{Path: unknownSource, Data: packageSource(syntheticMarker)}}, ml.ErrUnindexedSource},
		{"unsafe path", []ml.Source{{Path: "../" + workspaceSource, Data: packageSource(syntheticMarker)}}, ml.ErrUnsafeSourcePath},
		{"malformed source", ws(pkgClause + "func (\n"), ml.ErrMalformedSource},
		{"malformed source with supported group", ws(pkgClause + syntheticMarker + funcDoc + "func (\n"), ml.ErrMalformedSource},
		{"function doc", ws(string(goSource(syntheticMarker, ""))), ml.ErrUnsupportedMarker},
		{"function doc after text", ws(string(goSource("// Reconcile does the work.\n//\n"+syntheticMarker, ""))), ml.ErrUnsupportedMarker},
		{"function body", ws(string(goSource("// Reconcile does the work.", "\t"+syntheticMarker))), ml.ErrUnsupportedMarker},
		{"function body after supported group", ws(pkgClause + syntheticMarker + "\n\n// Reconcile does the work.\nfunc Reconcile() {\n\t" + syntheticMarker + "\n}\n"), ml.ErrUnsupportedMarker},
		{"supported group and function doc", ws(pkgClause + syntheticMarker + funcDoc + "\n" + syntheticMarker + "\nfunc Setup() {}\n"), ml.ErrUnsupportedMarker},
		{"type doc", ws(pkgClause + syntheticMarker + "\ntype reconciler struct{}\n"), ml.ErrUnsupportedMarker},
		{"group before type doc", ws(pkgClause + syntheticMarker + "\n\n// reconciler does the work.\ntype reconciler struct{}\n"), ml.ErrUnsupportedMarker},
		{"field", ws(pkgClause + "type reconciler struct {\n\t" + syntheticMarker + "\n\tName string\n}\n"), ml.ErrUnsupportedMarker},
		{"const block", ws(pkgClause + "const (\n\t" + syntheticMarker + "\n\n\t// a is one.\n\ta = 1\n)\n"), ml.ErrUnsupportedMarker},
		{"package doc", ws(syntheticMarker + "\n" + pkgClause + "// Reconcile does the work.\nfunc Reconcile() {}\n"), ml.ErrUnsupportedMarker},
		{"inline after declaration", ws(pkgClause + "type reconciler struct{} " + syntheticMarker + funcDoc), ml.ErrUnsupportedMarker},
		{"inline after function", ws(pkgClause + "func setup() {} " + syntheticMarker + funcDoc), ml.ErrUnsupportedMarker},
		{"inline in code", ws(pkgClause + "var x = 1 " + syntheticMarker + "\n"), ml.ErrUnsupportedMarker},
		{"inline after block comment", ws(pkgClause + "/* text */ " + syntheticMarker + funcDoc), ml.ErrUnsupportedMarker},
		{"two blank lines", ws(pkgClause + syntheticMarker + "\n" + funcDoc), ml.ErrUnsupportedMarker},
		{"no function doc", ws(pkgClause + syntheticMarker + "\n\nfunc Reconcile() {}\n"), ml.ErrUnsupportedMarker},
		{"end of file", ws(pkgClause + syntheticMarker + "\n"), ml.ErrUnsupportedMarker},
		{"group with text", marker("// Text.\n" + syntheticMarker), ml.ErrUnsupportedMarker},
		{"group with directive", marker("//go:generate echo\n" + syntheticMarker), ml.ErrUnsupportedMarker},
		{"group with block comment", marker("/* text */\n" + syntheticMarker), ml.ErrUnsupportedMarker},
		{"indented", marker(" " + syntheticMarker), ml.ErrUnsupportedMarker},
		{"no space", marker("//+kubebuilder:rbac:" + values), ml.ErrUnsupportedMarker},
		{"two spaces", marker("//  +kubebuilder:rbac:" + values), ml.ErrUnsupportedMarker},
		{"tab before plus", marker("//\t+kubebuilder:rbac:" + values), ml.ErrUnsupportedMarker},
		{"bare name", marker("// +kubebuilder:rbac"), ml.ErrUnsupportedMarker},
		{"empty arguments", marker("// +kubebuilder:rbac:"), ml.ErrUnsupportedMarker},
		{"equals after name", marker("// +kubebuilder:rbac=x"), ml.ErrUnsupportedMarker},
		{"trailing space", marker(syntheticMarker + " "), ml.ErrUnsupportedMarker},
		{"space in arguments", marker("// +kubebuilder:rbac:groups=example.com, resources=widgets,verbs=get"), ml.ErrUnsupportedMarker},
		{"tab in arguments", marker("// +kubebuilder:rbac:groups=example.com,\tresources=widgets,verbs=get"), ml.ErrUnsupportedMarker},
		{"non-ASCII", marker(syntheticMarker + "é"), ml.ErrUnsupportedMarker},
		{"CRLF", ws(strings.ReplaceAll(pkgClause+syntheticMarker+funcDoc, "\n", "\r\n")), ml.ErrUnsupportedMarker},
		{"go:build line", ws("//go:build linux\n\n" + pkgClause + syntheticMarker + funcDoc), ml.ErrUnsupportedContext},
		{"go:build ignore", ws("//go:build ignore\n\n" + pkgClause + syntheticMarker + funcDoc), ml.ErrUnsupportedContext},
		{"plus build line", ws("// +build linux\n\n" + pkgClause + syntheticMarker + funcDoc), ml.ErrUnsupportedContext},
		{"generated", ws("// Code generated by hand. DO NOT EDIT.\n\n" + pkgClause + syntheticMarker + funcDoc), ml.ErrUnsupportedContext},
		{"cgo", ws(pkgClause + "import \"C\"\n\n" + syntheticMarker + funcDoc), ml.ErrUnsupportedContext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireMarkersRejected(t, tc.bad, tc.want)
		})
	}
}

// If one current source has its markers in the function doc, the other current sources give no
// partial result.
func TestExtractRBACMarkersMixedCurrentLayout(t *testing.T) {
	for i := range 3 {
		in := controllerSources(t)
		t.Run(in[i].Path, func(t *testing.T) {
			data := in[i].Data
			last := bytes.LastIndex(data, []byte("// +kubebuilder:rbac"))
			gap := bytes.Index(data[max(last, 0):], []byte("\n\n"))
			if last < 0 || gap < 0 {
				t.Fatal("the source has no marker group with a blank line after it")
			}
			// Remove the blank line, thus the markers become part of the function doc.
			cut := last + gap
			in[i].Data = slices.Concat(data[:cut], data[cut+1:])
			before := cloneSources(in)
			out, err := ml.ExtractRBACMarkers(in)
			if out != nil || !errors.Is(err, ml.ErrUnsupportedMarker) {
				t.Fatalf("roles %v, err %v, want no result and %v", roles(out), err, ml.ErrUnsupportedMarker)
			}
			if !sameSources(in, before) {
				t.Fatal("ExtractRBACMarkers changed the input")
			}
		})
	}
}

// Valid sources without a marker give an empty result, not a failure.
func TestExtractRBACMarkersEmptyResult(t *testing.T) {
	inputs := map[string][]ml.Source{
		"no input": nil,
		"no marker": {
			{Path: appSource, Data: goSource("// App reconciles later.", "")},
			{Path: agentSource, Data: goSource("", "")},
			{Path: "internal/controller/doc.go", Data: goSource("/* +kubebuilder:rbac:groups=example.com */", "")},
		},
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			out, err := ml.ExtractRBACMarkers(in)
			if err != nil || out == nil || len(out) != 0 {
				t.Fatalf("roles %v, err %v, want an empty result", roles(out), err)
			}
		})
	}
}
