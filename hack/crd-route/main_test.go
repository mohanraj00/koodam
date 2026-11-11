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
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mohanraj00/koodam/internal/manifestlayout"
)

// syntheticCRD gives a small valid CRD of the kind.
func syntheticCRD(kind, plural string) []byte {
	return fmt.Appendf(nil, `---
# Synthetic %s CRD for the crd-route test.
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: %s.core.koodam.dev}
spec:
  group: core.koodam.dev
  names: {kind: %s, listKind: %sList, plural: %s, singular: %s}
  scope: Namespaced
  versions: [{name: v1alpha1, served: true, storage: true, schema: {openAPIV3Schema: {type: object}}}]
`, kind, plural, kind, kind, plural, strings.ToLower(kind))
}

func stage(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// tree gives each regular file under root, relative to root, with "/" separators.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func TestRunRoutesAppCRDsOutOfTheDefaultBase(t *testing.T) {
	agent := syntheticCRD("Agent", "agents")
	app := syntheticCRD("App", "apps")
	version := syntheticCRD("AppVersion", "appversions")
	staging := stage(t, map[string][]byte{
		"core.koodam.dev_agents.yaml":      agent,
		"core.koodam.dev_apps.yaml":        app,
		"core.koodam.dev_appversions.yaml": version,
	})
	root := t.TempDir()
	if err := run(root, staging); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{
		"config/apps/crd/bases/core.koodam.dev_apps.yaml",
		"config/apps/crd/bases/core.koodam.dev_appversions.yaml",
		"config/crd/bases/core.koodam.dev_agents.yaml",
	}
	if got := tree(t, root); !slices.Equal(got, want) {
		t.Fatalf("files = %q, want %q", got, want)
	}
	for path, data := range map[string][]byte{want[0]: app, want[1]: version, want[2]: agent} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("%s: bytes changed", path)
		}
	}
}

func TestRunReplacesAnExistingFile(t *testing.T) {
	agent := syntheticCRD("Agent", "agents")
	root := t.TempDir()
	dest := filepath.Join(root, "config", "crd", "bases", "core.koodam.dev_agents.yaml")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(root, stage(t, map[string][]byte{"core.koodam.dev_agents.yaml": agent})); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, agent) {
		t.Fatal("the old file was not replaced")
	}
	if files := tree(t, root); len(files) != 1 {
		t.Fatalf("files = %q, want only the CRD and no temporary file", files)
	}
}

func TestRunWritesNoFileOnBadInput(t *testing.T) {
	agent := syntheticCRD("Agent", "agents")
	for name, tc := range map[string]struct {
		files map[string][]byte
		dir   bool
		want  error
	}{
		"unknown kind": {
			files: map[string][]byte{
				"core.koodam.dev_agents.yaml": agent,
				"core.koodam.dev_others.yaml": syntheticCRD("Other", "others"),
			},
			want: manifestlayout.ErrUnknownKind,
		},
		"wrong file name": {
			files: map[string][]byte{"core.koodam.dev_agents.yaml": agent, "apps.yaml": syntheticCRD("App", "apps")},
			want:  manifestlayout.ErrNameMismatch,
		},
		"not yaml": {
			files: map[string][]byte{"core.koodam.dev_agents.yaml": agent, "notes.txt": []byte("x")},
			want:  errEntry,
		},
		"subdirectory": {
			files: map[string][]byte{"core.koodam.dev_agents.yaml": agent},
			dir:   true,
			want:  errEntry,
		},
		"empty": {want: errNoCRD},
	} {
		t.Run(name, func(t *testing.T) {
			staging := stage(t, tc.files)
			if tc.dir {
				if err := os.Mkdir(filepath.Join(staging, "nested.yaml"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			root := t.TempDir()
			if err := run(root, staging); !errors.Is(err, tc.want) {
				t.Fatalf("run error = %v, want %v", err, tc.want)
			}
			if files := tree(t, root); len(files) != 0 {
				t.Fatalf("files = %q, want none", files)
			}
		})
	}
}
