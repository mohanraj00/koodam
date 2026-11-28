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

package evidence

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// The seam test proves that the fixed verify program and the report parser read the gate result
// of the pinned Nookku release (docs/evidence-gate.md, "Seam test"). The seam job of CI installs
// the pinned wheel and gives two paths: KOODAM_SEAM_PYTHON is the Python with that wheel, and
// KOODAM_SEAM_CASES is conformance/gate/ of the pinned commit. With KOODAM_SEAM=1 the test fails
// if a path is missing. With no variable, the test skips.

//go:embed nookku.pin
var nookkuPin string

// pinValue gives back one value of the pin file.
func pinValue(t *testing.T, key, form string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + key + `=(` + form + `)$`).FindStringSubmatch(nookkuPin)
	if m == nil {
		t.Fatalf("nookku.pin has no valid %s", key)
	}
	return m[1]
}

func TestNookkuPin(t *testing.T) {
	pinValue(t, "NOOKKU_VERSION", `[0-9]+\.[0-9]+\.[0-9]+`)
	pinValue(t, "NOOKKU_WHEEL_SHA256", `[0-9a-f]{64}`)
	pinValue(t, "NOOKKU_COMMIT", `[0-9a-f]{40}`)
}

func TestSeam(t *testing.T) {
	python, cases := os.Getenv("KOODAM_SEAM_PYTHON"), os.Getenv("KOODAM_SEAM_CASES")
	if python == "" || cases == "" {
		if os.Getenv("KOODAM_SEAM") != "" {
			t.Fatal("KOODAM_SEAM is set, but KOODAM_SEAM_PYTHON or KOODAM_SEAM_CASES is not")
		}
		t.Skip("the seam job of CI sets KOODAM_SEAM_PYTHON and KOODAM_SEAM_CASES")
	}
	version := pinValue(t, "NOOKKU_VERSION", `[0-9]+\.[0-9]+\.[0-9]+`)
	// passCase is the folder of the case with 2 tests that pass.
	const passCase = "pass"
	other := AppVersionSubject("shop", "v2", "0b9f6c1e-1d2c-4c58-9a51-3c1f7a2b4d10", testDigest)
	for _, c := range []struct {
		name, folder string
		tamper       bool
		// want is the subject that the check expects. If it is empty, it is the subject of the case.
		want, reason string
	}{
		{name: "a pass", folder: passCase, reason: corev1beta1.EvidencePassed},
		{name: "a fail", folder: "fail", reason: corev1beta1.EvidenceFailed},
		{name: "a missing test", folder: "missing_test", reason: corev1beta1.EvidenceFailed},
		// Nookku 0.6.0 adds the cases of a persona set and of a second message. The result of a persona
		// set has the key persona in each test, and the verify program does not read the tests.
		{name: "a persona set with one persona that fails", folder: "persona_set", reason: corev1beta1.EvidenceFailed},
		{name: "a second message that passes", folder: "second_message_pass", reason: corev1beta1.EvidencePassed},
		{name: "a second message that is late", folder: "second_message_late", reason: corev1beta1.EvidenceFailed},
		{name: "a result.json that changed after the seal", folder: passCase, tamper: true,
			reason: corev1beta1.EvidenceNotVerified},
		{name: "a subject that does not match", folder: passCase, want: other,
			reason: corev1beta1.EvidenceSubjectMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			root, out := filepath.Join(work, "project"), filepath.Join(work, "report.json")
			verify := filepath.Join(work, "verify.py")
			if err := os.WriteFile(verify, []byte(VerifyScript), 0o600); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) {
				cmd := exec.Command(python, append([]string{"-I"}, args...)...)
				cmd.Env = append(os.Environ(), "NOOKKU_HOME="+filepath.Join(work, "home"))
				if text, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s", filepath.Base(args[0]), err, text)
				}
			}
			argv := []string{filepath.Join("testdata", "seam_case.py"), filepath.Join(cases, c.folder), root, testGate}
			if c.tamper {
				argv = append(argv, "--tamper")
			}
			run(argv...)
			run(verify, root, out)
			msg, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			report, err := ParseReport(string(msg))
			switch {
			case errors.Is(err, ErrFormat):
				t.Fatalf("Nookku %s writes result.json with a format version that the parser does not know. "+
					"The parser knows version %d. Change the parser and the design before the pin.", version, ResultFormat)
			case err != nil:
				t.Fatalf("the report of Nookku %s does not parse: %v", version, err)
			}
			if !c.tamper && report.Nookku != version {
				t.Fatalf("the Python of the seam test has Nookku %s, and the pin is %s", report.Nookku, version)
			}
			want := c.want
			if want == "" {
				var given struct{ Subject string }
				data, err := os.ReadFile(filepath.Join(cases, c.folder, "case.json"))
				if err != nil || json.Unmarshal(data, &given) != nil || given.Subject == "" {
					t.Fatalf("case.json of %s has no subject: %v", c.folder, err)
				}
				want = given.Subject
			}
			if reason, _ := report.Check(want); reason != c.reason {
				t.Fatalf("the reason is %s, want %s (exit code %d, verdict %q)", reason, c.reason, report.Exit,
					strings.ToValidUTF8(report.Verdict, "?"))
			}
		})
	}
}
