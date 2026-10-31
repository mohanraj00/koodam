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

// These tests start from the two real Agent CRDs of make manifests. They only read these files.
// The write tests use only files in t.TempDir.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	// agentFile and enabledFile are the two outputs of the command in make manifests.
	agentFile   = "../../config/crd/bases/core.koodam.dev_agents.yaml"
	enabledFile = "../../config/agent-ingress/crd/bases/core.koodam.dev_agents.yaml"
	// immutableMessage is the same as the AgentSpec marker.
	immutableMessage = "spec.ingressPort is immutable. You cannot add, remove or change it after create."

	// defaultName and enabledName are the names of the targets in the write tests.
	defaultName = "default.yaml"
	enabledName = "enabled.yaml"
)

type (
	schema = apiextensionsv1.JSONSchemaProps
	rule   = apiextensionsv1.ValidationRule
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParse(t *testing.T, b []byte) *agentCRD {
	t.Helper()
	crd, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return crd
}

func mustMarshal(t *testing.T, crd *agentCRD) []byte {
	t.Helper()
	b, err := marshal(crd)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustVariants(t *testing.T, in []byte) (def, en []byte) {
	t.Helper()
	def, en, err := variants(in)
	if err != nil {
		t.Fatal(err)
	}
	return def, en
}

// editSpec returns an edit of the AgentSpec schema.
func editSpec(f func(*schema)) func(*agentCRD) {
	return func(crd *agentCRD) {
		s := props(crd)[specField]
		f(&s)
		props(crd)[specField] = s
	}
}

func editRules(f func([]rule) []rule) func(*agentCRD) {
	return editSpec(func(s *schema) { s.XValidations = f(s.XValidations) })
}

// fixture returns the real default and enabled Agent CRDs, and the controller-gen form of them.
// The test makes that form from the default CRD with no help from the command: it removes the mode
// and the last rule, which must be the gate.
func fixture(t *testing.T) (def, en, fresh []byte) {
	t.Helper()
	def, en = read(t, agentFile), read(t, enabledFile)
	crd := mustParse(t, def)
	delete(crd.Metadata.Annotations, modeKey)
	editRules(func(rs []rule) []rule { return rs[:len(rs)-1] })(crd)
	return def, en, mustMarshal(t, crd)
}

func TestVariantsAreStable(t *testing.T) {
	def, en, fresh := fixture(t)

	// The two files are the output for the controller-gen form. A second run on the default
	// variant makes no change.
	for _, in := range [][]byte{fresh, def} {
		def2, en2 := mustVariants(t, in)
		if !bytes.Equal(def2, def) || !bytes.Equal(en2, en) {
			t.Error("the generated CRDs are not the current output. Run make manifests")
		}
	}
}

func TestOnlyTheGateAndTheModeAreDifferent(t *testing.T) {
	defB, enB, in := fixture(t)
	def, en := mustParse(t, defB), mustParse(t, enB)
	if def.Metadata.Annotations[modeKey] != disabled || en.Metadata.Annotations[modeKey] != enabled {
		t.Fatal("the mode annotations are not disabled and enabled")
	}
	if rs := props(def)[specField].XValidations; rs[len(rs)-1] != gate {
		t.Fatal("the last rule of the default CRD is not the disabled gate")
	}
	// The immutability rule also has the text !has(self.ingressPort). Thus compare whole rules, not bytes.
	for _, r := range props(en)[specField].XValidations {
		if r.Rule == gateRule || r.Message == gateMessage || strings.Contains(r.Message, "spec.ingressPort is disabled") {
			t.Fatal("the enabled CRD has the disabled gate")
		}
	}

	// Both variants have the API field and its immutability rule.
	for _, crd := range []*agentCRD{def, en} {
		s := props(crd)[specField]
		if checkPort(s) != nil || !slices.Contains(s.XValidations, rule{Rule: immutableRule, Message: immutableMessage}) {
			t.Fatal("a variant does not have spec.ingressPort and its immutability rule")
		}
	}

	// The enabled variant keeps the full pod template and its shared rules.
	tmpl := props(en)[specField].Properties["podTemplate"]
	if _, ok := tmpl.Properties[specField].Properties["initContainers"]; !ok ||
		!strings.Contains(fmt.Sprint(tmpl.XValidations), "has(self.spec.ephemeralContainers)") {
		t.Fatal("the enabled CRD does not have the full pod template")
	}

	// With no mode, the enabled variant is the input byte for byte. This keeps each default and
	// each raw value of the schema.
	delete(en.Metadata.Annotations, modeKey)
	if !bytes.Equal(mustMarshal(t, en), in) {
		t.Fatal("the enabled CRD changed the schema of the input")
	}
	if n := bytes.Count(in, []byte("default:")); n == 0 ||
		bytes.Count(enB, []byte("default:")) != n || bytes.Count(defB, []byte("default:")) != n {
		t.Fatal("the variants do not keep all schema defaults")
	}

	// With no gate and the same mode, the two variants are equal.
	editRules(func(rs []rule) []rule { return rs[:len(rs)-1] })(def)
	def.Metadata.Annotations[modeKey] = enabled
	if en := mustParse(t, enB); !reflect.DeepEqual(def, en) || !bytes.Equal(mustMarshal(t, def), enB) {
		t.Fatal("the variants have more differences than the gate and the mode")
	}
}

func TestRefusesUnexpectedInput(t *testing.T) {
	in, _, _ := fixture(t)
	edits := map[string]func(*agentCRD){
		"unknown mode":   func(c *agentCRD) { c.Metadata.Annotations[modeKey] = "on" },
		"duplicate gate": editRules(func(rs []rule) []rule { return append(rs, gate) }),
		"changed gate":   editRules(func(rs []rule) []rule { return append(rs[:len(rs)-1], rule{Rule: gateRule}) }),
		"no port":        editSpec(func(s *schema) { delete(s.Properties, portField) }),
		"port default": editSpec(func(s *schema) {
			p := s.Properties[portField]
			p.Default = &apiextensionsv1.JSON{Raw: []byte("8080")}
			s.Properties[portField] = p
		}),
		"no immutability rule": editRules(func(rs []rule) []rule {
			return slices.DeleteFunc(rs, func(r rule) bool { return r.Rule == immutableRule })
		}),
	}
	for name, edit := range edits {
		crd := mustParse(t, in)
		edit(crd)
		if _, _, err := variants(mustMarshal(t, crd)); err == nil {
			t.Errorf("%s: the command accepted the input", name)
		}
	}
	// The schema types decode items with no strict check. The encode check in parse must find this field.
	i := bytes.Index(in, []byte("items:\n")) + len("items:\n")
	indent := len(in[i:]) - len(bytes.TrimLeft(in[i:], " "))
	raw := map[string][]byte{
		"unknown items field": slices.Concat(in[:i], []byte(strings.Repeat(" ", indent)+"zz: x\n"), in[i:]),
		"two documents":       slices.Concat(in, in),
	}
	for name, b := range raw {
		if _, _, err := variants(b); err == nil {
			t.Errorf("%s: the command accepted the input", name)
		}
	}
}

// The default Kustomize CRDs use the default variant. helm-check makes sure that the Helm CRDs are
// a copy of them. No other file of config or of the chart uses the enabled variant.
func TestDefaultOutputsHaveOnlyTheDisabledVariant(t *testing.T) {
	kust := read(t, "../../config/crd/kustomization.yaml")
	if !bytes.Contains(kust, []byte("\n- bases/core.koodam.dev_agents.yaml\n")) {
		t.Error("the default Kustomize CRDs do not use the default Agent CRD")
	}
	enabledMode := []byte(modeKey + ": " + enabled)
	if !bytes.Contains(read(t, enabledFile), enabledMode) {
		t.Fatal("the enabled CRD does not have the enabled mode")
	}
	ingressDir := filepath.Join("..", "..", "config", "agent-ingress")
	for _, root := range []string{"../../config", "../../charts/koodam"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && path == ingressDir:
				return filepath.SkipDir
			case d.IsDir():
				return nil
			}
			b := read(t, path)
			if bytes.Contains(b, enabledMode) || bytes.Contains(b, []byte("agent-ingress/crd")) {
				t.Errorf("%s uses the enabled Agent CRD", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

var (
	errFail    = errors.New("injected failure")
	oldEnabled = []byte("old enabled\n")
)

// failRename makes the given calls of rename fail.
func failRename(t *testing.T, fail ...int) {
	calls := 0
	t.Cleanup(func() { rename = os.Rename })
	rename = func(a, b string) error {
		if calls++; slices.Contains(fail, calls) {
			return errFail
		}
		return os.Rename(a, b)
	}
}

// failWrite makes call n of write fail, or write one byte less with no error.
func failWrite(t *testing.T, n int, short bool) {
	calls := 0
	t.Cleanup(func() { write = (*os.File).Write })
	write = func(f *os.File, b []byte) (int, error) {
		if calls++; calls != n {
			return f.Write(b)
		}
		if short {
			return f.Write(b[:len(b)-1])
		}
		return 0, errFail
	}
}

// targets returns a directory with the default input and an old enabled CRD.
func targets(t *testing.T) (dir, def, en string, in []byte) {
	t.Helper()
	_, _, in = fixture(t)
	dir = t.TempDir()
	def, en = filepath.Join(dir, defaultName), filepath.Join(dir, enabledName)
	if err := os.WriteFile(def, in, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(en, oldEnabled, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, def, en, in
}

// wantFiles checks that dir has only the files in want, with their content.
func wantFiles(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != len(want) {
		t.Errorf("%s has %d files, want %d", dir, len(ents), len(want))
	}
	for name, b := range want {
		if !bytes.Equal(read(t, filepath.Join(dir, name)), b) {
			t.Errorf("%s does not have the expected content", name)
		}
	}
}

func TestRunWritesBothVariants(t *testing.T) {
	dir, def, en, in := targets(t)
	if err := os.Remove(en); err != nil {
		t.Fatal(err)
	}
	if err := run(def, en); err != nil {
		t.Fatal(err)
	}
	wantDef, wantEn := mustVariants(t, in)
	wantFiles(t, dir, map[string][]byte{defaultName: wantDef, enabledName: wantEn})

	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(def, link); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{filepath.Join(dir, ".", defaultName), link} {
		if err := run(def, alias); err == nil {
			t.Errorf("%s: the command accepted the same file for both variants", alias)
		}
	}
	wantFiles(t, dir, map[string][]byte{defaultName: wantDef, enabledName: wantEn, "link.yaml": wantDef})
}

// The writes are: the default original, the default variant, the enabled original, the enabled
// variant. The renames are: the default variant, the enabled variant, the put back of the default.
func TestWriteFailureKeepsBothOriginals(t *testing.T) {
	cases := map[string]struct {
		inject func(*testing.T)
		want   error
	}{
		"write error":   {func(t *testing.T) { failWrite(t, 4, false) }, errFail},
		"short write":   {func(t *testing.T) { failWrite(t, 2, true) }, io.ErrShortWrite},
		"first rename":  {func(t *testing.T) { failRename(t, 1) }, errFail},
		"second rename": {func(t *testing.T) { failRename(t, 2) }, errFail},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, def, en, in := targets(t)
			c.inject(t)
			if err := run(def, en); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			wantFiles(t, dir, map[string][]byte{defaultName: in, enabledName: oldEnabled})
		})
	}
}

func TestFailedRollbackKeepsTheOriginals(t *testing.T) {
	dir, def, en, in := targets(t)
	failRename(t, 2, 3)
	err := run(def, en)
	if !errors.Is(err, errFail) {
		t.Fatalf("got %v, want %v", err, errFail)
	}
	wantDef, _ := mustVariants(t, in)
	origs := map[string][]byte{defaultName: in, enabledName: oldEnabled}
	want := map[string][]byte{defaultName: wantDef, enabledName: oldEnabled}
	for name, b := range origs {
		m, _ := filepath.Glob(filepath.Join(dir, "."+name+".orig-*"))
		if len(m) != 1 || !strings.Contains(err.Error(), m[0]) {
			t.Fatalf("the error %q does not name the one original of %s: %q", err, name, m)
		}
		want[filepath.Base(m[0])] = b
	}
	wantFiles(t, dir, want)
}
