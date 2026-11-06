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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	ml "github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	cacheEmpty      = "empty"
	cacheConfigured = "configured"
	cachePrivate    = "private"
	// observedFile is the record of the fake controller-gen. The fake writes it with a relative
	// path. Thus a record in the private directory shows that this directory is the working directory.
	observedFile = "observed.json"
	// okRole selects the valid output of the fake.
	okRole = "ok-role"
	// configuredCache is a synthetic GOCACHE value. The fake only compares it. No test makes this path.
	configuredCache = "/koodam-test/configured-gocache"
)

var (
	// fakeTool is this test binary. If the path is not known, it is "" and the tests get ErrInvalidTool.
	fakeTool, _ = os.Executable()
	// offlineEnv has the settings that the generator must get with these values.
	offlineEnv = []string{"GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOPACKAGESDRIVER=off"}
	// passedNames has the variables that the generator must get if this process has them.
	passedNames = []string{"PATH", "TMPDIR", "XDG_CACHE_HOME", "GOROOT", "GOPATH", "GOMODCACHE"}
)

// observation is the record of one run. It does not hold an incoming environment value. Names has
// the variable names. Offline has a pass or fail fact for each offlineEnv entry. Cache is a fixed
// fact about GOCACHE: "empty", "configured", "private" or "other".
type observation struct {
	Args    []string
	Names   []string
	Offline map[string]bool
	Cache   string
}

// TestMain makes the test binary the fake controller-gen. The fake gets only fixed arguments, thus
// no shell and no test flag reach it.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "rbac:roleName=") {
		os.Exit(fakeControllerGen(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// roleYAML is a valid synthetic role. The comment and the quoted name make sure that a changed copy
// is not equal to the original bytes. The rule is not a grant of this project.
func roleYAML(name string) string {
	return "# synthetic output\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: \"" + name +
		"\"\nrules:\n- apiGroups:\n  - example.com\n  resources:\n  - widgets\n  verbs:\n  - list\n"
}

// fakeControllerGen records its call and writes a synthetic output. The part of the role name
// before the first "-" selects the behavior.
func fakeControllerGen(args []string) int {
	out, ok := strings.CutPrefix(args[len(args)-1], "output:rbac:artifacts:config=")
	wd, err := os.Getwd()
	if len(args) != 3 || !ok || err != nil {
		return 2
	}
	record, err := json.Marshal(observe(args, wd))
	if err != nil || os.WriteFile(observedFile, record, 0o600) != nil {
		return 2
	}
	name := strings.TrimPrefix(args[0], "rbac:roleName=")
	role, data := filepath.Join(out, roleFile), roleYAML(name)
	mode, _, _ := strings.Cut(name, "-")
	switch mode {
	case "fail":
		return 1
	case "slow":
		time.Sleep(time.Minute)
		return 1
	case "missing":
		return 0
	case "invalid":
		data = strings.Replace(data, "kind: ClusterRole", "kind: Role", 1)
	case "name":
		data = roleYAML("other")
	case "docs":
		data += "---\n" + roleYAML(name)
	case "extra":
		err = os.WriteFile(filepath.Join(out, "other.yaml"), nil, 0o600)
	case "symlink":
		// The link goes to a valid role, which the last write makes.
		err = os.Symlink(filepath.Join(wd, "target.yaml"), role)
		role = "target.yaml"
	case "dir":
		// role.yaml is a directory. The last write puts a file in this directory.
		err = os.Mkdir(role, 0o700)
		role = filepath.Join(role, roleFile)
	}
	if err != nil || os.WriteFile(role, []byte(data), 0o600) != nil {
		return 2
	}
	return 0
}

// observe gives the record of this run. It keeps names and fixed facts. It does not keep a value.
func observe(args []string, wd string) observation {
	env := os.Environ()
	names := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		names = append(names, k)
	}
	offline := make(map[string]bool, len(offlineEnv))
	for _, f := range offlineEnv {
		offline[f] = slices.Contains(env, f)
	}
	return observation{Args: args, Names: names, Offline: offline, Cache: cacheFact(wd)}
}

// cacheFact gives the fixed fact about GOCACHE. A private cache is a real directory in wd.
func cacheFact(wd string) string {
	switch v := os.Getenv("GOCACHE"); v {
	case "":
		return cacheEmpty
	case configuredCache:
		return cacheConfigured
	case filepath.Join(wd, cacheDir):
		if fi, err := os.Lstat(v); err == nil && fi.IsDir() {
			return cachePrivate
		}
	}
	return "other"
}

// privateDir gives a new test directory without symbolic links.
func privateDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// observed reads the record in dir. If there is no record, dir was not the working directory of a run.
func observed(t *testing.T, dir string) observation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, observedFile))
	if err != nil {
		t.Fatal("the generator did not run in the private directory")
	}
	var o observation
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func requireEmpty(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("the private directory changed before a preflight error")
	}
}

