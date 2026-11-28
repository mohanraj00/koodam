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

package v1beta1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// signature returns one line for each types file, type, field and constant of an API package. A
// line has the name, the Go type, the struct tag and the markers. It does not have the description,
// because a description has no effect on the API server.
//
// controller-gen makes the CRD schema from these parts only. Thus two packages with the same
// signature have the same schema, the same CEL rules and the same printer columns.
func signature(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*_types.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found no types file in %s", dir)
	}
	var lines []string
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		// markers returns the marker lines of the comments between two positions. A marker of a
		// kind can be in a comment group that an empty line separates from the type.
		markers := func(from, to token.Pos) string {
			var found []string
			for _, group := range file.Comments {
				if group.Pos() < from || group.End() > to {
					continue
				}
				for _, comment := range group.List {
					if text := strings.TrimPrefix(comment.Text, "// "); strings.HasPrefix(text, "+") {
						found = append(found, text)
					}
				}
			}
			return strings.Join(found, " | ")
		}
		lines = append(lines, "file "+filepath.Base(path))
		last := file.Name.End()
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok == token.IMPORT {
				last = decl.End()
				continue
			}
			for _, spec := range gen.Specs {
				switch spec := spec.(type) {
				case *ast.ValueSpec:
					for i, name := range spec.Names {
						line := "const " + name.Name
						if spec.Type != nil {
							line += " " + types.ExprString(spec.Type)
						}
						if i < len(spec.Values) {
							line += " = " + types.ExprString(spec.Values[i])
						}
						lines = append(lines, line)
					}
				case *ast.TypeSpec:
					fields, isStruct := spec.Type.(*ast.StructType)
					if !isStruct {
						lines = append(lines, "type "+spec.Name.Name+" "+types.ExprString(spec.Type)+" | "+markers(last, spec.Pos()))
						continue
					}
					lines = append(lines, "type "+spec.Name.Name+" struct | "+markers(last, spec.Pos()))
					from := fields.Fields.Opening
					for _, field := range fields.Fields.List {
						name := "(embedded)"
						if len(field.Names) > 0 {
							name = field.Names[0].Name
						}
						tag := ""
						if field.Tag != nil {
							tag = field.Tag.Value
						}
						lines = append(lines, "field "+spec.Name.Name+"."+name+" "+types.ExprString(field.Type)+" "+tag+
							" | "+markers(from, field.Pos()))
						from = field.End()
					}
				}
			}
			last = decl.End()
		}
	}
	return lines
}

// change replaces from with to in the one line that starts with prefix. An empty to with an empty
// from removes the line.
func change(t *testing.T, lines []string, prefix, from, to string) []string {
	t.Helper()
	found := -1
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			if found >= 0 {
				t.Fatalf("more than one line starts with %q", prefix)
			}
			found = i
		}
	}
	if found < 0 || !strings.Contains(lines[found], from) {
		t.Fatalf("v1alpha1 has no line %q with %q", prefix, from)
	}
	if from == "" && to == "" {
		return slices.Delete(lines, found, found+1)
	}
	lines[found] = strings.Replace(lines[found], from, to, 1)
	return lines
}

// TestOnlyTheKnownChangesFromV1alpha1 makes sure that v1beta1 is v1alpha1 with the changes of #300
// and no other change. While the API server serves the two versions:
//
//   - A kind with no change must have the same schema in the two versions, because the API server
//     converts it with no webhook.
//   - Each CEL rule must be in the two versions, because the API server uses only the rules of the
//     version of a request.
//
// If you change a type, a field or a marker, make the change in the two packages. If the change
// is only in v1beta1, it also needs a conversion, and a line in this test.
func TestOnlyTheKnownChangesFromV1alpha1(t *testing.T) {
	want := signature(t, "../v1alpha1")
	// TestKey.spec.testID has the name spec.testRunID.
	want = change(t, want, "field TestKeySpec.TestID ", "TestKeySpec.TestID string `json:\"testID\"`",
		"TestKeySpec.TestRunID string `json:\"testRunID\"`")
	want = change(t, want, "field TestKey.Spec ", "self.testID == oldSelf.testID", "self.testRunID == oldSelf.testRunID")
	want = change(t, want, "type TestKey struct ", "JSONPath=`.spec.testID`", "JSONPath=`.spec.testRunID`")
	// The plugins of a harness are a set of at most 8 preset names, with no list of the names.
	want = change(t, want, "field HarnessSpec.Plugins ", "+kubebuilder:validation:MaxItems=1", "+kubebuilder:validation:MaxItems=8")
	want = change(t, want, "type HarnessPlugin string ", "+kubebuilder:validation:Enum=nookku",
		"+kubebuilder:validation:MaxLength=63 | +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`")
	want = change(t, want, "const HarnessPluginNookku ", "", "")

	got := signature(t, ".")
	for i := range max(len(got), len(want)) {
		if i >= len(got) || i >= len(want) || got[i] != want[i] {
			t.Fatalf("v1beta1 has a change that this test does not know.\nwant: %s\ngot:  %s",
				strings.Join(want[min(i, len(want)):min(i+1, len(want))], ""),
				strings.Join(got[min(i, len(got)):min(i+1, len(got))], ""))
		}
	}
}
