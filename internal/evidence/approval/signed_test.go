package approval

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// vectorFile is the Entelos v0.5.0 vector file with the top-level keys object removed.
// testdata/README.md has the SHA-256 of the source file and the strip step.
const (
	vectorFile   = "testdata/entelos-v0.5.0-signed-cases.json"
	vectorDigest = "f968adc4a59c69e9af17a20284c168b6980b5d4b4c02d53fbedb2eccf968754a"
)

type vectorCase struct {
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

// loadVectors reads the vector file, checks its SHA-256 and gives the key sets and the cases.
func loadVectors(t *testing.T) (map[string]json.RawMessage, []vectorCase) {
	t.Helper()
	raw, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != vectorDigest {
		t.Fatalf("vector digest = %s, want %s", got, vectorDigest)
	}
	var vectors struct {
		KeySets map[string]json.RawMessage `json:"key_sets"`
		Cases   []vectorCase
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal("invalid vectors")
	}
	return vectors.KeySets, vectors.Cases
}

// TestSignedVectors runs each Entelos case, for example a bad signature or an unknown key, on
// the check of the key set and the signed record.
func TestSignedVectors(t *testing.T) {
	keySets, cases := loadVectors(t)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			want := c.Expect
			record, err := verifySigned(c.Signed, keySets[c.KeySet])
			if !want.Valid {
				if err != (Refusal{want.Code, want.Rule}) {
					t.Fatalf("got %v, want %s/%s", err, want.Code, want.Rule)
				}
				return
			}
			if err != nil || record.Kind != want.RecordKind || record.Ref != want.RecordRef ||
				record.Signer != want.Signer || record.KeyID != want.KeyID || record.KeyStatus != want.KeyStatus {
				t.Fatal("wrong verification result")
			}
		})
	}
}

// TestVectorsHaveNoSeeds fails if the vector file has the top-level keys object, or a seed field
// at any depth. The source file has the private key seeds of its test keys there.
func TestVectorsHaveNoSeeds(t *testing.T) {
	raw, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal("invalid vectors")
	}
	if _, found := doc["keys"]; found {
		t.Fatal("the vector file has the top-level keys object")
	}
	var walk func(value any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for name, item := range v {
				if name == "seed" {
					t.Fatal("the vector file has a seed field")
				}
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(doc)
}
