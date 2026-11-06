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

// Package manifestgen runs the pinned controller-gen to make the generated manifests. It writes
// only to a private temporary directory. It does not write to the repository or to an install path.
package manifestgen

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	// outputDir is the parent of the output directory of each role, in the private directory.
	outputDir = "output"
	roleFile  = "role.yaml"
	// cacheDir is the build cache in the private directory, if GOCACHE is not set. The Go tools
	// ignore a directory with a name that starts with "_".
	cacheDir = "_gocache"
)

// The errors have fixed text. They do not contain a path, a name, source text or generator output.
var (
	ErrInvalidTool     = errors.New("manifestgen: controller-gen path is not an absolute path to an executable file")
	ErrInvalidRoleName = errors.New("manifestgen: role name is not a valid lowercase DNS subdomain name")
	ErrUnknownRole     = errors.New("manifestgen: role group is not known")
	ErrWorkspace       = errors.New("manifestgen: cannot prepare the private generator directory")
	ErrGenerate        = errors.New("manifestgen: controller-gen failed")
	ErrInvalidOutput   = errors.New("manifestgen: controller-gen output is not exactly one role.yaml file with the expected ClusterRole")
)

// passEnv are the trusted toolchain and cache variables that the generator can get. HOME and
// XDG_CONFIG_HOME are not in the list. The go command finds the user configuration directory from
// them and can start a telemetry child process from it before it loads a package. GOCACHE is not
// in the list, because generatorEnv always sets it.
var passEnv = []string{"PATH", "TMPDIR", "XDG_CACHE_HOME", "GOROOT", "GOPATH", "GOMODCACHE"}

// generateRole runs controller-gen one time for one staged role package. It gives the original
// bytes of the role.yaml output after manifestlayout.ValidateRBACRole accepts them.
//
// tool is the absolute path of the pinned controller-gen. dir is the private directory of the
// caller. It must be a clean absolute path to a real directory. The caller stages the package of
// the role group in dir. Only the role group selects the package directory, and only a fixed value
// gives it. The function runs controller-gen without a shell, with fixed arguments and with a fixed
// environment that keeps the Go loader offline. It discards stdout and stderr. The output directory
// must not exist before the run. After the run, it must contain only one regular role.yaml file.
// The function does not change the output.
func generateRole(ctx context.Context, tool, dir string, role manifestlayout.RoleGroup, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validTool(tool) {
		return nil, ErrInvalidTool
	}
	if !validRoleName(name) {
		return nil, ErrInvalidRoleName
	}
	pkg, err := rolePackage(role)
	if err != nil {
		return nil, err
	}
	if !realDir(dir) {
		return nil, ErrWorkspace
	}
	cache, err := buildCache(dir)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(dir, outputDir, pkg)
	if !makeDir(filepath.Join(dir, outputDir)) || os.Mkdir(out, 0o700) != nil {
		return nil, ErrWorkspace
	}
	cmd := exec.CommandContext(ctx, tool, "rbac:roleName="+name, "paths=./"+pkg, "output:rbac:artifacts:config="+outputDir+"/"+pkg)
	cmd.Dir = dir
	cmd.Env = generatorEnv(cache)
	// Stdin, Stdout and Stderr are nil. Thus the child process uses the null device for them.
	if err := cmd.Run(); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, ErrGenerate
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 1 || entries[0].Name() != roleFile || !entries[0].Type().IsRegular() {
		return nil, ErrInvalidOutput
	}
	data, err := os.ReadFile(filepath.Join(out, roleFile))
	if err != nil || manifestlayout.ValidateRBACRole(data, name) != nil {
		return nil, ErrInvalidOutput
	}
	return data, nil
}

// rolePackage gives the package directory of the role group. Only fixed values give the
// directory. Thus a role group cannot select a different path.
func rolePackage(role manifestlayout.RoleGroup) (string, error) {
	switch role {
	case manifestlayout.RoleBase:
		return "base", nil
	case manifestlayout.RoleApp:
		return "app", nil
	}
	return "", ErrUnknownRole
}

// buildCache gives the GOCACHE value of the generator. It is the GOCACHE value of this process if
// it is set. If not, it is a private cache in dir. The cache must be a real directory.
func buildCache(dir string) (string, error) {
	if v := os.Getenv("GOCACHE"); v != "" {
		return v, nil
	}
	cache := filepath.Join(dir, cacheDir)
	if !makeDir(cache) {
		return "", ErrWorkspace
	}
	return cache, nil
}

// makeDir makes the directory p if it does not exist. It is true if p is then a real directory.
func makeDir(p string) bool {
	if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return false
	}
	return realDir(p)
}

// realDir is true for a clean absolute path to a directory. The last element must not be a
// symbolic link.
func realDir(p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// generatorEnv gives the full environment of controller-gen. The Go loader settings keep it
// offline and stop it from reading other configuration. GOCACHE is always the cache value. Only the
// passEnv variables come from the environment of this process.
func generatorEnv(cache string) []string {
	env := []string{"GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOPACKAGESDRIVER=off",
		"GOCACHE=" + cache}
	for _, k := range passEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// validTool is true for a clean absolute path to a regular file with an execute permission.
func validTool(p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// validRoleName accepts a DNS subdomain name that starts with a lowercase letter. Thus the name
// has no character that controller-gen reads as a marker separator.
func validRoleName(n string) bool {
	return n != "" && n[0] >= 'a' && n[0] <= 'z' && len(validation.IsDNS1123Subdomain(n)) == 0
}
