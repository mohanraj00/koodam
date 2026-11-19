// Package approval verifies Entelos decisions offline for the evidence gate.
package approval

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Record is the evidence from a verified approval.
type Record struct {
	Source    string `json:"source"`
	Integrity string `json:"integrity"`
	Subject   string `json:"subject"`
	Verdict   string `json:"verdict"`
	ID        string `json:"id"`
	Digest    string `json:"digest"`
}

// Result gives the approval evidence and the status of its signing key.
type Result struct {
	Record    Record
	KeyStatus string
}

// Refusal gives the code and optional rule of a failed check.
type Refusal struct{ Code, Rule string }

func (r Refusal) Error() string {
	if r.Rule == "" {
		return r.Code
	}
	return r.Rule + ": " + r.Code
}

// Verify checks a signed decision against a trusted key set from the caller.
// The expected subject is the snapshot reference of the decision.
func Verify(signed, keySet []byte, expectedSubject string) (Result, error) {
	record, err := verifySigned(signed, keySet)
	if err != nil {
		return Result{}, err
	}
	if record.Kind != "decision" {
		return Result{}, Refusal{"not_a_decision", ""}
	}
	subject := record.Body.text("snapshot_ref")
	if !refPattern.MatchString(subject) || subject != expectedSubject {
		return Result{}, Refusal{"subject_mismatch", ""}
	}
	var verdict string
	switch record.Body.text("disposition") {
	case "accept":
		verdict = "pass"
	case "reject":
		verdict = "fail"
	default:
		return Result{}, Refusal{"invalid_disposition", ""}
	}
	return Result{
		Record: Record{Source: "approval", Integrity: "signature", Subject: subject,
			Verdict: verdict, ID: record.Ref, Digest: record.Ref},
		KeyStatus: record.KeyStatus,
	}, nil
}

const (
	algorithm      = "ed25519"
	maxRecordBytes = 512 << 10

	invalidKeySet       = "invalid_keyset"
	invalidSignedRecord = "invalid_signed_record"
	schemaRule          = "schema"
)

