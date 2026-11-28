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
	"errors"
	"os/exec"
	"strings"
	"testing"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

const (
	testDigest  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testSubject = "koodam.dev/appversion/v1:shop/v2:0b9f6c1e-1d2c-4c58-9a51-3c1f7a2b4d10:" + testDigest
	testSHA     = "2222222222222222222222222222222222222222222222222222222222222222"
	testGate    = "gate-20261119-030000-0a1b2c3d"
)

// report makes the JSON text of a report. A change replaces one key and its value.
func report(exit string, verdict string, changes ...string) string {
	fields := map[string]string{"v": "1", "exit": exit, "gate": `"` + testGate + `"`,
		"subject": `"` + testSubject + `"`, "verdict": `"` + verdict + `"`, "sha256": `"` + testSHA + `"`,
		"format": "1", "nookku": `"0.6.0"`}
	for i := 0; i+1 < len(changes); i += 2 {
		fields[changes[i]] = changes[i+1]
	}
	parts := make([]string, 0, len(reportKeys))
	for _, k := range reportKeys {
		parts = append(parts, `"`+k+`":`+fields[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestParseReportAndCheck(t *testing.T) {
	pass, fail := report("0", "pass"), report("1", "fail")
	cases := []struct {
		name, msg, reason string
		err               error
		verdict           corev1beta1.EvidenceVerdict
	}{
		{name: "a pass", msg: pass, reason: corev1beta1.EvidencePassed, verdict: corev1beta1.EvidencePass},
		{name: "a fail", msg: fail, reason: corev1beta1.EvidenceFailed, verdict: corev1beta1.EvidenceFail},
		{name: "exit code 2", msg: report("2", "", "gate", `""`, "subject", `""`, "sha256", `""`, "format", "0"),
			reason: corev1beta1.EvidenceNotVerified},
		{name: "exit code 2 with a pass", msg: report("2", "pass"), reason: corev1beta1.EvidenceNotVerified},
		{name: "a pass with exit code 1", msg: report("1", "pass"), reason: corev1beta1.EvidenceNotVerified},
		{name: "a fail with exit code 0", msg: report("0", "fail"), reason: corev1beta1.EvidenceNotVerified},
		{name: "a wrong subject", msg: report("0", "pass", "subject", `"`+testSubject+`0"`),
			reason: corev1beta1.EvidenceSubjectMismatch},
		{name: "a wrong subject with a verdict that does not agree", msg: report("0", "fail", "subject", `"other"`),
			reason: corev1beta1.EvidenceSubjectMismatch},
		{name: "not JSON", msg: "Traceback (most recent call last):", err: ErrNotJSON},
		{name: "an empty message", msg: "", err: ErrNotJSON},
		{name: "a JSON list", msg: "[" + pass + "]", err: ErrNotJSON},
		{name: "2 objects", msg: pass + pass, err: ErrNotJSON},
		{name: "a message that is too long", msg: report("0", "pass", "nookku", `"`+strings.Repeat("9", 4096)+`"`),
			err: ErrNotJSON},
		{name: "an unknown key", msg: strings.Replace(pass, "{", `{"note":"x",`, 1), err: ErrKeys},
		{name: "a key 2 times", msg: strings.Replace(pass, "{", `{"exit":1,`, 1), err: ErrKeys},
		{name: "a missing key", msg: strings.Replace(pass, `"format":1,`, "", 1), err: ErrKeys},
		{name: "a report version that is not known", msg: report("0", "pass", "v", "2"), err: ErrVersion},
		{name: "exit code 3", msg: report("3", "pass"), err: ErrExitCode},
		{name: "a result format that is not known", msg: report("0", "pass", "format", "2"), err: ErrFormat},
		{name: "an exit code that is text", msg: report(`"0"`, "pass"), err: ErrFieldForm},
		{name: "an exit code that is null", msg: report("null", "pass"), err: ErrFieldForm},
		{name: "a null value with exit code 2", msg: report("2", "", "gate", "null"), err: ErrFieldForm},
		{name: "a gate id of a different form", msg: report("0", "pass", "gate", `"gate-case"`), err: ErrFieldForm},
		{name: "a subject with a control character", msg: report("0", "pass", "subject", `"a\nb"`), err: ErrFieldForm},
		{name: "a subject that is too long", msg: report("0", "pass", "subject", `"`+strings.Repeat("s", 513)+`"`),
			err: ErrFieldForm},
		{name: "a verdict that is not known", msg: report("0", "maybe"), err: ErrFieldForm},
		{name: "a SHA-256 of a different form", msg: report("0", "pass", "sha256", `"abc"`), err: ErrFieldForm},
		{name: "no Nookku version", msg: report("0", "pass", "nookku", `""`), err: ErrFieldForm},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := ParseReport(c.msg)
			if !errors.Is(err, c.err) {
				t.Fatalf("ParseReport error = %v, want %v", err, c.err)
			}
			if err != nil {
				if r != (Report{}) {
					t.Fatalf("ParseReport gave back a report with an error: %+v", r)
				}
				return
			}
			if reason, verdict := r.Check(testSubject); reason != c.reason || verdict != c.verdict {
				t.Fatalf("Check = %q, %q, want %q, %q", reason, verdict, c.reason, c.verdict)
			}
		})
	}
}

func TestSubject(t *testing.T) {
	for image, want := range map[string]string{
		"registry.example.com/app@" + testDigest:     testDigest,
		"registry.example.com/app:v1@" + testDigest:  testDigest,
		"registry.example.com/app:v1":                "",
		"registry.example.com/app@sha256:abc":        "",
		"registry.example.com/app@sha512:" + testSHA: "",
		"": "",
	} {
		if got, ok := ImageDigest(image); ok != (want != "") || (ok && got != want) {
			t.Errorf("ImageDigest(%q) = %q, %v, want %q", image, got, ok, want)
		}
	}
	got := AppVersionSubject("shop", "v2", "0b9f6c1e-1d2c-4c58-9a51-3c1f7a2b4d10", testDigest)
	if got != testSubject {
		t.Errorf("AppVersionSubject = %q, want %q", got, testSubject)
	}
}

// TestScriptsCompile checks that each fixed program of the gate Job is valid Python. The seam
// test runs the programs with the pinned Nookku release.
func TestScriptsCompile(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	for name, src := range map[string]string{"run": RunScript, "verify": VerifyScript} {
		cmd := exec.Command(python, "-c", "import sys; compile(sys.stdin.read(), sys.argv[1], 'exec')", name)
		cmd.Stdin = strings.NewReader(src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("the %s program does not compile: %v\n%s", name, err, out)
		}
	}
}
