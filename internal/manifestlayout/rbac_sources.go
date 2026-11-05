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

package manifestlayout

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"slices"
	"strings"
)

// RoleGroup is the name of one generated controller role.
type RoleGroup string

const (
	// RoleBase is the role of the default install.
	RoleBase RoleGroup = "base"
	// RoleApp is the role of the manual App install.
	RoleApp RoleGroup = "app"

	rbacMarker      = "+kubebuilder:rbac"
	sourcePathChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
)

// The errors have fixed text. They do not contain a path, source text or marker data.
var (
	ErrUnsafeSourcePath = errors.New("manifestlayout: source path is not a safe repository-relative Go file path")
	ErrDuplicateSource  = errors.New("manifestlayout: two source files have the same path")
	ErrMalformedSource  = errors.New("manifestlayout: source file is not valid Go")
	ErrUnindexedSource  = errors.New("manifestlayout: source file has an RBAC marker but is not in the source index")
)

// Source is one Go source file of the repository.
type Source struct {
	// Path is relative to the repository root, with "/" separators.
	Path string
	// Data is the full content of the file.
	Data []byte
}

// SourcePlacement is one source file with an RBAC marker and its role group.
type SourcePlacement struct {
	// Role is the role group. Only the source index gives this value.
	Role RoleGroup
	// Path is the path of the source file.
	Path string
	// Data is a copy of the source content. The bytes are the same.
	Data []byte
}

type sourceRoute struct {
	path string
	role RoleGroup
}

// sourceIndex gives the closed source-file index in the order of the plan. Each call makes a new
// slice, thus no caller can change the index. An App path is only an index entry. It is not a
// controller and it gives no permission.
func sourceIndex() []sourceRoute {
	return []sourceRoute{
		{path: "internal/controller/agent_controller.go", role: RoleBase},
		{path: "internal/controller/profile_controller.go", role: RoleBase},
		{path: "internal/controller/workspace_controller.go", role: RoleBase},
		{path: "internal/controller/app_controller.go", role: RoleApp},
		{path: "internal/controller/appversion_controller.go", role: RoleApp},
	}
}

// RouteRBACSources gives the role group of each source file that has an RBAC marker comment, in
// the order of the source index. A file without such a comment is not in the plan. If a file
// with such a comment is not in the index, or one input is not valid, it gives no plan and an
// error. It does not change the input, read files or examine imports. It does not parse, move or
// change the markers.
func RouteRBACSources(sources []Source) ([]SourcePlacement, error) {
	index := sourceIndex()
	found := make([]*SourcePlacement, len(index))
	for i, s := range sources {
		if !safeSourcePath(s.Path) {
			return nil, ErrUnsafeSourcePath
		}
		// A file system can ignore the case, thus two paths that differ only in case are the same.
		if slices.ContainsFunc(sources[:i], func(o Source) bool { return strings.EqualFold(o.Path, s.Path) }) {
			return nil, ErrDuplicateSource
		}
		marked, err := hasRBACMarker(s.Data)
		if err != nil {
			return nil, err
		}
		if !marked {
			continue
		}
		j := slices.IndexFunc(index, func(r sourceRoute) bool { return r.path == s.Path })
		if j < 0 {
			return nil, ErrUnindexedSource
		}
		found[j] = &SourcePlacement{Role: index[j].role, Path: s.Path, Data: bytes.Clone(s.Data)}
	}
	plan := make([]SourcePlacement, 0, len(found))
	for _, p := range found {
		if p != nil {
			plan = append(plan, *p)
		}
	}
	return plan, nil
}

// safeSourcePath accepts only a relative path with "/" separators that ends in ".go". Each part
// must have content, must not start with "." and must use only letters, digits, "." "_" and "-".
// Thus an absolute path, "." and ".." parts, a backslash and a directory path are not permitted.
func safeSourcePath(p string) bool {
	bad := func(c rune) bool { return !strings.ContainsRune(sourcePathChars, c) }
	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part[0] == '.' || strings.ContainsFunc(part, bad) {
			return false
		}
	}
	return strings.HasSuffix(p, ".go")
}

// hasRBACMarker parses the Go source and examines each comment in the file. It does not use the
// parser error, thus no source text goes into the result.
func hasRBACMarker(src []byte) (marked bool, err error) {
	// A panic in the parser is malformed input.
	defer func() {
		if recover() != nil {
			marked, err = false, ErrMalformedSource
		}
	}()
	file, perr := parser.ParseFile(token.NewFileSet(), "source.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if perr != nil {
		return false, ErrMalformedSource
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if rbacMarkerComment(c.Text) {
				return true, nil
			}
		}
	}
	return false, nil
}

// rbacMarkerComment uses the rules of controller-tools v0.22.0. A marker comment is a "//"
// comment, and its first content after spaces is "+". Thus a block comment and a string are not
// marker comments. The RBAC marker name is "kubebuilder:rbac", and the end, ":" or "=" comes
// after it. This check also accepts a name with more ":" parts. It examines each comment in the
// file, also a comment that controller-gen does not attach to the package. Thus it finds each
// marker that controller-gen can read as an RBAC marker, and possibly more.
func rbacMarkerComment(text string) bool {
	body, ok := strings.CutPrefix(text, "//")
	if !ok {
		return false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(body), rbacMarker)
	return ok && (rest == "" || rest[0] == ':' || rest[0] == '=')
}