var (
	versionPattern   = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	algorithmPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	keyIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}:[0-9a-f]{64}$`)
	refPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func checkFormat(doc object, prefix, unsupported string, malformed Refusal) error {
	version, ok := strings.CutPrefix(doc.text("format"), prefix)
	if _, found := doc["format"]; found && (!ok || !versionPattern.MatchString(version)) {
		return malformed
	}
	if version != "1" {
		return Refusal{unsupported, ""}
	}
	return nil
}

type publicKey struct {
	bytes  ed25519.PublicKey
	status string
}

type trustedKeys struct {
	signer string
	keys   map[string]publicKey
}

func parseKeys(raw []byte) (trustedKeys, error) {
	doc := strictObject(raw)
	if doc == nil {
		return trustedKeys{}, Refusal{invalidKeySet, "document"}
	}
	if err := checkFormat(doc, "entelos.keyset/", "unsupported_keyset_format", Refusal{invalidKeySet, "format"}); err != nil {
		return trustedKeys{}, err
	}
	list, _ := doc["keys"].([]any)
	signer := doc.text("signer")
	if len(doc) != 3 || !boundedText(signer) || len(list) == 0 || len(list) > 64 {
		return trustedKeys{}, Refusal{invalidKeySet, schemaRule}
	}
	for _, value := range list {
		entry, _ := value.(object)
		if !validKeyEntry(entry) {
			return trustedKeys{}, Refusal{invalidKeySet, schemaRule}
		}
	}
	keys := make(map[string]publicKey, len(list))
	for _, value := range list {
		entry := value.(object)
		id := entry.text("key_id")
		if entry.text("algorithm") != algorithm {
			return trustedKeys{}, Refusal{"unsupported_algorithm", ""}
		}
		rawKey := decodeBase64(entry.text("public_key"))
		if len(rawKey) != ed25519.PublicKeySize {
			return trustedKeys{}, Refusal{invalidKeySet, "key_encoding"}
		}
		if id != fmt.Sprintf("ed25519:%x", sha256.Sum256(rawKey)) {
			return trustedKeys{}, Refusal{invalidKeySet, "key_id"}
		}
		if _, found := keys[id]; found {
			return trustedKeys{}, Refusal{invalidKeySet, "duplicate_key"}
		}
		keys[id] = publicKey{bytes: rawKey, status: entry.text("status")}
	}
	return trustedKeys{signer: signer, keys: keys}, nil
}

func validKeyEntry(entry object) bool {
	fields := []string{"key_id", "algorithm", "public_key", "status"}
	if _, found := entry["note"]; found {
		if !boundedText(entry.text("note")) {
			return false
		}
		fields = append(fields, "note")
	}
	status := entry.text("status")
	return entry.hasStrings(fields...) && keyIDPattern.MatchString(entry.text("key_id")) &&
		algorithmPattern.MatchString(entry.text("algorithm")) &&
		(status == "active" || status == "retired" || status == "revoked")
}

func boundedText(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= 256
}

type verifiedRecord struct {
	Kind, Ref, Scope, Function, Signer, KeyID, KeyStatus string
	Body                                                 object
}

func validSignedFields(doc object) bool {
	kind, encoded := doc.text("record_kind"), doc.text("record")
	return doc.hasStrings("format", "scope", "function", "record_kind", "record_ref", "record", "algorithm", "key_id", "signature") &&
		doc.text("scope") != "" && doc.text("function") != "" &&
		(kind == "decision" || kind == "event" || kind == "activation") &&
		refPattern.MatchString(doc.text("record_ref")) && keyIDPattern.MatchString(doc.text("key_id")) &&
		algorithmPattern.MatchString(doc.text("algorithm")) &&
		len(encoded) > 0 && len(encoded) <= base64.StdEncoding.EncodedLen(maxRecordBytes)
}

func verifySigned(raw, keySet []byte) (verifiedRecord, error) {
	keys, err := parseKeys(keySet)
	if err != nil {
		return verifiedRecord{}, err
	}
	doc := strictObject(raw)
	if doc == nil {
		return verifiedRecord{}, Refusal{invalidSignedRecord, "document"}
	}
	if err := checkFormat(doc, "entelos.record/", "unsupported_record_format", Refusal{invalidSignedRecord, "format"}); err != nil {
		return verifiedRecord{}, err
	}
	if !validSignedFields(doc) {
		return verifiedRecord{}, Refusal{invalidSignedRecord, schemaRule}
	}
	if doc.text("algorithm") != algorithm {
		return verifiedRecord{}, Refusal{"unsupported_algorithm", ""}
	}
	record := decodeBase64(doc.text("record"))
	if record == nil {
		return verifiedRecord{}, Refusal{invalidSignedRecord, "record_encoding"}
	}
	signature := decodeBase64(doc.text("signature"))
	if len(signature) != ed25519.SignatureSize {
		return verifiedRecord{}, Refusal{invalidSignedRecord, "signature_encoding"}
	}
	const domain = "entelos.signature/1\x00"
	kind, ref, id := doc.text("record_kind"), doc.text("record_ref"), doc.text("key_id")
	message := append([]byte(domain+"application/vnd.cel-lifecycle."+kind+"+json\x00"), record...)
	if ref != fmt.Sprintf("sha256:%x", sha256.Sum256(message[len(domain):])) {
		return verifiedRecord{}, Refusal{"record_mismatch", "ref"}
	}
	key, found := keys.keys[id]
	if !found {
		return verifiedRecord{}, Refusal{"unknown_key", ""}
	}
	if key.status == "revoked" {
		return verifiedRecord{}, Refusal{"revoked_key", ""}
	}
	if !ed25519.Verify(key.bytes, message, signature) {
		return verifiedRecord{}, Refusal{"signature_invalid", ""}
	}
	body := strictObject(record)
	if body == nil {
		return verifiedRecord{}, Refusal{invalidSignedRecord, "record_json"}
	}
	if err := checkFormat(body, "entelos.record/", "unsupported_record_format", Refusal{invalidSignedRecord, "record_format"}); err != nil {
		return verifiedRecord{}, err
	}
	scope, function := doc.text("scope"), doc.text("function")
	if body.text("scope") != scope || body.text("function") != function {
		return verifiedRecord{}, Refusal{"record_mismatch", "binding"}
	}
	return verifiedRecord{Kind: kind, Ref: ref, Scope: scope, Function: function,
		KeyID: id, KeyStatus: key.status, Signer: keys.signer, Body: body}, nil
}

func decodeBase64(text string) []byte {
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != text {
		return nil
	}
	return raw
}
