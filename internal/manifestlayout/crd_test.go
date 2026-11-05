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
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ml "github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	group        = "core.koodam.dev"
	appKind      = "App"
	appPlural    = "apps"
	appsFile     = "core.koodam.dev_apps.yaml"
	versionsFile = "core.koodam.dev_appversions.yaml"
	appDir       = "config/apps/crd/bases/"
)

var baseDir = filepath.Join("..", "..", "config", "crd", "bases")

// syntheticCRD gives a small CRD with a comment. The comment must stay in the output.
func syntheticCRD(kind, plural, name, crdGroup string) []byte {
	return fmt.Appendf(nil, `---
# Synthetic %s CRD for the routing test.
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: %s}
spec:
  group: %s
  names: {kind: %s, listKind: %sList, plural: %s, singular: %s}
  scope: Namespaced
  versions: [{name: v1alpha1, served: true, storage: true, schema: {openAPIV3Schema: {type: object}}}]
`, kind, name, crdGroup, kind, kind, plural, strings.ToLower(kind))
}

func appData() []byte { return syntheticCRD(appKind, appPlural, appPlural+"."+group, group) }

// crdArtifact gives a valid synthetic CRD with the controller-gen file name of the kind.
func crdArtifact(kind, plural string) ml.Artifact {
	return ml.Artifact{Name: group + "_" + plural + ".yaml", Data: syntheticCRD(kind, plural, plural+"."+group, group)}
}

// currentCRDs reads the CRDs that are in config/crd/bases now.
func currentCRDs(t *testing.T) []ml.Artifact {
	t.Helper()
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]ml.Artifact, 0, len(entries))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(baseDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ml.Artifact{Name: e.Name(), Data: data})
	}
	return out
}

func clone(in []ml.Artifact) []ml.Artifact {
	out := make([]ml.Artifact, len(in))
	for i, a := range in {
		out[i] = ml.Artifact{Name: a.Name, Data: bytes.Clone(a.Data)}
	}
	return out
}

func sameArtifacts(a, b []ml.Artifact) bool {
	return slices.EqualFunc(a, b, func(x, y ml.Artifact) bool { return x.Name == y.Name && bytes.Equal(x.Data, y.Data) })
}

func paths(plan []ml.Placement) []string {
	out := make([]string, 0, len(plan))
	for _, p := range plan {
		out = append(out, p.Path)
	}
	return out
}

