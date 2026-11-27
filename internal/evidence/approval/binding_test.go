package approval

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The fixture set of the pinned release: one accept chain and one reject chain for the same
// request. testdata/README.md has the source and the SHA-256 of each file.
const (
	fixtureDir      = "testdata/v0.7.0"
	fixtureScope    = "fixture"
	fixtureFunction = "content:agent-approvals/egress-grant"
	fixtureRequest  = "sha256:9d56cb14b63cd272a4cac6c5962da33d289d660289a26ba31d21804a109628d4"
)

// The files of the fixture set.
const (
	fileRequest        = "request.json"
	fileManifestAccept = "manifest-accept.json"
	fileManifestReject = "manifest-reject.json"
	fileSnapshotAccept = "snapshot-accept.record"
	fileSnapshotReject = "snapshot-reject.record"
	fileDecisionAccept = "decision-accept.signed.json"
	fileDecisionReject = "decision-reject.signed.json"
	fileKeySet         = "keyset.json"
)

var fixtureDigests = map[string]string{
	fileRequest:        "9d56cb14b63cd272a4cac6c5962da33d289d660289a26ba31d21804a109628d4",
	fileManifestAccept: "81d3ef45a68446079527825ae4511534b125ee3b0c8fe334184218661393198c",
	fileManifestReject: "0f41eb56ada3388dbcb53c32e81aa063c3eace36d13afeae1d2410d20590a57a",
	fileSnapshotAccept: "831e24da5a0bead452c8ae9484d378b2856a133f1731e1e19f0718d87750ece1",
	fileSnapshotReject: "2504f5c90123556cb0cd3b9693732bb5c106be57df737b167e730e31fbc8bbf8",
	fileDecisionAccept: "66d9ec4a9db994ca745370a6a05185d0f67c1b07d48f244720d243616d1cab79",
	fileDecisionReject: "44fe1c7b124f8521d95d6df9b7f05a9f40143c9de9e75ce2e72220351c2bf49a",
	fileKeySet:         "b91f9c554afdd2820e923e87968416222300ff28769792051cfe698a2621a61b",
}

// fixtureTime is 1 hour after the decisions of the fixture set.
var fixtureTime = time.Date(2026, 11, 27, 13, 0, 0, 0, time.UTC)

// fixture reads one file of the fixture set and checks its pinned SHA-256.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != fixtureDigests[name] {
		t.Fatalf("%s: digest = %s, want %s", name, got, fixtureDigests[name])
	}
	return raw
}

// TestFixtureSet fails if the fixture folder has a file with no pin, or a file with a key seed.
func TestFixtureSet(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil || len(entries) != len(fixtureDigests) {
		t.Fatalf("got %d files (%v), want %d", len(entries), err, len(fixtureDigests))
	}
	for _, entry := range entries {
		if bytes.Contains(bytes.ToLower(fixture(t, entry.Name())), []byte("seed")) {
			t.Fatalf("%s has a seed", entry.Name())
		}
	}
}

