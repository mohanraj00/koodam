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
	"slices"
	"testing"

	ml "github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	agentSource      = "internal/controller/agent_controller.go"
	profileSource    = "internal/controller/profile_controller.go"
	workspaceSource  = "internal/controller/workspace_controller.go"
	appSource        = "internal/controller/app_controller.go"
	appVersionSource = "internal/controller/appversion_controller.go"
	unknownSource    = "internal/controller/extra_controller.go"
	// syntheticMarker has synthetic values. It is not a grant of this project.
	syntheticMarker = "// +kubebuilder:rbac:groups=example.com,resources=widgets,verbs=get"
)

// goSource gives a small Go file with the comment lines before a function and the body.
func goSource(comments, body string) []byte {
	return []byte("package controller\n\n" + comments + "\nfunc reconcile() {\n" + body + "\n}\n")
}

func markedSource(p string) ml.Source { return ml.Source{Path: p, Data: goSource(syntheticMarker, "")} }

// controllerSources reads the controller sources that have RBAC markers now.
func controllerSources(t *testing.T) []ml.Source {
	t.Helper()
	out := make([]ml.Source, 0, 3)
	for _, p := range []string{agentSource, profileSource, workspaceSource} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ml.Source{Path: p, Data: data})
	}
	return out
}

func cloneSources(in []ml.Source) []ml.Source {
	out := make([]ml.Source, len(in))
	for i, s := range in {
		out[i] = ml.Source{Path: s.Path, Data: bytes.Clone(s.Data)}
	}
	return out
}

func sameSources(a, b []ml.Source) bool {
	return slices.EqualFunc(a, b, func(x, y ml.Source) bool { return x.Path == y.Path && bytes.Equal(x.Data, y.Data) })
}

func samePlans(a, b []ml.SourcePlacement) bool {
	return slices.EqualFunc(a, b, func(x, y ml.SourcePlacement) bool {
		return x.Role == y.Role && x.Path == y.Path && bytes.Equal(x.Data, y.Data)
	})
}

func sourcePaths(plan []ml.SourcePlacement) []string {
	out := make([]string, 0, len(plan))
	for _, p := range plan {
		out = append(out, p.Path)
	}
	return out
}

