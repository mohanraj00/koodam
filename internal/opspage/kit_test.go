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

package opspage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"maps"
	"slices"
	"testing"
)

type pin struct {
	Project, Version, Asset, SHA256, Manifest string
	Files                                     map[string]string
}

// TestKitMatchesPin checks that the embedded kit has exactly the pinned files, each with its
// pinned SHA-256 value.
func TestKitMatchesPin(t *testing.T) {
	var p pin
	if err := json.Unmarshal(Pin, &p); err != nil {
		t.Fatal(err)
	}
	if p.Project != "mohanraj00/mugham" || p.Version != "v0.7.0" || p.Asset != "mugham.tar" || len(p.SHA256) != 64 {
		t.Fatalf("the pin does not name the release asset: %+v", p)
	}
	got := map[string]string{}
	err := fs.WalkDir(files, "static/kit", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(path)
		sum := sha256.Sum256(data)
		got[path[len("static/kit/"):]] = hex.EncodeToString(sum[:])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, p.Files) {
		for _, name := range slices.Sorted(maps.Keys(p.Files)) {
			if got[name] != p.Files[name] {
				t.Errorf("%s: embedded %q, pinned %q", name, got[name], p.Files[name])
			}
		}
		for name := range got {
			if _, ok := p.Files[name]; !ok {
				t.Errorf("%s is embedded but not pinned", name)
			}
		}
	}
	for _, need := range []string{"LICENSE", "src/kit.js", "src/components/index.js", "src/tokens/tokens.css"} {
		if _, ok := p.Files[need]; !ok {
			t.Errorf("the pin does not have %s", need)
		}
	}
}