// The current base CRDs keep their paths and bytes. The App kinds go only to the App output.
func TestRouteMixedInput(t *testing.T) {
	in := append(currentCRDs(t), crdArtifact(appKind, appPlural), crdArtifact("AppVersion", "appversions"))
	before := clone(in)
	reversed := slices.Clone(in)
	slices.Reverse(reversed)
	a, err := ml.RouteCRDs(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ml.RouteCRDs(reversed)
	if err != nil {
		t.Fatal(err)
	}
	basePlurals := []string{"agents", "profiles", "testkeys", "workspaces"}
	want := make([]string, 0, 2+len(basePlurals))
	want = append(want, appDir+appsFile, appDir+versionsFile)
	for _, plural := range basePlurals {
		want = append(want, "config/crd/bases/"+group+"_"+plural+".yaml")
	}
	if got := paths(a); !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if !slices.EqualFunc(a, b, func(x, y ml.Placement) bool { return x.Path == y.Path && bytes.Equal(x.Data, y.Data) }) {
		t.Fatal("the plan changes with the input order")
	}
	for _, p := range a {
		i := slices.IndexFunc(in, func(x ml.Artifact) bool { return x.Name == path.Base(p.Path) })
		if i < 0 || !bytes.Equal(p.Data, in[i].Data) {
			t.Errorf("%s: bytes differ from the artifact", p.Path)
		}
		// The plan must not share memory with the input.
		p.Data[0] ^= 0xff
	}
	if !sameArtifacts(in, before) {
		t.Fatal("RouteCRDs changed the input")
	}
}

func TestRouteRejectsWithoutPartialPlan(t *testing.T) {
	data := func(b []byte) ml.Artifact { return ml.Artifact{Name: appsFile, Data: b} }
	named := func(name string) ml.Artifact { return ml.Artifact{Name: name, Data: appData()} }
	appWith := func(old, repl string) ml.Artifact {
		return data([]byte(strings.Replace(string(appData()), old, repl, 1)))
	}
	cases := []struct {
		name string
		bad  []ml.Artifact
		want error
	}{
		{"unknown kind", []ml.Artifact{crdArtifact("Widget", "widgets")}, ml.ErrUnknownKind},
		{"malformed YAML", []ml.Artifact{data([]byte("apiVersion: [\n"))}, ml.ErrMalformed},
		{"duplicate top field", []ml.Artifact{data(append(appData(), "kind: CustomResourceDefinition\n"...))}, ml.ErrMalformed},
		{"duplicate nested field", []ml.Artifact{appWith("  group: ", "  group: x\n  group: ")}, ml.ErrMalformed},
		{"unknown field", []ml.Artifact{data(append(appData(), "extra: true\n"...))}, ml.ErrMalformed},
		{"field with wrong case", []ml.Artifact{appWith("spec:", "Spec:")}, ml.ErrMalformed},
		{"two documents", []ml.Artifact{data(append(appData(), appData()...))}, ml.ErrDocumentCount},
		{"document end marker", []ml.Artifact{data(append(appData(), "...\n"...))}, ml.ErrDocumentCount},
		{"comment only", []ml.Artifact{data([]byte("---\n# empty\n---\n"))}, ml.ErrDocumentCount},
		{"no data", []ml.Artifact{data(nil)}, ml.ErrDocumentCount},
		{"ConfigMap", []ml.Artifact{data([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: apps\n"))}, ml.ErrNotCRD},
		{"v1beta1 CRD", []ml.Artifact{appWith("apiextensions.k8s.io/v1\n", "apiextensions.k8s.io/v1beta1\n")}, ml.ErrNotCRD},
		{"CRD list", []ml.Artifact{data([]byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinitionList\nitems: []\n"))}, ml.ErrNotCRD},
		{"no kind", []ml.Artifact{appWith("kind: CustomResourceDefinition\n", "")}, ml.ErrNotCRD},
		{"wrong group", []ml.Artifact{data(syntheticCRD(appKind, appPlural, appPlural+"."+group, "apps.koodam.dev"))}, ml.ErrWrongGroup},
		{"wrong metadata name", []ml.Artifact{data(syntheticCRD(appKind, appPlural, "apps.example.com", group))}, ml.ErrWrongIdentity},
		{"wrong plural", []ml.Artifact{data(syntheticCRD(appKind, "applications", appPlural+"."+group, group))}, ml.ErrWrongIdentity},
		{"file name of other kind", []ml.Artifact{named(versionsFile)}, ml.ErrNameMismatch},
		{"short file name", []ml.Artifact{named("apps.yaml")}, ml.ErrNameMismatch},
		{"parent path", []ml.Artifact{named("../" + appsFile)}, ml.ErrUnsafeName},
		{"directory path", []ml.Artifact{named(appDir + appsFile)}, ml.ErrUnsafeName},
		{"absolute path", []ml.Artifact{named("/" + appsFile)}, ml.ErrUnsafeName},
		{"backslash", []ml.Artifact{named(`..\` + appsFile)}, ml.ErrUnsafeName},
		{"hidden file", []ml.Artifact{named("." + appsFile)}, ml.ErrUnsafeName},
		{"NUL", []ml.Artifact{named(appsFile + "\x00")}, ml.ErrUnsafeName},
		{"empty name", []ml.Artifact{named("")}, ml.ErrUnsafeName},
		{"duplicate kind", []ml.Artifact{crdArtifact(appKind, appPlural), crdArtifact(appKind, appPlural)}, ml.ErrDuplicate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A valid artifact comes first, thus a partial plan is not empty.
			in := append([]ml.Artifact{crdArtifact("AppVersion", "appversions")}, tc.bad...)
			before := clone(in)
			plan, err := ml.RouteCRDs(in)
			if plan != nil {
				t.Fatalf("got a partial plan %v", paths(plan))
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err.Error() != tc.want.Error() {
				t.Fatalf("error text %q is not the fixed reason", err)
			}
			if !sameArtifacts(in, before) {
				t.Fatal("RouteCRDs changed the input")
			}
		})
	}
}

// lineEndings are the YAML line breaks. The mixed entry uses each break in turn.
var lineEndings = []struct {
	name   string
	breaks []string
}{
	{"LF", []string{"\n"}},
	{"CRLF", []string{"\r\n"}},
	{"CR", []string{"\r"}},
	{"NEL", []string{"\xc2\x85"}},
	{"LS", []string{"\xe2\x80\xa8"}},
	{"PS", []string{"\xe2\x80\xa9"}},
	{"mixed", []string{"\r\n", "\r", "\n", "\xc2\x85", "\xe2\x80\xa8", "\xe2\x80\xa9"}},
}

// bareApp gives the App CRD without the initial separator and comment.
func bareApp() string { return strings.SplitN(string(appData()), "\n", 3)[2] }

// withBreaks puts the breaks in turn in place of each LF.
func withBreaks(s string, breaks []string) []byte {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteString(breaks[(i-1)%len(breaks)])
		}
		b.WriteString(line)
	}
	return []byte(b.String())
}

// requireRejected examines that RouteCRDs gives no plan, the fixed error and no input change.
func requireRejected(t *testing.T, data []byte, want error) {
	t.Helper()
	in := []ml.Artifact{crdArtifact("AppVersion", "appversions"), {Name: appsFile, Data: data}}
	before := clone(in)
	plan, err := ml.RouteCRDs(in)
	if plan != nil {
		t.Fatalf("got a partial plan %v", paths(plan))
	}
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if err.Error() != want.Error() {
		t.Fatalf("error text %q is not the fixed reason", err)
	}
	if !sameArtifacts(in, before) {
		t.Fatal("RouteCRDs changed the input")
	}
}

// One CRD with empty documents, comments and separators keeps its original bytes.
func TestRouteKeepsBytesForEachLineBreak(t *testing.T) {
	inputs := []struct{ name, doc string }{
		{"no separator", bareApp()},
		{"initial separator and comment", string(appData())},
		{"leading comments and separators", "# head\n\n---\n# empty\n---\n" + bareApp() + "---\n# tail\n"},
	}
	for _, e := range lineEndings {
		for _, tc := range inputs {
			t.Run(e.name+" "+tc.name, func(t *testing.T) {
				data := withBreaks(tc.doc, e.breaks)
				before := bytes.Clone(data)
				plan, err := ml.RouteCRDs([]ml.Artifact{{Name: appsFile, Data: data}})
				if err != nil {
					t.Fatal(err)
				}
				if len(plan) != 1 || plan[0].Path != appDir+appsFile || !bytes.Equal(plan[0].Data, before) {
					t.Fatalf("plan %v does not keep the original bytes", paths(plan))
				}
				// The plan must not share memory with the input.
				plan[0].Data[0] ^= 0xff
				if !bytes.Equal(data, before) {
					t.Fatal("the plan shares memory with the input")
				}
			})
		}
	}
}

// Each document with content counts. An extra document is rejected with each line break.
func TestRouteRejectsExtraDocumentForEachLineBreak(t *testing.T) {
	extras := []struct{ name, doc string }{
		{"ConfigMap", "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: unexpected}\n"},
		{"null", "null\n"},
		{"tilde null", "~\n"},
		{"empty map", "{}\n"},
		{"empty list", "[]\n"},
		{"empty quoted string", "''\n"},
		{"scalar", "x\n"},
		{"second CRD", bareApp()},
	}
	for _, e := range lineEndings {
		for _, x := range extras {
			t.Run(e.name+" "+x.name+" after", func(t *testing.T) {
				requireRejected(t, withBreaks(bareApp()+"---\n"+x.doc, e.breaks), ml.ErrDocumentCount)
			})
			t.Run(e.name+" "+x.name+" before", func(t *testing.T) {
				requireRejected(t, withBreaks(x.doc+"---\n"+bareApp(), e.breaks), ml.ErrDocumentCount)
			})
		}
		t.Run(e.name+" null on separator line", func(t *testing.T) {
			requireRejected(t, withBreaks(bareApp()+"--- null\n", e.breaks), ml.ErrDocumentCount)
		})
		t.Run(e.name+" document end marker", func(t *testing.T) {
			requireRejected(t, withBreaks(bareApp()+"...\n", e.breaks), ml.ErrDocumentCount)
		})
	}
}

// The reproduced case: CR line breaks, no initial separator and a ConfigMap after the CRD.
func TestRouteRejectsReproducedCRCase(t *testing.T) {
	extra := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: unexpected}\n"
	requireRejected(t, []byte(strings.ReplaceAll(bareApp()+"\n---\n"+extra, "\n", "\r")), ml.ErrDocumentCount)
}

// requireRouted examines that RouteCRDs gives the App path and the original bytes.
func requireRouted(t *testing.T, data []byte) {
	t.Helper()
	before := bytes.Clone(data)
	plan, err := ml.RouteCRDs([]ml.Artifact{{Name: appsFile, Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Path != appDir+appsFile || !bytes.Equal(plan[0].Data, before) || !bytes.Equal(data, before) {
		t.Fatalf("plan %v does not keep the original bytes", paths(plan))
	}
}

// The parser makes an empty node for a bare tag ("!"). The tag is content with each line break.
func TestRouteRejectsBareTagForEachLineBreak(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"after on separator line", bareApp() + "--- !\n"},
		{"before on separator line", "--- !\n---\n" + bareApp()},
		{"after on own line", bareApp() + "---\n!\n"},
		{"before on own line", "---\n!\n---\n" + bareApp()},
		{"before without separator", "!\n---\n" + bareApp()},
		{"after with comment", bareApp() + "--- ! # tag\n"},
		{"after at end of data", bareApp() + "--- !"},
		{"between empty documents", "---\n# empty\n--- !\n---\n" + bareApp() + "---\n"},
		{"exact reproduced after", string(appData()) + "--- !\n"},
		{"exact reproduced before", "--- !\n" + string(appData())},
	}
	for _, e := range lineEndings {
		for _, tc := range cases {
			t.Run(e.name+" "+tc.name, func(t *testing.T) {
				requireRejected(t, withBreaks(tc.doc, e.breaks), ml.ErrDocumentCount)
			})
		}
		// A "!" in a comment is not a tag.
		t.Run(e.name+" tag text in comments", func(t *testing.T) {
			requireRouted(t, withBreaks("--- # !\n  # !\n---\n"+bareApp()+"--- # !\n", e.breaks))
		})
		// A "!" on the separator line of the CRD is a legal non-specific tag of the CRD mapping.
		t.Run(e.name+" tag on CRD mapping", func(t *testing.T) {
			requireRouted(t, withBreaks("--- !\n"+bareApp(), e.breaks))
		})
	}
}

// withSchema gives the App CRD with an entries property that has the schema s.
func withSchema(s string) []byte {
	return bytes.Replace(appData(), []byte("openAPIV3Schema: {type: object}"),
		[]byte("openAPIV3Schema: {type: object, properties: {entries: "+s+"}}"), 1)
}

// The custom decoders of the schema union fields do not reject a wrong value type, an unknown
// field or a field with the wrong case. The check must find them at each depth.
func TestRouteRejectsInvalidSchemaUnion(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"exact reproduced scalar items", `{type: array, items: 7}`},
		{"string items", `{type: array, items: x}`},
		{"bool items", `{type: array, items: true}`},
		{"scalar in items list", `{type: array, items: [{type: string}, 7]}`},
		{"unknown field in items", `{type: array, items: {type: string, bogus: 1}}`},
		{"wrong case in items", `{type: array, items: {Type: string}}`},
		{"duplicate field in items", `{type: array, items: {type: string, type: integer}}`},
		{"unknown field in items list", `{type: array, items: [{type: string, bogus: 1}]}`},
		{"nested scalar items", `{type: array, items: {type: array, items: 7}}`},
		{"scalar additionalProperties", `{type: object, additionalProperties: 7}`},
		{"unknown field in additionalProperties", `{type: object, additionalProperties: {type: string, bogus: 1}}`},
		{"wrong case in additionalProperties", `{type: object, additionalProperties: {Type: string}}`},
		{"scalar items in additionalProperties", `{type: object, additionalProperties: {type: array, items: 7}}`},
		{"string additionalItems", `{type: array, additionalItems: x}`},
		{"unknown field in additionalItems", `{type: array, additionalItems: {bogus: 1}}`},
		{"scalar dependencies", `{type: object, dependencies: {a: 7}}`},
		{"string dependencies", `{type: object, dependencies: {a: x}}`},
		{"bool dependencies", `{type: object, dependencies: {a: true}}`},
		{"number in dependencies list", `{type: object, dependencies: {a: [7]}}`},
		{"unknown field in dependencies", `{type: object, dependencies: {a: {bogus: 1}}}`},
		{"scalar items in dependencies", `{type: object, dependencies: {a: {type: array, items: 7}}}`},
		{"scalar items in allOf", `{allOf: [{items: 7}]}`},
		{"scalar items in not", `{not: {items: 7}}`},
		{"scalar items in definitions", `{definitions: {d: {items: 7}}}`},
		{"scalar items in patternProperties", `{type: object, patternProperties: {"^a": {items: 7}}}`},
		{"unknown rule field in items", `{type: array, items: {type: string, x-kubernetes-validations: [{rule: has(self.a), bogus: 1}]}}`},
		{"unknown externalDocs field in items", `{type: array, items: {externalDocs: {bogus: x}}}`},
		{"deep recursive union", `{type: array, items: {items: {additionalProperties: {items: [{dependencies: {a: {additionalItems: {items: 7}}}}]}}}}`},
		{"deep recursive unknown field", `{type: array, items: {items: {additionalProperties: {items: [{dependencies: {a: {additionalItems: {bogus: 1}}}}]}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireRejected(t, withSchema(tc.schema), ml.ErrMalformed)
		})
	}
}

// Each legal union variant and each raw JSON value keeps its original bytes.
func TestRouteAcceptsSchemaUnionVariants(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"schema items", `{type: array, items: {type: string}}`},
		{"items list", `{type: array, items: [{type: string}, {type: integer}]}`},
		{"bool additionalProperties", `{type: object, additionalProperties: false}`},
		{"schema additionalProperties", `{type: object, additionalProperties: {type: array, items: {type: string}}}`},
		{"bool additionalItems", `{type: array, items: [{type: string}], additionalItems: true}`},
		{"schema additionalItems", `{type: array, items: [{type: string}], additionalItems: {type: integer}}`},
		{"property list dependencies", `{type: object, dependencies: {a: [b, c]}}`},
		{"schema dependencies", `{type: object, dependencies: {a: {required: [b], properties: {b: {items: {type: string}}}}}}`},
		{"raw values in items", `{type: array, items: {type: object, default: {bogus: 7, Type: [x]}, example: [1, x], enum: [{a: 1}, 7, x]}}`},
		{"rules and docs in items", `{type: array, items: {type: string, externalDocs: {description: d}, x-kubernetes-validations: [{rule: has(self.a), message: m, reason: FieldValueInvalid}]}}`},
		{"deep recursive union", `{type: array, items: {items: {additionalProperties: {items: [{dependencies: {a: {additionalItems: {items: {type: string}}}}}]}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireRouted(t, withSchema(tc.schema))
		})
	}
}

// The converter decodes YAML keys with the YAML 1.1 rules and changes each key to a JSON string.
// Two different keys that become one JSON key must be rejected at each depth.
func TestRouteRejectsJSONKeyCollision(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"exact reproduced property keys", `{type: object, properties: {1: {type: string}, "1": {type: integer}}}`},
		{"same values", `{type: object, properties: {1: {type: string}, "1": {type: string}}}`},
		{"bool and string", `{type: object, properties: {true: {type: string}, "true": {type: integer}}}`},
		{"YAML 1.1 bool and string", `{type: object, properties: {yes: {type: string}, "true": {type: integer}}}`},
		{"float and integer", `{type: object, properties: {1.0: {type: string}, 1: {type: integer}}}`},
		{"hexadecimal and string", `{type: object, properties: {0x1: {type: string}, "1": {type: integer}}}`},
		{"underscore integer and string", `{type: object, properties: {1_000: {type: string}, "1000": {type: integer}}}`},
		{"infinity and string", `{type: object, properties: {.inf: {type: string}, ".inf": {type: integer}}}`},
		{"in items", `{type: array, items: {type: object, properties: {1: {type: string}, "1": {type: integer}}}}`},
		{"in dependencies", `{type: object, dependencies: {1: [a], "1": [b]}}`},
		{"raw default", `{type: object, default: {1: a, "1": b}}`},
		{"raw example", `{type: object, example: {true: a, "true": b}}`},
		{"raw enum", `{type: object, enum: [{1: a, "1": b}]}`},
		{"deep raw value", `{type: object, default: {a: [{b: {1: x, "1": y}}]}}`},
		{"inline merge and key", `{type: object, default: {<<: {1: a}, "1": b}}`},
		{"alias merge and key", `{type: object, example: &e {1: a}, default: {<<: *e, "1": b}}`},
		{"alias list merge and key", `{type: object, example: [&e {1: a}], default: {<<: [*e], "1": b}}`},
		{"two merged maps", `{type: object, default: {<<: [{1: a}, {"1": b}]}}`},
		{"alias of colliding map", `{type: object, example: &e {1: a, "1": b}, default: *e}`},
		{"merge key overlaps key", `{type: object, default: {<<: {a: 1}, a: 2}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireRejected(t, withSchema(tc.schema), ml.ErrMalformed)
		})
	}
}

// Unique keys, keys that the converter changes to unique strings and merge keys keep their bytes.
func TestRouteAcceptsUniqueAndCoercedKeys(t *testing.T) {
	cases := []struct{ name, schema string }{
		{"numeric bool and string", `{type: object, properties: {1: {type: string}, "2": {type: string}, true: {type: string}, "false": {type: string}}}`},
		{"YAML 1.1 coercion", `{type: object, properties: {yes: {type: string}, no: {type: string}, 0x10: {type: string}, 1_000: {type: string}, 1.5: {type: string}}}`},
		{"same key in two maps", `{type: object, properties: {a: {properties: {1: {type: string}}}, b: {properties: {"1": {type: string}}}}}`},
		{"raw values", `{type: object, default: {1: a, "2": b, true: c}, example: {1.5: x}, enum: [{1: a}, {"1": a}]}`},
		{"quoted merge text is a key", `{type: object, default: {"<<": {a: 1}, a: 2}}`},
		{"inline merge", `{type: object, default: {<<: {a: 1}, b: 2}}`},
		{"alias merge", `{type: object, example: &e {a: 1}, default: {<<: *e, b: 2}}`},
		{"alias list merge", `{type: object, example: [&e {a: 1}, &f {1: c}], default: {<<: [*e, *f], b: 2}}`},
		{"nested merges", `{type: object, example: &e {a: 1}, default: &f {<<: *e, b: 2}, enum: [{<<: *f, c: 3}]}`},
		{"alias reuse", `{type: object, properties: {a: &s {type: string}, b: *s}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireRouted(t, withSchema(tc.schema))
		})
	}
}

// withMetadata gives the App CRD with block-style metadata. The extra lines come after the name.
func withMetadata(extra string) string {
	return strings.Replace(string(appData()), "metadata: {name: "+appPlural+"."+group+"}\n",
		"metadata:\n  name: "+appPlural+"."+group+"\n"+extra, 1)
}

// The key collision check works with each line break, in flow and block style.
func TestRouteJSONKeyCollisionForEachLineBreak(t *testing.T) {
	rejected := []struct{ name, doc string }{
		{"exact reproduced", string(withSchema(`{type: object, properties: {1: {type: string}, "1": {type: integer}}}`))},
		{"block labels", withMetadata("  labels:\n    1: a\n    \"1\": b\n")},
		{"block YAML 1.1 labels", withMetadata("  labels:\n    on: a\n    'true': b\n")},
		{"block merge", withMetadata("  annotations: &l\n    1: a\n  labels:\n    <<: *l\n    \"1\": b\n")},
	}
	routed := []struct{ name, doc string }{
		{"unique block labels", withMetadata("  labels:\n    1: a\n    \"2\": b\n    yes: c\n")},
		{"block merge", withMetadata("  annotations: &l\n    a: x\n  labels:\n    <<: *l\n    b: c\n")},
	}
	for _, e := range lineEndings {
		for _, tc := range rejected {
			t.Run(e.name+" rejects "+tc.name, func(t *testing.T) {
				requireRejected(t, withBreaks(tc.doc, e.breaks), ml.ErrMalformed)
			})
		}
		for _, tc := range routed {
			t.Run(e.name+" routes "+tc.name, func(t *testing.T) {
				requireRouted(t, withBreaks(tc.doc, e.breaks))
			})
		}
	}
}

// The YAML 1.1 decoder of the converter gives the merge rule. A merge key with the non-specific
// tag ("!") or the merge tag is a merge, also when it is quoted. A key with the string tag or
// without a tag in quotes is a key. The check must agree with each line break, at each depth.
func TestRouteTaggedMergeKeysForEachLineBreak(t *testing.T) {
	routed := []struct{ name, doc string }{
		{"exact reproduced tagged merge", string(withSchema(`{type: object, default: {! "<<": {a: 1}, b: 2}}`))},
		{"plain tagged merge", string(withSchema(`{type: object, default: {! <<: {a: 1}, b: 2}}`))},
		{"merge tag on quoted key", string(withSchema(`{type: object, default: {!!merge "<<": {a: 1}, b: 2}}`))},
		{"string tag is a key", string(withSchema(`{type: object, default: {!!str <<: {a: 1}, a: 2}}`))},
		{"single quoted merge text is a key", string(withSchema(`{type: object, default: {'<<': {a: 1}, a: 2}}`))},
		{"tagged alias merge", string(withSchema(`{type: object, example: &e {a: 1}, default: {! "<<": *e, b: 2}}`))},
		{"tagged alias list merge", string(withSchema(`{type: object, example: [&e {a: 1}, &f {1: c}], default: {! "<<": [*e, *f], b: 2}}`))},
		{"tagged merges in nested raw values", string(withSchema(`{type: object, default: {x: [{! "<<": {a: 1}, b: 2}]}, example: {d: {! "<<": {c: 3}}}, enum: [{! "<<": {a: 1}, b: 2}]}`))},
		{"tagged merge of properties", string(withSchema(`{type: object, properties: {! "<<": {a: {type: string}}, b: {type: string}}}`))},
		{"block tagged merge", withMetadata("  annotations: &l\n    a: x\n  labels:\n    ! \"<<\": *l\n    b: c\n")},
		{"block string tag key", withMetadata("  labels:\n    !!str <<: x\n    b: c\n")},
	}
	rejected := []struct{ name, doc string }{
		{"tagged merge and key", string(withSchema(`{type: object, default: {! "<<": {1: a}, "1": b}}`))},
		{"merge tag and key", string(withSchema(`{type: object, default: {!!merge "<<": {1: a}, "1": b}}`))},
		{"tagged merge overlaps key", string(withSchema(`{type: object, default: {! "<<": {a: 1}, a: 2}}`))},
		{"tagged alias merge and key", string(withSchema(`{type: object, example: &e {1: a}, default: {! "<<": *e, "1": b}}`))},
		{"tagged merge in nested raw enum", string(withSchema(`{type: object, enum: [{x: {! "<<": {1: a}, "1": b}}]}`))},
		{"tagged merge in nested raw example", string(withSchema(`{type: object, example: {x: [{! "<<": {true: a}, "true": b}]}}`))},
		{"non-specific tag on key", string(withSchema(`{type: object, default: {! 1: a, "1": b}}`))},
		{"block tagged merge", withMetadata("  annotations: &l\n    1: a\n  labels:\n    ! \"<<\": *l\n    \"1\": b\n")},
	}
	for _, e := range lineEndings {
		for _, tc := range routed {
			t.Run(e.name+" routes "+tc.name, func(t *testing.T) {
				requireRouted(t, withBreaks(tc.doc, e.breaks))
			})
		}
		for _, tc := range rejected {
			t.Run(e.name+" rejects "+tc.name, func(t *testing.T) {
				requireRejected(t, withBreaks(tc.doc, e.breaks), ml.ErrMalformed)
			})
		}
	}
}
