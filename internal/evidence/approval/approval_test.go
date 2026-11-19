package approval

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
)

func TestSignedVectors(t *testing.T) {
	const (
		tag     = "v0.5.0"
		digest  = "05498e56d1c1a9a3d11da45a7b7d56d1b4abdf407c824f7eb5169166a8a0b90b"
		subject = "sha256:16a0eeb0791b6c92451fd284dd9f599e0a7dbe7f6ebea6e2d2d06c7f74aec112"
	)
	raw, err := os.ReadFile("testdata/entelos-" + tag + "-signed-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != digest {
		t.Fatalf("Entelos %s vector digest = %s, want %s", tag, got, digest)
	}
	var vectors struct {
		KeySets map[string]json.RawMessage `json:"key_sets"`
		Cases   []struct {
			Name   string
			KeySet string `json:"key_set"`
			Signed json.RawMessage
			Expect struct {
				Valid      bool
				Code, Rule string
				RecordKind string `json:"record_kind"`
				RecordRef  string `json:"record_ref"`
				Signer     string
				KeyID      string `json:"key_id"`
				KeyStatus  string `json:"key_status"`
			}
		}
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal("invalid vectors")
	}
	for _, c := range vectors.Cases {
		t.Run(c.Name, func(t *testing.T) {
			keys, want := vectors.KeySets[c.KeySet], c.Expect
			record, err := verifySigned(c.Signed, keys)
			if !want.Valid {
				if err != (Refusal{want.Code, want.Rule}) {
					t.Fatalf("refusal type %T, want %s/%s", err, want.Code, want.Rule)
				}
				return
			}
			if err != nil || record.Kind != want.RecordKind || record.Ref != want.RecordRef ||
				record.Signer != want.Signer || record.KeyID != want.KeyID || record.KeyStatus != want.KeyStatus {
				t.Fatal("wrong verification result")
			}
			result, err := Verify(c.Signed, keys, subject)
			if want.RecordKind != "decision" {
				if err != (Refusal{"not_a_decision", ""}) {
					t.Fatal("expected a refusal for this record kind")
				}
				return
			}
			evidence := Record{"approval", "signature", subject, "pass", want.RecordRef, want.RecordRef}
			if err != nil || result != (Result{evidence, want.KeyStatus}) {
				t.Fatal("wrong evidence for accepted decision")
			}
			if _, err := Verify(c.Signed, keys, "sha256:"+strings.Repeat("0", 64)); err != (Refusal{"subject_mismatch", ""}) {
				t.Fatal("expected a refusal for the wrong subject")
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
	body := fmt.Appendf(nil, `{ "snapshot_ref":%q,"disposition":"reject","scope":"gate",
		"function":"pause","format":"entelos.record/1","reason":%q }`, subject, publicText)
	envelope := append([]byte("application/vnd.cel-lifecycle.decision+json\x00"), body...)
	ref := fmt.Sprintf("sha256:%x", sha256.Sum256(envelope))
	signature := ed25519.Sign(private, append([]byte(domain), envelope...))
	signatureText := base64.StdEncoding.EncodeToString(signature)
	signed := fmt.Appendf(nil, `{"format":"entelos.record/1","record_kind":"decision","scope":"gate","function":"pause",
		"record_ref":%q,"record":%q,"algorithm":"ed25519","key_id":%q,"signature":%q}`,
		ref, base64.StdEncoding.EncodeToString(body), id, signatureText)

	var logs bytes.Buffer
	writer := log.Writer()
	t.Cleanup(func() { log.SetOutput(writer) })
	log.SetOutput(&logs)
	result, err := Verify(signed, keys, subject)
	if err != nil || result != (Result{Record{"approval", "signature", subject, "fail", ref, ref}, "active"}) {
		t.Fatal("wrong evidence for rejected decision")
	}
	badSigned := bytes.Replace(signed, []byte(`"signature":`), []byte(fmt.Sprintf(`"%s":`, signatureText)), 1)
	badKeys := bytes.Replace(keys, []byte(`"public_key":`), []byte(fmt.Sprintf(`"%s":`, publicText)), 1)
	badRecord, signedErr := Verify(badSigned, keys, subject)
	badKeySet, keyErr := Verify(signed, badKeys, subject)
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