// TestFixtureChains checks the accept chain and the reject chain of the pinned release: the
// binding gives the snapshot reference, and Verify accepts the signed decision for it.
func TestFixtureChains(t *testing.T) {
	keys := fixture(t, fileKeySet)
	chains := []struct {
		decision, snapshot, manifest string
		// snapshotRef, verdict and recordRef are the expected result.
		snapshotRef, verdict, recordRef string
	}{
		{fileDecisionAccept, fileSnapshotAccept, fileManifestAccept,
			"sha256:9a57caf3d4e8e65bc941dc1d7008f799a56e6bdd5f5f0e42c09df65aaf15549f", "pass",
			"sha256:4281200930d0ab28cad1f1107eaf294408d25c63f2f0bc544c8dab09767900f0"},
		{fileDecisionReject, fileSnapshotReject, fileManifestReject,
			"sha256:b987470e60a2bf5a33774af96624d699bea9961c01e2f55ddb2b0a88b0fa09c5", "fail",
			"sha256:0c115aadd3b5cbc26f0e8420bfda03ec60da4045d22d3756c74ca5880e65a4ff"},
	}
	for i, c := range chains {
		signed := fixture(t, c.decision)
		ref, err := Binding(fixture(t, c.snapshot), fixture(t, c.manifest), fixtureScope, fixtureRequest)
		if err != nil || ref != c.snapshotRef {
			t.Fatalf("%s: binding = %q, %v", c.decision, ref, err)
		}
		result, err := Verify(signed, keys, Expected{fixtureScope, fixtureFunction, ref}, fixtureTime)
		got := result.Record
		if err != nil || got.Subject != ref || got.Verdict != c.verdict || got.ID != c.recordRef || got.Digest != c.recordRef {
			t.Fatalf("%s: got %+v, %v", c.decision, result, err)
		}
		if at, ok := DecidedAt(signed); !ok || !at.Equal(fixtureTime.Add(-time.Hour)) {
			t.Fatalf("%s: decided at %v, %v", c.decision, at, ok)
		}
		// The decision of one chain does not name the snapshot of the other chain.
		other := chains[1-i]
		ref, err = Binding(fixture(t, other.snapshot), fixture(t, other.manifest), fixtureScope, fixtureRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(signed, keys, Expected{fixtureScope, fixtureFunction, ref}, fixtureTime); err != (Refusal{codeSubjectMismatch, ""}) {
			t.Fatalf("%s with %s: got %v", c.decision, other.snapshot, err)
		}
	}
}

// TestBindingRefusals changes one part of the accept chain at a time.
func TestBindingRefusals(t *testing.T) {
	snapshot, manifest := fixture(t, fileSnapshotAccept), fixture(t, fileManifestAccept)
	// bound gives a snapshot record with the candidate, for a manifest that the test makes.
	bound := func(kind string, manifest []byte) []byte {
		return fmt.Appendf(nil, `{"format":"entelos.record/1","scope":"fixture","candidate":{"kind":%q,"ref":%q}}`,
			kind, digest(manifest))
	}
	item := func(id, ref string) string { return fmt.Sprintf(`{"item_id":%q,"digest":%q}`, id, ref) }
	items := func(list string) []byte { return []byte(`{"format":"entelos.content/1","items":[` + list + `]}`) }
	request := item(requestItemID, fixtureRequest)
	for name, c := range map[string]struct {
		snapshot, manifest []byte
		scope, request     string
		want               error
	}{
		"a manifest with one more byte":      {snapshot, append(bytes.Clone(manifest), '\n'), fixtureScope, fixtureRequest, Refusal{codeBinding, "manifest_ref"}},
		"the manifest of the other chain":    {snapshot, fixture(t, fileManifestReject), fixtureScope, fixtureRequest, Refusal{codeBinding, "manifest_ref"}},
		"a different scope":                  {snapshot, manifest, "not-fixture", fixtureRequest, Refusal{codeBinding, "scope"}},
		"an empty scope":                     {snapshot, manifest, "", fixtureRequest, Refusal{codeBinding, "scope"}},
		"a different request":                {snapshot, manifest, fixtureScope, digest([]byte("x")), Refusal{codeSubjectMismatch, ""}},
		"a request that is not a digest":     {bound("content", items(item(requestItemID, "r"))), items(item(requestItemID, "r")), fixtureScope, "r", Refusal{codeSubjectMismatch, ""}},
		"a snapshot that is not JSON":        {[]byte("not a record"), manifest, fixtureScope, fixtureRequest, Refusal{codeBinding, "snapshot"}},
		"a snapshot of a later format":       {[]byte(`{"format":"entelos.record/2"}`), manifest, fixtureScope, fixtureRequest, Refusal{"unsupported_record_format", ""}},
		"a candidate that is not content":    {bound("bundle", manifest), manifest, fixtureScope, fixtureRequest, Refusal{codeBinding, "candidate_kind"}},
		"a manifest that is not JSON":        {bound("content", []byte("x")), []byte("x"), fixtureScope, fixtureRequest, Refusal{codeBinding, "manifest"}},
		"a manifest of a later format":       {bound("content", []byte(`{"format":"entelos.content/2"}`)), []byte(`{"format":"entelos.content/2"}`), fixtureScope, fixtureRequest, Refusal{"unsupported_content_format", ""}},
		"a manifest with no item":            {bound("content", items("")), items(""), fixtureScope, fixtureRequest, Refusal{codeSubjectMismatch, ""}},
		"a manifest with an extra item":      {bound("content", items(request+","+item("extra", fixtureRequest))), items(request + "," + item("extra", fixtureRequest)), fixtureScope, fixtureRequest, Refusal{codeSubjectMismatch, ""}},
		"an item with a different id":        {bound("content", items(item("not-request", fixtureRequest))), items(item("not-request", fixtureRequest)), fixtureScope, fixtureRequest, Refusal{codeSubjectMismatch, ""}},
		"an item with a different digest":    {bound("content", items(item(requestItemID, digest(nil)))), items(item(requestItemID, digest(nil))), fixtureScope, fixtureRequest, Refusal{codeSubjectMismatch, ""}},
		"the made snapshot with the request": {bound("content", items(request)), items(request), fixtureScope, fixtureRequest, nil},
	} {
		if _, err := Binding(c.snapshot, c.manifest, c.scope, c.request); err != c.want {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
	// One more byte in the snapshot gives a different reference, so the decision does not name it.
	ref, err := Binding(append(bytes.Clone(snapshot), ' '), manifest, fixtureScope, fixtureRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(fixture(t, fileDecisionAccept), fixture(t, fileKeySet),
		Expected{fixtureScope, fixtureFunction, ref}, fixtureTime); err != (Refusal{codeSubjectMismatch, ""}) {
		t.Fatalf("a snapshot with one more byte: got %v", err)
	}
}
