package approval

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// pinValue reads one value of entelos.pin and checks its form.
func pinValue(t *testing.T, name, pattern string) string {
	t.Helper()
	raw, err := os.ReadFile("entelos.pin")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^` + name + `=(` + pattern + `)$`).FindSubmatch(raw)
	if match == nil {
		t.Fatalf("entelos.pin has no valid %s", name)
	}
	return string(match[1])
}

// TestPin checks the form of each value of the pin, and that the README of testdata names the
// same tag, commit and SHA-256.
func TestPin(t *testing.T) {
	readme, err := os.ReadFile("testdata/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, pattern := range map[string]string{
		"ENTELOS_TAG": `v[0-9]+\.[0-9]+\.[0-9]+`, "ENTELOS_COMMIT": `[0-9a-f]{40}`, "ENTELOS_VECTORS_SHA256": `[0-9a-f]{64}`,
	} {
		if value := pinValue(t, name, pattern); !strings.Contains(string(readme), "`"+value+"`") {
			t.Errorf("testdata/README.md does not name the %s of the pin", name)
		}
	}
}

// TestSeam compares the copy of the signed record vectors with the source file of the pinned
// tag. The seam job of CI checks out the tag, checks its commit and sets KOODAM_APPROVAL_VECTORS.
// The source file must have the pinned SHA-256, and the copy must be the source with no
// top-level keys object.
func TestSeam(t *testing.T) {
	path := os.Getenv("KOODAM_APPROVAL_VECTORS")
	if path == "" {
		if os.Getenv("KOODAM_SEAM") != "" {
			t.Fatal("KOODAM_SEAM is set, but KOODAM_APPROVAL_VECTORS is not")
		}
		t.Skip("the seam job of CI sets KOODAM_APPROVAL_VECTORS")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(source)), pinValue(t, "ENTELOS_VECTORS_SHA256", `[0-9a-f]{64}`); got != want {
		t.Fatalf("the vectors of the tag have the SHA-256 %s, and the pin has %s", got, want)
	}
	copied, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if json.Unmarshal(source, &want) != nil || json.Unmarshal(copied, &got) != nil {
		t.Fatal("invalid vectors")
	}
	delete(want, "keys")
	if !reflect.DeepEqual(got, want) {
		t.Fatal("the copy of the vectors is not the source file with no keys object")
	}
}
