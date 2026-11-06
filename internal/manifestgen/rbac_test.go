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

package manifestgen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	ml "github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	// The markers have synthetic values. They are not grants of this project.
	baseMarker = "// +kubebuilder:rbac:groups=example.com,resources=widgets,verbs=list"
	appMarker  = "// +kubebuilder:rbac:groups=example.com,resources=gadgets,verbs=get"
	agentPath  = "internal/controller/agent_controller.go"
	appPath    = "internal/controller/app_controller.go"
	basePkg    = "base"
	appPkg     = "app"
)

// testNames select the valid output of the fake.
var testNames = RoleNames{Base: "koodam-base", App: "koodam-app"}

func markedSource(marker string) []byte {
	return []byte("package controller\n\ntype reconciler struct{}\n\n" + marker + "\n\n// Reconcile does the work.\nfunc Reconcile() {}\n")
}

// splitSources has the App source first. The source index puts the base role first.
func splitSources() []ml.Source {
	return []ml.Source{{Path: appPath, Data: markedSource(appMarker)}, {Path: agentPath, Data: markedSource(baseMarker)}}
}

// privateTemp sets TMPDIR to a new test directory and gives this directory.
func privateTemp(t *testing.T) string {
	t.Helper()
	dir := privateDir(t)
	t.Setenv("TMPDIR", dir)
	return dir
}

// inspect gives a remove function. It runs check on the private directory and then removes it.
func inspect(check func(dir string)) func(string) error {
	return func(dir string) error {
		check(dir)
		return os.RemoveAll(dir)
	}
}

// noDirectory gives a remove function for a call that must not make a private directory.
func noDirectory(t *testing.T) func(string) error {
	return func(string) error {
		t.Error("the call made a private directory")
		return nil
	}
}

func requireRemoved(t *testing.T, tmp string) {
	t.Helper()
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatal("the private generator directory is not removed")
	}
}

func requireFile(t *testing.T, p, want string) {
	t.Helper()
	if data, err := os.ReadFile(p); err != nil || string(data) != want {
		t.Fatalf("the file %s does not have the expected bytes", filepath.Base(p))
	}
}

func requireAbsent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the path %s exists", filepath.Base(p))
	}
}

// requireRefused examines that there is no output and that err is the fixed error with no input value.
func requireRefused(t *testing.T, out []RoleManifest, err, want error, values ...string) {
	t.Helper()
	if out != nil {
		t.Fatal("an error gives output")
	}
	requireFixed(t, nil, err, want, values...)
}

// The staged directory is private. It has the fixed module and one package for each role. The
// staged packages are the extracted packages without change.
func TestGenerateRBACRolesStagesEachRole(t *testing.T) {
	tmp := privateTemp(t)
	t.Setenv("KOODAM_TEST_SECRET", "synthetic")
	t.Setenv("GOCACHE", "")
	sources := splitSources()
	staged, err := ml.ExtractRBACMarkers(sources)
	if err != nil || len(staged) != 2 || staged[0].Role != ml.RoleBase || staged[1].Role != ml.RoleApp {
		t.Fatal("the sources do not give a base package and then an App package")
	}
	examined := false
	check := func(dir string) {
		examined = true
		fi, err := os.Lstat(dir)
		if err != nil || filepath.Dir(dir) != tmp || fi.Mode().Perm() != 0o700 {
			t.Fatal("the generator directory is not a private directory in TMPDIR")
		}
		requireFile(t, filepath.Join(dir, goModFile), goMod)
		requireFile(t, filepath.Join(dir, basePkg, markerFile), string(staged[0].Data))
		requireFile(t, filepath.Join(dir, appPkg, markerFile), string(staged[1].Data))
		requireFile(t, filepath.Join(dir, outputDir, basePkg, roleFile), roleYAML(testNames.Base))
		requireFile(t, filepath.Join(dir, outputDir, appPkg, roleFile), roleYAML(testNames.App))
		// The fake keeps the record of the last run. That is the App role.
		o := observed(t, dir)
		want := []string{"rbac:roleName=" + testNames.App, "paths=./" + appPkg, "output:rbac:artifacts:config=" + outputDir + "/" + appPkg}
		if !slices.Equal(o.Args, want) {
			t.Fatalf("the arguments are %q, want %q", o.Args, want)
		}
		requireEnv(t, o)
		if o.Cache != cachePrivate {
			t.Fatal("the generator does not get the private GOCACHE")
		}
	}
	if _, err := generateRoles(context.Background(), fakeTool, testNames, sources, inspect(check)); err != nil {
		t.Fatal(err)
	}
	if !examined {
		t.Fatal("the call did not remove a private directory")
	}
	requireRemoved(t, tmp)
}

