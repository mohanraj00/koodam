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
	"errors"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

// markerPackage is the package name of each staged marker package.
const markerPackage = "rbacmarkers"

// The errors have fixed text. They do not contain a path, source text or marker data.
var (
	ErrUnsupportedMarker  = errors.New("manifestlayout: RBAC marker is not in a supported package-level placement")
	ErrUnsupportedContext = errors.New("manifestlayout: source file with an RBAC marker has a build constraint, a generated header or cgo")
)

// MarkerPackage is the staged Go package source for the RBAC markers of one role group.
type MarkerPackage struct {
	// Role is the role group. Only the source index gives this value.
	Role RoleGroup
	// Data is the full content of one Go file. The markers are the package documentation comment,
	// directly before the package clause.
	Data []byte
}

// ExtractRBACMarkers gives one staged Go package source for each role group that has RBAC
// markers, in the order of the source index. The markers are in the order of the index and then
// in the order of the source file. Each marker line is a copy of the original comment text. This
// function does not interpret the permission fields. It does not change the input or the source
// index, and it does not read files.
//
// It accepts only one marker placement. That placement is a comment group at file scope, after
// the preceding top-level declaration, and on lines that are not the lines of that declaration.
// One blank line separates the group from the documentation comment of a top-level function or
// method. Each line of the group is "// +kubebuilder:rbac:" and the arguments, at column 1, with
// only printable ASCII characters and no spaces in the arguments. controller-tools v0.22.0 reads
// such a group as package-level markers. If one marker has a different placement or form, or one
// input is not valid, it gives no result and an error.
func ExtractRBACMarkers(sources []Source) ([]MarkerPackage, error) {
	plan, err := RouteRBACSources(sources)
	if err != nil {
		return nil, err
	}
	roles := make([]RoleGroup, 0, len(plan))
	lines := make([][]string, 0, len(plan))
	for _, p := range plan {
		found, err := packageMarkers(p.Data)
		if err != nil {
			return nil, err
		}
		i := slices.Index(roles, p.Role)
		if i < 0 {
			roles = append(roles, p.Role)
			lines = append(lines, nil)
			i = len(roles) - 1
		}
		lines[i] = append(lines[i], found...)
	}
	out := make([]MarkerPackage, 0, len(roles))
	for i, role := range roles {
		var b strings.Builder
		for _, line := range lines[i] {
			b.WriteString(line + "\n")
		}
		b.WriteString("package " + markerPackage + "\n")
		out = append(out, MarkerPackage{Role: role, Data: []byte(b.String())})
	}
	return out, nil
}

// packageMarkers gives the text of each RBAC marker comment of the source, in the source order.
// Each comment that rbacMarkerComment finds must be in a supported group. It does not use the
// parser error, thus no source text goes into the result.
func packageMarkers(src []byte) (found []string, err error) {
	// A panic in the parser is malformed input.
	defer func() {
		if recover() != nil {
			found, err = nil, ErrMalformedSource
		}
	}()
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, "source.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if perr != nil {
		return nil, ErrMalformedSource
	}
	if !plainPackageFile(file) {
		return nil, ErrUnsupportedContext
	}
	accepted := supportedGroups(fset, file, src)
	for _, g := range file.Comments {
		for _, c := range g.List {
			if !rbacMarkerComment(c.Text) {
				continue
			}
			if !slices.Contains(accepted, g) {
				return nil, ErrUnsupportedMarker
			}
			found = append(found, c.Text)
		}
	}
	return found, nil
}

// plainPackageFile is false if the file has a build constraint before the package clause, has a
// generated-code header, or imports "C". Each of these can remove the file from the package that
// controller-gen loads. Then a staged copy of its markers adds permissions.
func plainPackageFile(file *ast.File) bool {
	if ast.IsGenerated(file) {
		return false
	}
	for _, g := range file.Comments {
		if g.Pos() > file.Package {
			break
		}
		for _, c := range g.List {
			if constraint.IsGoBuild(c.Text) || constraint.IsPlusBuild(c.Text) {
				return false
			}
		}
	}
	return !slices.ContainsFunc(file.Imports, func(s *ast.ImportSpec) bool {
		p, err := strconv.Unquote(s.Path.Value)
		return err != nil || p == "C"
	})
}

// supportedGroups gives the comment groups with the supported marker placement. It uses the
// lines of the file without the "//line" directives.
func supportedGroups(fset *token.FileSet, file *ast.File, src []byte) []*ast.CommentGroup {
	pos := func(p token.Pos) token.Position { return fset.PositionFor(p, false) }
	tf := fset.File(file.Package)
	out := make([]*ast.CommentGroup, 0, len(file.Decls))
	for k, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Doc == nil {
			continue
		}
		// The group must come directly before the documentation comment.
		i := slices.Index(file.Comments, fn.Doc)
		if i < 1 {
			continue
		}
		g := file.Comments[i-1]
		prev := file.Name.End()
		if k > 0 {
			prev = file.Decls[k-1].End()
		}
		// The group starts on a line after the preceding declaration. One blank line comes after it.
		if g.Pos() < prev || pos(g.Pos()).Line <= pos(prev).Line || pos(fn.Doc.Pos()).Line != pos(g.End()).Line+2 {
			continue
		}
		first := pos(g.Pos()).Line
		exact := func(j int, c *ast.Comment) bool {
			p := pos(c.Slash)
			end := tf.Offset(c.End())
			// The parser removes a "\r" from the comment text, thus the next byte must be "\n".
			return p.Line == first+j && p.Column == 1 && exactRBACMarker(c.Text) && end < len(src) && src[end] == '\n'
		}
		all := true
		for j, c := range g.List {
			all = all && exact(j, c)
		}
		if all {
			out = append(out, g)
		}
	}
	return out
}

// exactRBACMarker is true only for "// +kubebuilder:rbac:" and the arguments. The arguments are
// not empty and have only printable ASCII characters that are not spaces.
func exactRBACMarker(text string) bool {
	args, ok := strings.CutPrefix(text, "// "+rbacMarker+":")
	return ok && args != "" && !strings.ContainsFunc(args, func(c rune) bool { return c <= ' ' || c > '~' })
}