// The current controller sources go to the base role. The synthetic App sources go to the App
// role. The plan keeps the original bytes in the index order.
func TestRouteRBACSourcesMixedInput(t *testing.T) {
	crlf := bytes.ReplaceAll(goSource(syntheticMarker, ""), []byte("\n"), []byte("\r\n"))
	in := append([]ml.Source{
		{Path: appVersionSource, Data: append([]byte("\xef\xbb\xbf"), crlf...)},
		{Path: "internal/controller/helper.go", Data: goSource("// A plain comment.", "")},
		markedSource(appSource),
	}, controllerSources(t)...)
	before := cloneSources(in)
	reversed := slices.Clone(in)
	slices.Reverse(reversed)
	a, err := ml.RouteRBACSources(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ml.RouteRBACSources(reversed)
	if err != nil {
		t.Fatal(err)
	}
	want := []ml.SourcePlacement{
		{Role: ml.RoleBase, Path: agentSource},
		{Role: ml.RoleBase, Path: profileSource},
		{Role: ml.RoleBase, Path: workspaceSource},
		{Role: ml.RoleApp, Path: appSource},
		{Role: ml.RoleApp, Path: appVersionSource},
	}
	for i := range want {
		j := slices.IndexFunc(in, func(s ml.Source) bool { return s.Path == want[i].Path })
		want[i].Data = before[j].Data
	}
	if !samePlans(a, want) {
		t.Fatalf("plan %v does not agree with the index order, the roles or the bytes", sourcePaths(a))
	}
	if !samePlans(a, b) {
		t.Fatal("the plan changes with the input order")
	}
	// The plan must not share memory with the input.
	for _, p := range a {
		p.Data[0] ^= 0xff
	}
	if !sameSources(in, before) {
		t.Fatal("RouteRBACSources changed the input")
	}
}

// requireSourcesRejected examines that RouteRBACSources gives no plan, the fixed error and no
// input change. A valid marked source comes first, thus a partial plan is not empty.
func requireSourcesRejected(t *testing.T, bad []ml.Source, want error) {
	t.Helper()
	in := append([]ml.Source{markedSource(appSource)}, bad...)
	before := cloneSources(in)
	plan, err := ml.RouteRBACSources(in)
	if plan != nil {
		t.Fatalf("got a partial plan %v", sourcePaths(plan))
	}
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if err.Error() != want.Error() {
		t.Fatal("the error text is not the fixed reason")
	}
	if !sameSources(in, before) {
		t.Fatal("RouteRBACSources changed the input")
	}
}

func TestRouteRBACSourcesRejectsWithoutPartialPlan(t *testing.T) {
	plain := func(p string) ml.Source { return ml.Source{Path: p, Data: goSource("", "")} }
	data := func(b string) ml.Source { return ml.Source{Path: unknownSource, Data: []byte(b)} }
	cases := []struct {
		name string
		bad  []ml.Source
		want error
	}{
		{"unknown marked file", []ml.Source{markedSource(unknownSource)}, ml.ErrUnindexedSource},
		{"marker in unknown test file", []ml.Source{markedSource("internal/controller/agent_controller_test.go")}, ml.ErrUnindexedSource},
		{"marker in other directory", []ml.Source{markedSource("cmd/agent_controller.go")}, ml.ErrUnindexedSource},
		{"marker in case variant of index path", []ml.Source{markedSource("internal/controller/Agent_controller.go")}, ml.ErrUnindexedSource},
		{"duplicate path", []ml.Source{markedSource(agentSource), plain(agentSource)}, ml.ErrDuplicateSource},
		{"duplicate of first path", []ml.Source{plain(appSource)}, ml.ErrDuplicateSource},
		{"duplicate path with other case", []ml.Source{plain("internal/controller/App_controller.go")}, ml.ErrDuplicateSource},
		{"absolute path", []ml.Source{plain("/" + agentSource)}, ml.ErrUnsafeSourcePath},
		{"parent path", []ml.Source{plain("../" + agentSource)}, ml.ErrUnsafeSourcePath},
		{"inner parent part", []ml.Source{plain("internal/../" + agentSource)}, ml.ErrUnsafeSourcePath},
		{"dot part", []ml.Source{plain("./" + agentSource)}, ml.ErrUnsafeSourcePath},
		{"double separator", []ml.Source{plain("internal//controller/agent_controller.go")}, ml.ErrUnsafeSourcePath},
		{"backslash", []ml.Source{plain(`internal\controller\agent_controller.go`)}, ml.ErrUnsafeSourcePath},
		{"directory", []ml.Source{plain("internal/controller/")}, ml.ErrUnsafeSourcePath},
		{"directory with Go name", []ml.Source{plain("internal/controller.go/")}, ml.ErrUnsafeSourcePath},
		{"not Go", []ml.Source{plain("internal/controller/agent_controller.txt")}, ml.ErrUnsafeSourcePath},
		{"only extension", []ml.Source{plain("internal/controller/.go")}, ml.ErrUnsafeSourcePath},
		{"hidden file", []ml.Source{plain("internal/controller/.agent_controller.go")}, ml.ErrUnsafeSourcePath},
		{"drive letter", []ml.Source{plain("C:/internal/controller/agent_controller.go")}, ml.ErrUnsafeSourcePath},
		{"space", []ml.Source{plain("internal/controller/agent controller.go")}, ml.ErrUnsafeSourcePath},
		{"NUL", []ml.Source{plain(agentSource + "\x00")}, ml.ErrUnsafeSourcePath},
		{"empty path", []ml.Source{plain("")}, ml.ErrUnsafeSourcePath},
		{"no package clause", []ml.Source{data(syntheticMarker + "\n")}, ml.ErrMalformedSource},
		{"syntax error", []ml.Source{data("package controller\n\nfunc (\n")}, ml.ErrMalformedSource},
		{"open block comment", []ml.Source{data("package controller\n\n/* " + syntheticMarker + "\n")}, ml.ErrMalformedSource},
		{"no data", []ml.Source{{Path: unknownSource}}, ml.ErrMalformedSource},
		{"malformed index file", []ml.Source{{Path: agentSource, Data: []byte("package controller\n\nvar = \n")}}, ml.ErrMalformedSource},
		{"malformed unmarked file", []ml.Source{data("package controller\n\nfunc reconcile() {\n")}, ml.ErrMalformedSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireSourcesRejected(t, tc.bad, tc.want)
		})
	}
}

