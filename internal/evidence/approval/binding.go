package approval

import (
	"crypto/sha256"
	"fmt"
)

const (
	// snapshotMediaType is the media type in the record reference of a stored snapshot record.
	snapshotMediaType = "application/vnd.cel-lifecycle.snapshot+json"
	// requestItemID is the fixed id of the one manifest item that holds the outside request.
	requestItemID = "request"
	// codeBinding is the code of a snapshot or a manifest that does not bind a request. The rule
	// names the step.
	codeBinding = "binding_mismatch"
)

func digest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

// Binding checks that the stored bytes of a snapshot record and the bytes of a content manifest
// bind the digest of an outside request. It gives the record reference of the snapshot, which is
// the snapshot_ref that a signed decision must name. Each compare is exact:
//
//   - The candidate of the snapshot has the kind content, and the snapshot has the scope.
//   - The candidate reference is sha256: and the plain SHA-256 of the manifest bytes. It is not
//     the manifest_ref of the snapshot, which has an envelope.
//   - The manifest has exactly one item, with the id "request" and the digest of the request.
//
// It checks no signature. A caller checks the binding first, and then gives the reference to
// Verify as the subject. Thus a signed decision of a different request never reaches the checks of
// the dates and of the disposition. A manifest with a different item set gives the code
// subject_mismatch. Each other failure gives the code binding_mismatch.
func Binding(snapshot, manifest []byte, scope, request string) (string, error) {
	record := strictObject(snapshot)
	if record == nil {
		return "", Refusal{codeBinding, "snapshot"}
	}
	if err := checkFormat(record, "entelos.record/", "unsupported_record_format", Refusal{codeBinding, "snapshot_format"}); err != nil {
		return "", err
	}
	candidate, _ := record["candidate"].(object)
	if candidate.text("kind") != "content" {
		return "", Refusal{codeBinding, "candidate_kind"}
	}
	if scope == "" || record.text("scope") != scope {
		return "", Refusal{codeBinding, "scope"}
	}
	if candidate.text("ref") != digest(manifest) {
		return "", Refusal{codeBinding, "manifest_ref"}
	}
	content := strictObject(manifest)
	if content == nil {
		return "", Refusal{codeBinding, "manifest"}
	}
	if err := checkFormat(content, "entelos.content/", "unsupported_content_format", Refusal{codeBinding, "manifest_format"}); err != nil {
		return "", err
	}
	items, _ := content["items"].([]any)
	if len(items) != 1 {
		return "", Refusal{codeSubjectMismatch, ""}
	}
	if item, _ := items[0].(object); item.text("item_id") != requestItemID || !refPattern.MatchString(request) ||
		item.text("digest") != request {
		return "", Refusal{codeSubjectMismatch, ""}
	}
	return digest(append([]byte(snapshotMediaType+"\x00"), snapshot...)), nil
}
