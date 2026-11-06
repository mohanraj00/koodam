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

	"github.com/mohanraj00/koodam/internal/manifestlayout"
)

const (
	// goMod is the temporary module. It has no dependencies. The go directive is the same as the
	// go directive of the repository.
	goMod = "module rbacgen\n\ngo 1.26.0\n"
	// goModFile is the name of the module file in the private directory.
	goModFile = "go.mod"
	// markerFile is the name of the one Go file in each staged package.
	markerFile = "rbac.go"
)

// ErrCleanup has fixed text. It does not contain a path.
var ErrCleanup = errors.New("manifestgen: cannot remove the private generator directory")

// RoleNames are the ClusterRole names for the role groups. Only a trusted caller gives them.
type RoleNames struct {
	Base string
	App  string
}

// RoleManifest is the controller-gen output for one role group.
type RoleManifest struct {
	Role manifestlayout.RoleGroup
	// Name is the ClusterRole name.
	Name string
	// Data is the original role.yaml content from controller-gen. The bytes are not changed.
	Data []byte
}

// GenerateRBACRoles gives the controller-gen ClusterRole for each role group that has RBAC
// markers, in the order of the source index. A role group without markers gives no output. If no
// role group has markers, the result is empty and not nil, and controller-gen does not run.
//
// controllerGen is the absolute path of the pinned controller-gen. The function extracts the
// markers with manifestlayout.ExtractRBACMarkers and writes each staged package without change to
// one package of a temporary module in a private directory. Then it calls generateRole one time
// for each package. generateRole validates each output. If one step fails, also the removal of the
// private directory, the function gives no output and an error. It does not change the input.
func GenerateRBACRoles(ctx context.Context, controllerGen string, names RoleNames, sources []manifestlayout.Source) ([]RoleManifest, error) {
	return generateRoles(ctx, controllerGen, names, sources, os.RemoveAll)
}

// generateRoles does the work of GenerateRBACRoles. remove removes the private directory. A test
// can use it to examine the directory or to make the removal fail.
func generateRoles(ctx context.Context, tool string, names RoleNames, sources []manifestlayout.Source,
	remove func(string) error) (out []RoleManifest, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validTool(tool) {
		return nil, ErrInvalidTool
	}
	if !validRoleName(names.Base) || !validRoleName(names.App) || names.Base == names.App {
		return nil, ErrInvalidRoleName
	}
	pkgs, err := manifestlayout.ExtractRBACMarkers(sources)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return []RoleManifest{}, nil
	}
	dir, err := os.MkdirTemp("", "koodam-rbac-")
	if err != nil {
		return nil, ErrWorkspace
	}
	defer func() {
		if remove(dir) != nil && err == nil {
			err = ErrCleanup
		}
		if err != nil {
			out = nil
		}
	}()
	if err := stageRoles(dir, pkgs); err != nil {
		return nil, err
	}
	out = make([]RoleManifest, 0, len(pkgs))
	for _, p := range pkgs {
		name := roleName(p.Role, names)
		data, err := generateRole(ctx, tool, dir, p.Role, name)
		if err != nil {
			return nil, err
		}
		out = append(out, RoleManifest{Role: p.Role, Name: name, Data: data})
	}
	return out, nil
}

// stageRoles writes the fixed module and each staged package to dir. Only rolePackage gives a
// package directory. The function does not make an output directory, because generateRole must
// make it.
func stageRoles(dir string, pkgs []manifestlayout.MarkerPackage) error {
	if os.WriteFile(filepath.Join(dir, goModFile), []byte(goMod), 0o600) != nil {
		return ErrWorkspace
	}
	for _, p := range pkgs {
		pkg, err := rolePackage(p.Role)
		if err != nil {
			return err
		}
		if os.Mkdir(filepath.Join(dir, pkg), 0o700) != nil ||
			os.WriteFile(filepath.Join(dir, pkg, markerFile), p.Data, 0o600) != nil {
			return ErrWorkspace
		}
	}
	return nil
}

// roleName gives the ClusterRole name of the role group. A role group that is not known gives "".
// generateRole does not accept this value.
func roleName(role manifestlayout.RoleGroup, names RoleNames) string {
	switch role {
	case manifestlayout.RoleBase:
		return names.Base
	case manifestlayout.RoleApp:
		return names.App
	}
	return ""
}