// The output has one role for each group in the index order, with the original generator bytes.
func TestGenerateRBACRolesGivesOriginalBytes(t *testing.T) {
	tmp := privateTemp(t)
	sources := splitSources()
	out, err := GenerateRBACRoles(context.Background(), fakeTool, testNames, sources)
	if err != nil {
		t.Fatal(err)
	}
	want := []RoleManifest{
		{Role: ml.RoleBase, Name: testNames.Base, Data: []byte(roleYAML(testNames.Base))},
		{Role: ml.RoleApp, Name: testNames.App, Data: []byte(roleYAML(testNames.App))},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatal("the output is not the original generator bytes for each role in the index order")
	}
	if !reflect.DeepEqual(sources, splitSources()) {
		t.Fatal("the input changed")
	}
	requireRemoved(t, tmp)
}

// A group without markers gives no package, no generator run and no output.
func TestGenerateRBACRolesOneGroup(t *testing.T) {
	tmp := privateTemp(t)
	sources := []ml.Source{{Path: agentPath, Data: markedSource(baseMarker)}, {Path: appPath, Data: []byte("package controller\n")}}
	check := func(dir string) {
		requireAbsent(t, filepath.Join(dir, appPkg))
		requireAbsent(t, filepath.Join(dir, outputDir, appPkg))
	}
	out, err := generateRoles(context.Background(), fakeTool, testNames, sources, inspect(check))
	if err != nil {
		t.Fatal(err)
	}
	want := []RoleManifest{{Role: ml.RoleBase, Name: testNames.Base, Data: []byte(roleYAML(testNames.Base))}}
	if !reflect.DeepEqual(out, want) {
		t.Fatal("the output is not one role for the group with markers")
	}
	requireRemoved(t, tmp)
}

// Without markers, the result is empty and not nil. There is no private directory and no run.
func TestGenerateRBACRolesNoMarkers(t *testing.T) {
	tmp := privateTemp(t)
	for _, sources := range [][]ml.Source{nil, {{Path: agentPath, Data: []byte("package controller\n")}}} {
		out, err := generateRoles(context.Background(), fakeTool, testNames, sources, noDirectory(t))
		if err != nil || out == nil || len(out) != 0 {
			t.Fatal("no marker must give an empty output and no error")
		}
		requireRemoved(t, tmp)
	}
}

// Each refused input gives a fixed error before a private directory and before a run. The tests of
// generateRole and manifestlayout cover the other forms of each input.
func TestGenerateRBACRolesRejectsInput(t *testing.T) {
	tmp := privateTemp(t)
	source := func(p, data string) []ml.Source { return []ml.Source{{Path: p, Data: []byte(data)}} }
	cases := []struct {
		name    string
		tool    string
		names   RoleNames
		sources []ml.Source
		want    error
	}{
		{"no tool", "", testNames, splitSources(), ErrInvalidTool},
		{"relative tool", "controller-gen", testNames, splitSources(), ErrInvalidTool},
		{"no base name", fakeTool, RoleNames{App: testNames.App}, splitSources(), ErrInvalidRoleName},
		{"no app name", fakeTool, RoleNames{Base: testNames.Base}, splitSources(), ErrInvalidRoleName},
		{"same names", fakeTool, RoleNames{Base: testNames.App, App: testNames.App}, splitSources(), ErrInvalidRoleName},
		{"separator", fakeTool, RoleNames{Base: "koodam,paths=./app", App: testNames.App}, splitSources(), ErrInvalidRoleName},
		{"malformed source", fakeTool, testNames, source(agentPath, "not go"), ml.ErrMalformedSource},
		{"unindexed marker", fakeTool, testNames, source("internal/controller/other_controller.go", string(markedSource(baseMarker))), ml.ErrUnindexedSource},
		{"marker placement", fakeTool, testNames, source(agentPath, "package controller\n\nfunc Reconcile() {\n\t"+baseMarker+"\n}\n"), ml.ErrUnsupportedMarker},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := generateRoles(context.Background(), c.tool, c.names, c.sources, noDirectory(t))
			requireRefused(t, out, err, c.want, c.names.Base, c.names.App, c.sources[0].Path)
			requireRemoved(t, tmp)
		})
	}
}

