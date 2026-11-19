package approval

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

// The window of each decision of the tests, and a time in it.
const windowFrom, windowUntil = "2026-11-01", "2027-01-01"

var inWindow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// TestVerifyVectors checks the scope, the function, the subject and the window of each valid
// decision of the vectors.
func TestVerifyVectors(t *testing.T) {
	const subject = "sha256:16a0eeb0791b6c92451fd284dd9f599e0a7dbe7f6ebea6e2d2d06c7f74aec112"
	keySets, cases := loadVectors(t)
	for _, c := range cases {
		if !c.Expect.Valid {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			keys, want := keySets[c.KeySet], c.Expect
			var purpose struct{ Scope, Function string }
			if err := json.Unmarshal(c.Signed, &purpose); err != nil {
				t.Fatal("invalid signed record")
			}
			expected := Expected{Scope: purpose.Scope, Function: purpose.Function, Subject: subject}
			result, err := Verify(c.Signed, keys, expected, inWindow)
			if want.RecordKind != "decision" {
				if err != (Refusal{codeNotDecision, ""}) {
					t.Fatal("expected a refusal for this record kind")
				}
				return
			}
			evidence := Record{"approval", "signature", subject, "pass", want.RecordRef, want.RecordRef}
			if err != nil || result != (Result{evidence, want.KeyStatus}) {
				t.Fatal("wrong evidence for accepted decision")
			}
			for _, other := range []struct {
				expected Expected
				code     string
			}{
				{Expected{purpose.Scope, purpose.Function, "sha256:" + strings.Repeat("0", 64)}, codeSubjectMismatch},
				{Expected{"other", purpose.Function, subject}, codePurposeMismatch},
				{Expected{purpose.Scope, "other", subject}, codePurposeMismatch},
				{Expected{"", "", subject}, codePurposeMismatch},
			} {
				if _, err := Verify(c.Signed, keys, other.expected, inWindow); err != (Refusal{other.code, ""}) {
					t.Fatalf("got %v, want %s", err, other.code)
				}
			}
			// The window is half-open: from 2026-11-01 to 2027-01-01, in UTC dates.
			for _, w := range []struct {
				now  time.Time
				want error
			}{
				{time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC), Refusal{codeNotYetValid, ""}},
				{time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), nil},
				{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), nil},
				{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Refusal{codeExpired, ""}},
				// 2026-12-31 at 20:00 in UTC-5 is 2027-01-01 in UTC.
				{time.Date(2026, 12, 31, 20, 0, 0, 0, time.FixedZone("UTC-5", -5*3600)), Refusal{codeExpired, ""}},
			} {
				if _, err := Verify(c.Signed, keys, expected, w.now); err != w.want {
					t.Fatalf("at %s: got %v, want %v", w.now, err, w.want)
				}
			}
		})
	}
}

func TestVerifyRejectAndKeepBytesPrivate(t *testing.T) {
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	publicText := base64.StdEncoding.EncodeToString(public)
	id := fmt.Sprintf("ed25519:%x", sha256.Sum256(public))
	subject := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("subject")))
	keys := fmt.Appendf(nil, `{"format":"entelos.keyset/1","signer":%q,"keys":[{"algorithm":"ed25519",
		"key_id":%q,"public_key":%q,"status":"active"}]}`, publicText, id, publicText)
	const domain = "entelos.signature/1\x00"
	// sign gives a signed decision with the window from "from" to "until", and its reference.
	sign := func(from, until string) (signed []byte, ref string, signature []byte) {
		body := fmt.Appendf(nil, `{ "snapshot_ref":%q,"disposition":"reject","scope":"gate","function":"pause",
			"format":"entelos.record/1","from":%q,"until":%q,"reason":%q }`, subject, from, until, publicText)
		envelope := append([]byte("application/vnd.cel-lifecycle.decision+json\x00"), body...)
		ref = fmt.Sprintf("sha256:%x", sha256.Sum256(envelope))
		signature = ed25519.Sign(private, append([]byte(domain), envelope...))
		signed = fmt.Appendf(nil, `{"format":"entelos.record/1","record_kind":"decision","scope":"gate",
			"function":"pause","record_ref":%q,"record":%q,"algorithm":"ed25519","key_id":%q,"signature":%q}`,
			ref, base64.StdEncoding.EncodeToString(body), id, base64.StdEncoding.EncodeToString(signature))
		return signed, ref, signature
	}
	gate := Expected{Scope: "gate", Function: "pause", Subject: subject}
	signed, ref, signature := sign(windowFrom, windowUntil)
	signatureText := base64.StdEncoding.EncodeToString(signature)
	// A window with a date that is not real, with no date or with from not before until is refused.
	for _, w := range [][2]string{{"2026-02-30", windowUntil}, {"", windowUntil}, {windowFrom, windowFrom},
		{windowFrom, "2027-1-1"}} {
		bad, _, _ := sign(w[0], w[1])
		if _, err := Verify(bad, keys, gate, inWindow); err != (Refusal{codeInvalidWindow, ""}) {
			t.Fatalf("window %v: got %v", w, err)
		}
	}

	var logs bytes.Buffer
	writer := log.Writer()
	t.Cleanup(func() { log.SetOutput(writer) })
	log.SetOutput(&logs)
	result, err := Verify(signed, keys, gate, inWindow)
	if err != nil || result != (Result{Record{"approval", "signature", subject, "fail", ref, ref}, "active"}) {
		t.Fatal("wrong evidence for rejected decision")
	}
	badSigned := bytes.Replace(signed, []byte(`"signature":`), []byte(fmt.Sprintf(`"%s":`, signatureText)), 1)
	badKeys := bytes.Replace(keys, []byte(`"public_key":`), []byte(fmt.Sprintf(`"%s":`, publicText)), 1)
	badRecord, signedErr := Verify(badSigned, keys, gate, inWindow)
	badKeySet, keyErr := Verify(signed, badKeys, gate, inWindow)
	if signedErr != (Refusal{"invalid_signed_record", "schema"}) || keyErr != (Refusal{"invalid_keyset", "schema"}) ||
		badRecord != (Result{}) || badKeySet != (Result{}) {
		t.Fatal("expected a refusal for each unknown field")
	}
	output := fmt.Appendf(logs.Bytes(), "%+v %v %v", result, signedErr, keyErr)
	for _, secret := range [][]byte{public, signature, []byte(publicText), []byte(signatureText)} {
		if bytes.Contains(output, secret) {
			t.Fatal("verification output holds key or signature bytes")
		}
	}
}