// requireFixed examines that there is no output and that err is the fixed error with no input value.
func requireFixed(t *testing.T, data []byte, err, want error, values ...string) {
	t.Helper()
	if data != nil {
		t.Fatal("an error gives output")
	}
	if err != want {
		t.Fatalf("got %v, want %v", err, want)
	}
	for _, v := range values {
		if v != "" && strings.Contains(err.Error(), v) {
			t.Fatal("the error contains an input value")
		}
	}
}

// requireEnv examines the loader settings, a GOCACHE that is not empty, and the names. Each permitted
// name must occur one time. Thus HOME, XDG_CONFIG_HOME and all other ambient variables are absent.
func requireEnv(t *testing.T, o observation) {
	t.Helper()
	want := map[string]bool{"GOCACHE": true}
	for _, k := range passedNames {
		if _, ok := os.LookupEnv(k); ok {
			want[k] = true
		}
	}
	for _, f := range offlineEnv {
		if !o.Offline[f] {
			t.Fatalf("the generator does not get %s", f)
		}
		k, _, _ := strings.Cut(f, "=")
		want[k] = true
	}
	for _, k := range o.Names {
		if !want[k] {
			t.Fatalf("the generator gets the variable %s not one time", k)
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatal("the generator does not get all permitted variables")
	}
	if o.Cache == cacheEmpty {
		t.Fatal("the generator gets an empty GOCACHE")
	}
}

func TestGenerateRoleRunsFixedCommand(t *testing.T) {
	t.Setenv("KOODAM_TEST_SECRET", "synthetic")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "https://proxy.invalid")
	for role, pkg := range map[ml.RoleGroup]string{ml.RoleBase: "base", ml.RoleApp: "app"} {
		dir := privateDir(t)
		data, err := generateRole(context.Background(), fakeTool, dir, role, okRole)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != roleYAML(okRole) {
			t.Fatal("the output is not the original generator bytes")
		}
		o := observed(t, dir)
		want := []string{"rbac:roleName=" + okRole, "paths=./" + pkg, "output:rbac:artifacts:config=output/" + pkg}
		if !slices.Equal(o.Args, want) {
			t.Fatalf("the arguments are %q, want %q", o.Args, want)
		}
		requireEnv(t, o)
	}
}

// The ambient HOME and XDG_CONFIG_HOME do not reach the generator. The generator always gets a
// GOCACHE value.
func TestGenerateRoleUserConfigAndCache(t *testing.T) {
	// The test does not change an ambient HOME. It sets HOME only if it is absent.
	if _, ok := os.LookupEnv("HOME"); !ok {
		t.Setenv("HOME", t.TempDir())
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, c := range []struct {
		name, cache, want string
		set               bool
	}{
		{"configured", configuredCache, "configured", true},
		{"empty", "", "private", true},
		{"unset", "", "private", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GOCACHE", c.cache)
			if !c.set {
				if err := os.Unsetenv("GOCACHE"); err != nil {
					t.Fatal(err)
				}
			}
			dir := privateDir(t)
			if _, err := generateRole(context.Background(), fakeTool, dir, ml.RoleApp, "ok-app"); err != nil {
				t.Fatal(err)
			}
			o := observed(t, dir)
			requireEnv(t, o)
			if o.Cache != c.want {
				t.Fatalf("the GOCACHE fact is %s, want %s", o.Cache, c.want)
			}
		})
	}
}

func TestGenerateRoleRejectsOutput(t *testing.T) {
	for name, want := range map[string]error{
		"fail-role": ErrGenerate, "missing-role": ErrInvalidOutput, "invalid-role": ErrInvalidOutput,
		"name-role": ErrInvalidOutput, "docs-role": ErrInvalidOutput, "extra-role": ErrInvalidOutput,
		"symlink-role": ErrInvalidOutput, "dir-role": ErrInvalidOutput,
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateDir(t)
			data, err := generateRole(context.Background(), fakeTool, dir, ml.RoleBase, name)
			requireFixed(t, data, err, want, name, dir)
			observed(t, dir)
		})
	}
}

