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

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"

	"github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	baseFile   = "internal/controller/agent_controller.go"
	appFile    = "internal/controller/appversion_controller.go"
	baseSource = "package controller\n\n// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get\n\n" +
		"// a is a function.\nfunc a() {}\n"
	appSource = "package controller\n\n// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get\n\n" +
		"// b is a function.\nfunc b() {}\n"
)

// fakeGen writes a role with the name and the staged markers as a comment, as a stand-in for
// controller-gen.
func fakeGen(dir, name string) error {
	markers, err := os.ReadFile(filepath.Join(dir, "markers.go"))
	if err != nil {
		return err
	}
	role := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: " + name + "\n"
	for line := range strings.SplitSeq(strings.TrimSpace(string(markers)), "\n") {
		role += "# " + line + "\n"
	}
	return os.WriteFile(filepath.Join(dir, "role.yaml"), []byte(role), 0o644)
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, data := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunSplitsTheRoles(t *testing.T) {
	root := writeTree(t, map[string]string{
		baseFile:        baseSource,
		appFile:         appSource,
		"bin/tool/x.go": appSource,
		".hidden/x.go":  appSource,
	})
	if err := run(root, fakeGen); err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(root, "config/rbac/role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := os.ReadFile(filepath.Join(root, "config/apps/rbac/role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(base), "name: manager-role\n") || !strings.Contains(string(base), "jobs") ||
		strings.Contains(string(base), "deployments") {
		t.Errorf("the base role is not correct:\n%s", base)
	}
	if !strings.Contains(string(app), "name: app-manager-role\n") || !strings.Contains(string(app), "deployments") ||
		strings.Contains(string(app), "jobs") {
		t.Errorf("the App role is not correct:\n%s", app)
	}
}

func TestRunWritesNoFileOnAnError(t *testing.T) {
	wrongName := func(dir, _ string) error { return fakeGen(dir, "other-role") }
	for name, tc := range map[string]struct {
		files map[string]string
		gen   func(dir, name string) error
		want  error
	}{
		"a marker in a file that is not in the index": {
			files: map[string]string{baseFile: baseSource,
				appFile: appSource, "internal/other/x.go": appSource},
			gen: fakeGen, want: manifestlayout.ErrUnindexedSource},
		"a role group with no marker": {
			files: map[string]string{baseFile: baseSource},
			gen:   fakeGen, want: errNoGroup},
		"a role with a different name": {
			files: map[string]string{baseFile: baseSource,
				appFile: appSource},
			gen: wrongName, want: manifestlayout.ErrNotRole},
	} {
		t.Run(name, func(t *testing.T) {
			root := writeTree(t, tc.files)
			if err := run(root, tc.gen); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "config")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the command wrote a file: %v", err)
			}
		})
	}
}

// appReadVerbs are the only verbs that the default role can have on appversions. The Hold
// controller reads the AppVersions of a Profile when the App controllers do not run.
var appReadVerbs = []string{"get", "list"}

// appGrants checks the default role. It permits only get and list on appversions, and it refuses
// each other App resource. It gives a problem for each refused grant.
func appGrants(data []byte) ([]string, error) {
	role := rbacv1.ClusterRole{}
	if err := yaml.UnmarshalStrict(data, &role); err != nil {
		return nil, err
	}
	var problems []string
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			switch base, _, _ := strings.Cut(res, "/"); {
			case base == "deployments" || base == "services" || base == "*":
				problems = append(problems, "the App grant "+res)
			case res == "appversions":
				for _, v := range rule.Verbs {
					if !slices.Contains(appReadVerbs, v) {
						problems = append(problems, "the verb "+v+" on appversions")
					}
				}
			case base == "appversions":
				problems = append(problems, "the App grant "+res)
			}
		}
	}
	return problems, nil
}

func TestAppGrants(t *testing.T) {
	const head = "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: manager-role\nrules:\n"
	rule := func(resource string, verbs ...string) string {
		return "- apiGroups: [core.koodam.dev]\n  resources: [" + resource + "]\n  verbs: [" +
			strings.Join(verbs, ", ") + "]\n"
	}
	for name, tc := range map[string]struct {
		rules string
		want  []string
	}{
		"the read verbs on appversions": {rule("appversions", "get", "list"), nil},
		"a write verb on appversions": {rule("appversions", "get", "update"),
			[]string{"the verb update on appversions"}},
		"the watch verb on appversions": {rule("appversions", "watch"), []string{"the verb watch on appversions"}},
		"the status of appversions":     {rule("appversions/status", "get"), []string{"the App grant appversions/status"}},
		"deployments":                   {rule("deployments", "get"), []string{"the App grant deployments"}},
		"services":                      {rule("services", "list"), []string{"the App grant services"}},
		"a wildcard":                    {rule(`"*"`, "get"), []string{"the App grant *"}},
		"a different resource":          {rule("holds", "get", "update"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := appGrants([]byte(head + tc.rules))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDefaultRoleHasNoAppGrant examines the generated roles of the repository.
func TestDefaultRoleHasNoAppGrant(t *testing.T) {
	base, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	problems, err := appGrants(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("config/rbac/role.yaml has %s", p)
	}
	app, err := os.ReadFile("../../config/apps/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"secrets", "serviceaccounts", "roles", "\"*\"", "'*'"} {
		if strings.Contains(string(app), s) {
			t.Errorf("config/apps/rbac/role.yaml has %q", s)
		}
	}
}
