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

package outputdigest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(file, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The digest of "hello\n" from sha256sum.
	want := Result{SHA256: "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03", Size: 6}
	if got := File(file); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for path, reason := range map[string]string{
		filepath.Join(dir, "none"): ReasonNotFound,
		dir:                        ReasonNotRegularFile,
		link:                       ReasonNotRegularFile,
	} {
		if got := File(path); got != (Result{Reason: reason}) {
			t.Errorf("%s: got %+v, want the reason %s", path, got, reason)
		}
	}
}

func TestParseAcceptsOnlyAResultOfThisPackage(t *testing.T) {
	good := Result{SHA256: "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03", Size: 6}
	if got, ok := Parse(string(good.Encode())); !ok || got != good {
		t.Fatalf("got %+v %v", got, ok)
	}
	if got, ok := Parse(`{"reason":"NotFound","sha256":"x"}`); !ok || got != (Result{Reason: ReasonNotFound}) {
		t.Fatalf("got %+v %v", got, ok)
	}
	for _, bad := range []string{"", "not json", `{"sha256":"ABC"}`, `{"sha256":"5891"}`, `{"reason":"Other"}`,
		`{"sha256":"5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03","size":-1}`} {
		if _, ok := Parse(bad); ok {
			t.Errorf("Parse accepted %q", bad)
		}
	}
}
