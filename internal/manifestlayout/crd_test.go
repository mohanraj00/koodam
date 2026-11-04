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
