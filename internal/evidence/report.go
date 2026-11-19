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
// Package evidence has the report parser and the check of the evidence gate
// (docs/evidence-gate.md). It reads the short report that the verify step of the gate Job writes
// to the termination message. The report comes from a pod, so the parser accepts only the known
// keys and values of a fixed form. An error of this package never holds text of the report.
package evidence

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/types"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// ReportVersion is the version of the report of the verify step.
	ReportVersion = 1
	// ResultFormat is the version of result.json of Nookku that this parser knows (SPEC.md
	// section 7.15 of Nookku).
	ResultFormat = 1
	// maxReport is the size limit of a termination message.
	maxReport = 4096
	// maxSubject is the subject limit of Nookku.
	maxSubject = 512
	// subjectPrefix names the subject form of an AppVersion.
	subjectPrefix = "koodam.dev/appversion/v1:"
)

// The errors of ParseReport. Each one gives the reason NotVerified.
var (
	ErrNotJSON   = errors.New("the report is not one JSON object of the permitted size")
	ErrKeys      = errors.New("the report does not have exactly the known keys, each one time")
	ErrVersion   = errors.New("the report has a version that the parser does not know")
	ErrExitCode  = errors.New("the report has an exit code that is not 0, 1 or 2")
	ErrFormat    = errors.New("result.json has a format version that the parser does not know")
	ErrFieldForm = errors.New("a field of the report does not have the permitted form")
)

var (
	reGateID      = regexp.MustCompile(`^gate-[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$`)
	reSHA256      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reVersion     = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	reImageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reportKeys    = []string{"v", "exit", "gate", "subject", "verdict", "sha256", "format", "nookku"}
)

// Report is the report of the verify step. Exit is the exit code of nookku gate verify. The other
// fields come from the result that Nookku verified. They are empty if Exit is 2.
type Report struct {
	V       int    `json:"v"`
	Exit    int    `json:"exit"`
	Gate    string `json:"gate"`
	Subject string `json:"subject"`
	Verdict string `json:"verdict"`
	SHA256  string `json:"sha256"`
	Format  int    `json:"format"`
	Nookku  string `json:"nookku"`
}

// ParseReport reads the termination message of the verify container. It refuses a message that
// is not one JSON object, an unknown key, a key that occurs 2 times, a missing key and a value
// that does not have the fixed form. With the exit code 2, Nookku gave no result that can be
// used, so the parser examines only the exit code.
func ParseReport(msg string) (Report, error) {
	var r Report
	if len(msg) == 0 || len(msg) > maxReport || !json.Valid([]byte(msg)) ||
		!strings.HasPrefix(strings.TrimSpace(msg), "{") {
		return r, ErrNotJSON
	}
	if err := exactKeys(msg); err != nil {
		return r, err
	}
	dec := json.NewDecoder(strings.NewReader(msg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Report{}, ErrFieldForm
	}
	switch {
	case r.V != ReportVersion:
		return Report{}, ErrVersion
	case r.Exit == 2:
		return Report{V: r.V, Exit: r.Exit}, nil
	case r.Exit != 0 && r.Exit != 1:
		return Report{}, ErrExitCode
	case r.Format != ResultFormat:
		return Report{}, ErrFormat
	case !reGateID.MatchString(r.Gate) || !validSubject(r.Subject) || !reSHA256.MatchString(r.SHA256) ||
		!reVersion.MatchString(r.Nookku) ||
		(r.Verdict != string(corev1alpha1.EvidencePass) && r.Verdict != string(corev1alpha1.EvidenceFail)):
		return Report{}, ErrFieldForm
	}
	return r, nil
}

// exactKeys gives ErrKeys if the JSON object msg does not have each known key one time, or has a
// different key. It gives ErrFieldForm for a null value: the decoder keeps the zero value of a
// field for a null, and the zero value of the exit code is the exit code of a pass.
func exactKeys(msg string) error {
	dec := json.NewDecoder(strings.NewReader(msg))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ErrKeys
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		key, ok := t.(string)
		var value json.RawMessage
		if err != nil || !ok || seen[key] || dec.Decode(&value) != nil {
			return ErrKeys
		}
		if string(value) == "null" {
			return ErrFieldForm
		}
		seen[key] = true
	}
	for _, k := range reportKeys {
		if !seen[k] {
			return ErrKeys
		}
	}
	if len(seen) != len(reportKeys) {
		return ErrKeys
	}
	return nil
}

func validSubject(s string) bool {
	return len(s) > 0 && len(s) <= maxSubject && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Check gives the reason of the condition Evidence for a report that parsed, and the verdict of
// an accepted record. want is the subject that the controller computes again at the time of the
// check. The verdict is empty if the controller does not accept the record.
func (r Report) Check(want string) (reason string, verdict corev1alpha1.EvidenceVerdict) {
	switch {
	case r.Exit != 0 && r.Exit != 1:
		return corev1alpha1.EvidenceNotVerified, ""
	case r.Subject != want:
		return corev1alpha1.EvidenceSubjectMismatch, ""
	case r.Exit == 0 && r.Verdict == string(corev1alpha1.EvidencePass):
		return corev1alpha1.EvidencePassed, corev1alpha1.EvidencePass
	case r.Exit == 1 && r.Verdict == string(corev1alpha1.EvidenceFail):
		return corev1alpha1.EvidenceFailed, corev1alpha1.EvidenceFail
	}
	return corev1alpha1.EvidenceNotVerified, ""
}

// ImageDigest gives back the sha256 digest of an image reference, and false if the reference
// does not name the image by digest.
func ImageDigest(image string) (string, bool) {
	_, digest, found := strings.Cut(image, "@")
	return digest, found && reImageDigest.MatchString(digest)
}

// AppVersionSubject is the subject digest of an AppVersion. The UID binds the record to one
// object, and the digest binds it to the image of the app container.
func AppVersionSubject(namespace, name string, uid types.UID, digest string) string {
	return subjectPrefix + namespace + "/" + name + ":" + string(uid) + ":" + digest
}
