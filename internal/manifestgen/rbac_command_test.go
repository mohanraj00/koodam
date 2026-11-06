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
	"bytes"
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
	// observedFile is the record that the fake controller-gen writes into its working directory.
	observedFile = "observed.json"
	// okRole selects the valid output of the fake.
	okRole = "ok-role"
)

type observation struct {
	Args []string
	Dir  string
	Env  []string
	// PrivateCache is the name of the GOCACHE directory if it is a directory in the working
	// directory. If not, it is "".
	PrivateCache string
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
	dir, err := os.Getwd()
	if len(args) != 3 || !ok || err != nil {
		return 2
	}
	record, err := json.Marshal(observation{Args: args, Dir: dir, Env: os.Environ(), PrivateCache: privateCache(dir)})
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
		if os.WriteFile(filepath.Join(out, "other.yaml"), nil, 0o600) != nil {
			return 2
		}
	case "symlink":
		if os.WriteFile("target.yaml", []byte(data), 0o600) != nil || os.Symlink(filepath.Join(dir, "target.yaml"), role) != nil {
			return 2
		}
		return 0
	case "dir":
		if os.Mkdir(role, 0o700) != nil {
			return 2
		}
		return 0
	}
	if os.WriteFile(role, []byte(data), 0o600) != nil {
		return 2
	}
	return 0
}

// privateCache gives the name of the GOCACHE directory if it is an absolute path to a directory in
// dir. It compares the files, thus a symbolic link in the temporary path has no effect.
func privateCache(dir string) string {
	v, ok := os.LookupEnv("GOCACHE")
	if !ok || !filepath.IsAbs(v) {
		return ""
	}
	cache, err := os.Stat(v)
	if err != nil || !cache.IsDir() {
		return ""
	}
	child, err := os.Stat(filepath.Join(dir, filepath.Base(v)))
	if err != nil || !os.SameFile(cache, child) {
		return ""
	}
	return filepath.Base(v)
}

func fakeTool(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
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

func observed(t *testing.T, dir string) observation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, observedFile))
	if err != nil {
		t.Fatal("the generator did not run")
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

// requireEnv examines that the generator gets the loader settings, one GOCACHE value and only the
// permitted variables, each one time. HOME and XDG_CONFIG_HOME are not permitted.
func requireEnv(t *testing.T, env []string) {
	t.Helper()
	fixed := []string{"GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOPACKAGESDRIVER=off"}
	permitted := []string{"PATH", "TMPDIR", "XDG_CACHE_HOME", "GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE"}
	for _, f := range fixed {
		if !slices.Contains(env, f) {
			t.Fatalf("the generator does not get %s", f)
		}
		k, _, _ := strings.Cut(f, "=")
		permitted = append(permitted, k)
	}
	seen := map[string]bool{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if !slices.Contains(permitted, k) || seen[k] {
			t.Fatalf("the generator gets the variable %s not one time", k)
		}
		seen[k] = true
		if k == "GOCACHE" && v == "" {
			t.Fatal("the generator gets an empty GOCACHE")
		}
	}
	if !seen["GOCACHE"] {
		t.Fatal("the generator does not get GOCACHE")
	}
}

func envValue(env []string, k string) (string, bool) {
	for _, e := range env {
		if ek, v, _ := strings.Cut(e, "="); ek == k {
			return v, true
		}
	}
	return "", false
}

func TestGenerateRoleRunsFixedCommand(t *testing.T) {
	t.Setenv("KOODAM_TEST_SECRET", "synthetic")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "https://proxy.invalid")
	for role, pkg := range map[ml.RoleGroup]string{ml.RoleBase: "base", ml.RoleApp: "app"} {
		dir := privateDir(t)
		data, err := generateRole(context.Background(), fakeTool(t), dir, role, okRole)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, []byte(roleYAML(okRole))) {
			t.Fatal("the output is not the original generator bytes")
		}
		o := observed(t, dir)
		want := []string{"rbac:roleName=" + okRole, "paths=./" + pkg, "output:rbac:artifacts:config=output/" + pkg}
		if !slices.Equal(o.Args, want) {
			t.Fatalf("the arguments are %q, want %q", o.Args, want)
		}
		if o.Dir != dir {
			t.Fatal("the generator does not run in the private directory")
		}
		requireEnv(t, o.Env)
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
	configured := t.TempDir()
	for _, c := range []struct {
		name, cache string
		set         bool
	}{{"configured", configured, true}, {"empty", "", true}, {"unset", "", false}} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GOCACHE", c.cache)
			if !c.set {
				if err := os.Unsetenv("GOCACHE"); err != nil {
					t.Fatal(err)
				}
			}
			dir := privateDir(t)
			if _, err := generateRole(context.Background(), fakeTool(t), dir, ml.RoleApp, "ok-app"); err != nil {
				t.Fatal(err)
			}
			o := observed(t, dir)
			requireEnv(t, o.Env)
			for _, k := range []string{"HOME", "XDG_CONFIG_HOME"} {
				if _, ok := envValue(o.Env, k); ok {
					t.Fatalf("the generator gets %s", k)
				}
			}
			cache, _ := envValue(o.Env, "GOCACHE")
			if c.cache != "" && (cache != c.cache || o.PrivateCache != "") {
				t.Fatal("the generator does not get the configured GOCACHE")
			}
			if c.cache == "" && (cache != filepath.Join(dir, cacheDir) || o.PrivateCache != cacheDir) {
				t.Fatal("the generator does not get a private GOCACHE in the private directory")
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
			data, err := generateRole(context.Background(), fakeTool(t), dir, ml.RoleBase, name)
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
	tool := fakeTool(t)
	other := privateDir(t)
	plain := filepath.Join(other, "plain")
	if os.WriteFile(plain, nil, 0o600) != nil {
		t.Fatal("cannot write the plain file")
	}
	cases := []struct {
		name string
		edit func(in *input)
		want error
	}{
		{"no tool", func(in *input) { in.tool = "" }, ErrInvalidTool},
		{"relative tool", func(in *input) { in.tool = "controller-gen" }, ErrInvalidTool},
		{"unclean tool", func(in *input) { in.tool = filepath.Dir(tool) + "/./" + filepath.Base(tool) }, ErrInvalidTool},
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
		{"symlink dir", func(in *input) { in.dir = filepath.Join(other, "link") }, ErrWorkspace},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := privateDir(t)
			link := filepath.Join(other, "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(link) })
			in := input{tool: tool, dir: dir, role: ml.RoleBase, name: okRole}
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
			data, err := generateRole(context.Background(), fakeTool(t), dir, ml.RoleBase, okRole)
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
	data, err := generateRole(ctx, fakeTool(t), dir, ml.RoleBase, okRole)
	requireFixed(t, data, err, context.Canceled)
	requireEmpty(t, dir)
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	data, err = generateRole(ctx, fakeTool(t), dir, ml.RoleBase, "slow-role")
	requireFixed(t, data, err, context.DeadlineExceeded)
	if time.Since(start) > 30*time.Second {
		t.Fatal("the end of the context does not stop the generator")
	}
}