// A later role fails after a valid earlier role. The call gives no partial output.
func TestGenerateRBACRolesNoPartialOutput(t *testing.T) {
	for app, want := range map[string]error{"fail-app": ErrGenerate, "missing-app": ErrInvalidOutput, "invalid-app": ErrInvalidOutput} {
		t.Run(app, func(t *testing.T) {
			tmp := privateTemp(t)
			check := func(dir string) {
				requireFile(t, filepath.Join(dir, outputDir, basePkg, roleFile), roleYAML(testNames.Base))
			}
			out, err := generateRoles(context.Background(), fakeTool, RoleNames{Base: testNames.Base, App: app}, splitSources(), inspect(check))
			requireRefused(t, out, err, want, app, tmp)
			requireRemoved(t, tmp)
		})
	}
}

// A failed removal gives no output, also after each role is valid. The error has no path. The first
// error of the call stays the result.
func TestGenerateRBACRolesCleanupFailure(t *testing.T) {
	for name, c := range map[string]struct {
		names RoleNames
		want  error
	}{
		"after success":       {testNames, ErrCleanup},
		"after a failed role": {RoleNames{Base: testNames.Base, App: "fail-app"}, ErrGenerate},
	} {
		t.Run(name, func(t *testing.T) {
			privateTemp(t)
			dir := ""
			remove := func(d string) error {
				dir = d
				return errors.New("synthetic removal failure in " + d)
			}
			out, err := generateRoles(context.Background(), fakeTool, c.names, splitSources(), remove)
			if dir == "" {
				t.Fatal("the call did not remove a private directory")
			}
			requireRefused(t, out, err, c.want, dir)
		})
	}
}

// If the call cannot make the private directory, it gives a fixed error with no path.
func TestGenerateRBACRolesWorkspace(t *testing.T) {
	missing := filepath.Join(privateDir(t), "missing")
	t.Setenv("TMPDIR", missing)
	out, err := GenerateRBACRoles(context.Background(), fakeTool, testNames, splitSources())
	requireRefused(t, out, err, ErrWorkspace, missing)
}

// The end of the context stops the call. The public function removes the directory after the stop.
func TestGenerateRBACRolesCancel(t *testing.T) {
	tmp := privateTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := generateRoles(ctx, fakeTool, testNames, splitSources(), noDirectory(t))
	requireRefused(t, out, err, context.Canceled)
	requireRemoved(t, tmp)
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	out, err = GenerateRBACRoles(ctx, fakeTool, RoleNames{Base: testNames.Base, App: "slow-app"}, splitSources())
	requireRefused(t, out, err, context.DeadlineExceeded)
	if time.Since(start) > 30*time.Second {
		t.Fatal("the end of the context does not stop the generator")
	}
	requireRemoved(t, tmp)
}

// The temporary module has the go directive of the repository.
func TestGoModHasRepositoryDirective(t *testing.T) {
	repoMod, err := os.ReadFile(filepath.Join("..", "..", goModFile))
	_, directive, ok := strings.Cut(goMod, "\n\n")
	if err != nil || !ok || !strings.Contains(string(repoMod), "\n"+directive) {
		t.Fatal("the temporary module does not have the go directive of the repository")
	}
}