// Each preflight error comes before a change to the private directory and before a run. A role
// group cannot select a path.
func TestGenerateRoleRejectsInput(t *testing.T) {
	type input struct {
		tool, dir string
		role      ml.RoleGroup
		name      string
	}
	dir := privateDir(t)
	other := privateDir(t)
	plain := filepath.Join(other, "plain")
	link := filepath.Join(other, "link")
	if os.WriteFile(plain, nil, 0o600) != nil || os.Symlink(dir, link) != nil {
		t.Fatal("cannot write the plain file and the link")
	}
	cases := []struct {
		name string
		edit func(in *input)
		want error
	}{
		{"no tool", func(in *input) { in.tool = "" }, ErrInvalidTool},
		{"relative tool", func(in *input) { in.tool = "controller-gen" }, ErrInvalidTool},
		{"unclean tool", func(in *input) { in.tool = filepath.Dir(fakeTool) + "/./" + filepath.Base(fakeTool) }, ErrInvalidTool},
		{"missing tool", func(in *input) { in.tool = filepath.Join(other, "missing") }, ErrInvalidTool},
		{"directory tool", func(in *input) { in.tool = other }, ErrInvalidTool},
		{"not executable", func(in *input) { in.tool = plain }, ErrInvalidTool},
		{"no role name", func(in *input) { in.name = "" }, ErrInvalidRoleName},
		{"upper case", func(in *input) { in.name = "Koodam" }, ErrInvalidRoleName},
		{"separator", func(in *input) { in.name = "koodam,paths=./app" }, ErrInvalidRoleName},
		{"digit first", func(in *input) { in.name = "1koodam" }, ErrInvalidRoleName},
		{"too long", func(in *input) { in.name = strings.Repeat("a", 254) }, ErrInvalidRoleName},
		{"empty role", func(in *input) { in.role = "" }, ErrUnknownRole},
		{"role case", func(in *input) { in.role = "Base" }, ErrUnknownRole},
		{"role parent", func(in *input) { in.role = "../base" }, ErrUnknownRole},
		{"role traversal", func(in *input) { in.role = "base/../app" }, ErrUnknownRole},
		{"role child", func(in *input) { in.role = "app/x" }, ErrUnknownRole},
		{"role absolute", func(in *input) { in.role = ml.RoleGroup(other) }, ErrUnknownRole},
		{"role NUL", func(in *input) { in.role = "base\x00" }, ErrUnknownRole},
		{"no dir", func(in *input) { in.dir = "" }, ErrWorkspace},
		{"relative dir", func(in *input) { in.dir = "relative/dir" }, ErrWorkspace},
		{"unclean dir", func(in *input) { in.dir += "/." }, ErrWorkspace},
		{"missing dir", func(in *input) { in.dir = filepath.Join(other, "missing") }, ErrWorkspace},
		{"file dir", func(in *input) { in.dir = plain }, ErrWorkspace},
		{"symlink dir", func(in *input) { in.dir = link }, ErrWorkspace},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := input{tool: fakeTool, dir: dir, role: ml.RoleBase, name: okRole}
			c.edit(&in)
			data, err := generateRole(context.Background(), in.tool, in.dir, in.role, in.name)
			requireFixed(t, data, err, c.want, in.dir, string(in.role), in.name)
			requireEmpty(t, dir)
		})
	}
}

// The output directory must be new. The private output parent and cache must be real directories.
func TestGenerateRoleNeedsRealDirectories(t *testing.T) {
	t.Setenv("GOCACHE", "")
	for _, c := range []struct{ name, path string }{{"stale output", "output/base"}, {"output link", "output"}, {"cache link", cacheDir}} {
		t.Run(c.name, func(t *testing.T) {
			dir := privateDir(t)
			p := filepath.Join(dir, filepath.FromSlash(c.path))
			var err error
			if c.name == "stale output" {
				err = os.MkdirAll(p, 0o700)
			} else {
				err = os.Symlink(t.TempDir(), p)
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := generateRole(context.Background(), fakeTool, dir, ml.RoleBase, okRole)
			requireFixed(t, data, err, ErrWorkspace, dir)
			if _, err := os.Lstat(filepath.Join(dir, observedFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("the generator ran")
			}
		})
	}
}

func TestGenerateRoleCancel(t *testing.T) {
	dir := privateDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := generateRole(ctx, fakeTool, dir, ml.RoleBase, okRole)
	requireFixed(t, data, err, context.Canceled)
	requireEmpty(t, dir)
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	data, err = generateRole(ctx, fakeTool, dir, ml.RoleBase, "slow-role")
	requireFixed(t, data, err, context.DeadlineExceeded)
	if time.Since(start) > 30*time.Second {
		t.Fatal("the end of the context does not stop the generator")
	}
}
