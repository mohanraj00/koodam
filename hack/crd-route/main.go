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

// Command crd-route moves the CRDs that controller-gen wrote to a staging directory to their
// destinations in the repository. The closed kind index in internal/manifestlayout gives each
// destination. Thus the App and AppVersion CRDs go to config/apps/crd/bases, and the default
// install in config/crd/bases has no App CRD.
//
// Usage, from the repository root: go run ./hack/crd-route STAGING_DIR
//
// If one staged file is not a valid CRD of a known kind, the command writes no file.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mohanraj00/koodam/internal/manifestlayout"
)

var (
	errUsage    = errors.New("crd-route: usage: crd-route STAGING_DIR")
	errEntry    = errors.New("crd-route: the staging directory must contain only regular .yaml files")
	errNoCRD    = errors.New("crd-route: the staging directory has no CRD")
	errBadRoute = errors.New("crd-route: a destination is not a relative path in the repository")
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, errUsage)
		os.Exit(2)
	}
	if err := run(".", os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run reads each CRD in staging, routes all of them, and only then writes them under root.
func run(root, staging string) error {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	artifacts := make([]manifestlayout.Artifact, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".yaml") {
			return errEntry
		}
		data, err := os.ReadFile(filepath.Join(staging, e.Name()))
		if err != nil {
			return err
		}
		artifacts = append(artifacts, manifestlayout.Artifact{Name: e.Name(), Data: data})
	}
	if len(artifacts) == 0 {
		return errNoCRD
	}
	plan, err := manifestlayout.RouteCRDs(artifacts)
	if err != nil {
		return err
	}
	for _, p := range plan {
		if !filepath.IsLocal(filepath.FromSlash(p.Path)) {
			return errBadRoute
		}
	}
	for _, p := range plan {
		if err := writeFile(filepath.Join(root, filepath.FromSlash(p.Path)), p.Data); err != nil {
			return err
		}
	}
	return nil
}

// writeFile writes data to a temporary file in the destination directory, then renames it.
// Thus a reader never sees a partial CRD file.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	t, err := os.CreateTemp(dir, "."+filepath.Base(path)+".new-*")
	if err != nil {
		return err
	}
	n, err := t.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = t.Chmod(0o644)
	}
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(t.Name(), path)
	}
	if err != nil {
		_ = os.Remove(t.Name())
	}
	return err
}
