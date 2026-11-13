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

// Command rbac-route makes one ClusterRole file for each role group. The closed source-file index
// in internal/manifestlayout gives the role group of each Go file with an RBAC marker. The command
// copies the markers of each group into a temporary Go package and runs controller-gen on that
// package. Thus the App grants go to config/apps/rbac/role.yaml, and the default install in
// config/rbac/role.yaml has no App grant.
//
// Usage, from the repository root: go run ./hack/rbac-route CONTROLLER_GEN
//
// If one source file or one role output is not valid, the command writes no file.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mohanraj00/koodam/internal/manifestlayout"
)

var (
	errUsage   = errors.New("rbac-route: usage: rbac-route CONTROLLER_GEN")
	errNoGroup = errors.New("rbac-route: a role group has no RBAC marker")
)

// output is the role name and the destination of one role group.
type output struct{ name, path string }

func outputs() map[manifestlayout.RoleGroup]output {
	return map[manifestlayout.RoleGroup]output{
		manifestlayout.RoleBase: {name: "manager-role", path: "config/rbac/role.yaml"},
		manifestlayout.RoleApp:  {name: "app-manager-role", path: "config/apps/rbac/role.yaml"},
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, errUsage)
		os.Exit(2)
	}
	gen, err := filepath.Abs(os.Args[1])
	if err == nil {
		err = run(".", func(dir, name string) error { return controllerGen(gen, dir, name) })
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// controllerGen runs controller-gen on the staged package in dir. It writes dir/role.yaml.
func controllerGen(gen, dir, name string) error {
	cmd := exec.Command(gen, "rbac:roleName="+name, "paths=.", "output:rbac:artifacts:config=.")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stderr, os.Stderr
	return cmd.Run()
}

// run reads the Go files under root, stages one marker package for each role group, calls
// generate for each package and examines each role. Only then it writes the roles under root.
func run(root string, generate func(dir, name string) error) error {
	sources, err := goSources(root)
	if err != nil {
		return err
	}
	packages, err := manifestlayout.ExtractRBACMarkers(sources)
	if err != nil {
		return err
	}
	want := outputs()
	if len(packages) != len(want) {
		return errNoGroup
	}
	staging, err := os.MkdirTemp("", "koodam-rbac.")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	roles := map[string][]byte{}
	for _, p := range packages {
		out := want[p.Role]
		dir := filepath.Join(staging, string(p.Role))
		if err := os.Mkdir(dir, 0o755); err != nil {
			return err
		}
		// The staged package is a module of its own, thus controller-gen loads no repository code.
		files := map[string][]byte{"go.mod": []byte("module rbacmarkers\n\ngo 1.24\n"), "markers.go": p.Data}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return err
			}
		}
		if err := generate(dir, out.name); err != nil {
			return err
		}
		role, err := os.ReadFile(filepath.Join(dir, "role.yaml"))
		if err != nil {
			return err
		}
		if err := manifestlayout.ValidateRBACRole(role, out.name); err != nil {
			return err
		}
		roles[out.path] = role
	}
	for path, role := range roles {
		dest := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, role, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// goSources reads each Go file under root. It does not read a directory with a name that starts
// with "." or "_", and it does not read bin, which has the downloaded tools.
func goSources(root string) ([]manifestlayout.Source, error) {
	var sources []manifestlayout.Source
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(name, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sources = append(sources, manifestlayout.Source{Path: filepath.ToSlash(rel), Data: data})
		return nil
	})
	return sources, err
}
