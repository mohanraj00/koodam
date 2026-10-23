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

package harness

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type helperAsset struct {
	body, archive                   []byte
	edit                            func(*fakeOpts)
	noBundle, noArchive, wrongRekor bool
}

func newHelperAssets(t *testing.T, arch string) map[string]*helperAsset {
	t.Helper()
	assets := map[string]*helperAsset{}
	for _, name := range []string{codeModeHost, bubblewrap} {
		body := []byte(name + " binary")
		assets[name] = &helperAsset{body: body, archive: tgz(t, name+"-"+arch+"-unknown-linux-musl", body)}
	}
	return assets
}

// serveHelper gives each helper its own bundle and Rekor entry. No signing key is saved.
func (v *vendor) serveHelper(t *testing.T, w http.ResponseWriter, r *http.Request, body *string) bool {
	t.Helper()
	for name, a := range v.helpers {
		rel := "/dl/rust-v0.9.1/" + name + "-" + v.arch + "-unknown-linux-musl"
		switch r.URL.Path {
		case rel + ".sigstore":
			if a.noBundle {
				http.NotFound(w, r)
				return true
			}
			bundle, entry := v.sigstore.bundle(t, sum(a.body), a.edit)
			*body = entry
			if a.wrongRekor {
				*body = "e30="
			}
			_, _ = fmt.Fprint(w, bundle)
			return true
		case rel + ".tar.gz":
			if a.noArchive {
				http.NotFound(w, r)
				return true
			}
			_, _ = w.Write(a.archive)
			return true
		}
	}
	return false
}

func TestCodexHelpers(t *testing.T) {
	for _, arch := range []struct{ goarch, target, version string }{
		{amd64, codexX86, ""}, {amd64, codexX86, codexTestVersion},
		{arm64, codexARM, ""}, {arm64, codexARM, codexTestVersion},
	} {
		t.Run(arch.goarch+"/"+arch.version, func(t *testing.T) {
			v := newVendor(t)
			v.arch = arch.target
			v.codex = tgz(t, "codex-"+v.arch+"-unknown-linux-musl", v.codexBin)
			v.helpers = newHelperAssets(t, v.arch)
			if _, err := v.install(arch.goarch, Codex, arch.version, ""); err != nil {
				t.Fatal(err)
			}
			expectInstalled(t, v, Codex)
			for name, a := range v.helpers {
				expectBinary(t, filepath.Join(v.installDir, name), a.body)
			}
		})
	}
}

func TestCodexHelperChecks(t *testing.T) {
	for _, name := range []string{codeModeHost, bubblewrap} {
		for _, c := range []struct {
			name, want string
			edit       func(*testing.T, *helperAsset)
		}{
			{"missing bundle", "404", func(_ *testing.T, a *helperAsset) { a.noBundle = true }},
			{"bad signature", "the bundle signature is not valid", func(_ *testing.T, a *helperAsset) {
				a.edit = func(o *fakeOpts) { o.badSig = true }
			}},
			{"wrong workflow", identity, func(_ *testing.T, a *helperAsset) {
				a.edit = func(o *fakeOpts) { o.uri = strings.Replace(o.uri, "rust-release", "other-release", 1) }
			}},
			{"wrong tag", identity, func(_ *testing.T, a *helperAsset) {
				a.edit = func(o *fakeOpts) { o.uri += "1" }
			}},
			{"wrong issuer", "certificate issuer is", func(_ *testing.T, a *helperAsset) {
				a.edit = func(o *fakeOpts) { o.issuerV2 = "https://example.com" }
			}},
			{"bad SET", "signed entry timestamp", func(_ *testing.T, a *helperAsset) {
				a.edit = func(o *fakeOpts) { o.badSET = true }
			}},
			{"wrong Rekor entry", "not the entry in the bundle", func(_ *testing.T, a *helperAsset) { a.wrongRekor = true }},
			{"wrong sha256", "the sha256", func(_ *testing.T, a *helperAsset) {
				a.body = []byte("a different binary")
			}},
			{"missing archive", "404", func(_ *testing.T, a *helperAsset) { a.noArchive = true }},
			{"wrong archive member", "no file", func(t *testing.T, a *helperAsset) {
				a.archive = tgz(t, "other-binary", a.body)
			}},
		} {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				v := newVendor(t)
				c.edit(t, v.helpers[name])
				_, err := v.install(amd64, Codex, codexTestVersion, "")
				if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("install error = %v, want %s: %s", err, name, c.want)
				}
				if files := v.files(t); len(files) != 0 {
					t.Fatalf("a failed check left %v", files)
				}
			})
		}
	}
}

func TestCodexHelperFailureKeepsInstalledFiles(t *testing.T) {
	v := newVendor(t)
	names := []string{realName(Codex), codeModeHost, bubblewrap, Codex, projectScript}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(v.installDir, name), []byte("previous "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	v.helpers[bubblewrap].noBundle = true
	if _, err := v.install(amd64, Codex, codexTestVersion, ""); err == nil || !strings.Contains(err.Error(), bubblewrap) {
		t.Fatal("the missing bwrap bundle did not stop the install")
	}
	if len(v.files(t)) != len(names) {
		t.Fatal("the failed install left temporary files")
	}
	for _, name := range names {
		expectBinary(t, filepath.Join(v.installDir, name), []byte("previous "+name))
	}
}

// A valid signature for the CLI must not fill a helper slot with the same binary.
func TestCodexHelperRejectsRepeatedDigest(t *testing.T) {
	for _, name := range []string{codeModeHost, bubblewrap} {
		t.Run(name, func(t *testing.T) {
			v := newVendor(t)
			a := v.helpers[name]
			a.body = v.codexBin
			a.archive = tgz(t, name+"-"+v.arch+"-unknown-linux-musl", a.body)
			_, err := v.install(amd64, Codex, codexTestVersion, "")
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "also used by") {
				t.Fatalf("install error = %v, want a repeated digest error for %s", err, name)
			}
			if files := v.files(t); len(files) != 0 {
				t.Fatalf("a repeated digest left %v", files)
			}
		})
	}
}