// Only a "//" comment with "+kubebuilder:rbac" and the end, ":" or "=" after the first spaces
// is a marker. Block comments and strings are not comments for the marker rules.
func TestRBACMarkerCommentRules(t *testing.T) {
	values := ":groups=example.com,resources=widgets,verbs=get"
	cases := []struct {
		name, comments, body string
		marked               bool
	}{
		{"one space", "// +kubebuilder:rbac" + values, "", true},
		{"no space", "//+kubebuilder:rbac" + values, "", true},
		{"tab", "//\t+kubebuilder:rbac" + values, "", true},
		{"many spaces and trailing space", "//    +kubebuilder:rbac" + values + "  \t", "", true},
		{"no-break space", "//\u00a0+kubebuilder:rbac" + values, "", true},
		{"bare name", "// +kubebuilder:rbac", "", true},
		{"bare name with trailing space", "// +kubebuilder:rbac ", "", true},
		{"equals after name", "// +kubebuilder:rbac=x", "", true},
		{"more name parts", "// +kubebuilder:rbac:groups:extra=x", "", true},
		{"after other doc lines", "// Reconcile does the work.\n//\n// +kubebuilder:rbac" + values, "", true},
		{"in function body", "", "\t// +kubebuilder:rbac" + values, true},
		{"at end of code line", "", "\t_ = 1 // +kubebuilder:rbac" + values, true},
		{"longer name", "// +kubebuilder:rbacx" + values, "", false},
		{"digit after name", "// +kubebuilder:rbac2", "", false},
		{"letters after name", "// +kubebuilder:rbacgroups=x", "", false},
		{"space after name", "// +kubebuilder:rbac groups=x", "", false},
		{"tab after name", "// +kubebuilder:rbac\t" + values, "", false},
		{"other case", "// +Kubebuilder:rbac" + values, "", false},
		{"space after plus", "// + kubebuilder:rbac" + values, "", false},
		{"two plus signs", "// ++kubebuilder:rbac" + values, "", false},
		{"no plus", "// kubebuilder:rbac" + values, "", false},
		{"text before plus", "// see +kubebuilder:rbac" + values, "", false},
		{"other prefix", "// +xkubebuilder:rbac" + values, "", false},
		{"other marker", "// +kubebuilder:object:root=true", "", false},
		{"webhook marker", "// +kubebuilder:webhook:path=/validate,mutating=false", "", false},
		{"one-line block comment", "/* +kubebuilder:rbac" + values + " */", "", false},
		{"block comment with line comment text", "/*\n// +kubebuilder:rbac" + values + "\n*/", "", false},
		{"block comment in body", "", "\t/* +kubebuilder:rbac" + values + " */", false},
		{"string", "", "\t_ = \"// +kubebuilder:rbac" + values + "\"", false},
		{"raw string", "", "\t_ = `\n// +kubebuilder:rbac" + values + "\n`", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := ml.Source{Path: unknownSource, Data: goSource(tc.comments, tc.body)}
			if tc.marked {
				requireSourcesRejected(t, []ml.Source{src}, ml.ErrUnindexedSource)
				return
			}
			plan, err := ml.RouteRBACSources([]ml.Source{src})
			if err != nil || plan == nil || len(plan) != 0 {
				t.Fatalf("plan %v, err %v: the decoy is a marker", sourcePaths(plan), err)
			}
		})
	}
}

// Valid sources without a marker give an empty plan, not a failure.
func TestRouteRBACSourcesEmptyPlan(t *testing.T) {
	inputs := map[string][]ml.Source{
		"no input": nil,
		"no marker": {
			{Path: appSource, Data: goSource("// App reconciles later.", "")},
			{Path: agentSource, Data: goSource("", "")},
			{Path: "internal/controller/helper.go", Data: goSource("// +kubebuilder:object:root=true", "")},
		},
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			before := cloneSources(in)
			plan, err := ml.RouteRBACSources(in)
			if err != nil || plan == nil || len(plan) != 0 {
				t.Fatalf("plan %v, err %v, want an empty plan", sourcePaths(plan), err)
			}
			if !sameSources(in, before) {
				t.Fatal("RouteRBACSources changed the input")
			}
		})
	}
}
