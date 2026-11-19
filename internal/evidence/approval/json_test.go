package approval

import (
	"strings"
	"testing"
)

func TestStrictObjectRefuses(t *testing.T) {
	for _, c := range []struct{ name, raw string }{
		{"empty", ``},
		{"not JSON", `{"a":`},
		{"a list", `[1]`},
		{"a string", `"a"`},
		{"two values", `{"a":1} {"b":2}`},
		{"duplicate name", `{"a":1,"a":2}`},
		{"duplicate name inside", `{"a":{"b":1,"b":1}}`},
		{"invalid UTF-8", "{\"a\":\"\xff\"}"},
		{"lone high surrogate", `{"a":"\ud800"}`},
		{"high surrogate with no low", `{"a":"\ud800A"}`},
		{"lone low surrogate", `{"a":"\udc00"}`},
		{"float out of range", `{"a":1e400}`},
		{"integer too long", `{"a":` + strings.Repeat("1", 4301) + `}`},
		{"too deep", strings.Repeat(`{"a":`, 1000) + `1` + strings.Repeat(`}`, 1000)},
	} {
		if strictObject([]byte(c.raw)) != nil {
			t.Errorf("%s: got an object, want nil", c.name)
		}
	}
}

func TestStrictObjectAccepts(t *testing.T) {
	doc := strictObject([]byte(`{"a":"x","b":[1,-2.5,true,null,{"c":"😀"}],` +
		`"d":` + strings.Repeat("9", 4300) + `,"e":"é\n"}`))
	if doc == nil {
		t.Fatal("a valid object is refused")
	}
	list, _ := doc["b"].([]any)
	inner, _ := list[4].(object)
	if doc.text("a") != "x" || inner.text("c") != "\U0001F600" || doc.text("e") != "é\n" {
		t.Fatalf("wrong values: %v", doc)
	}
	// text gives an empty string for a missing field and for a field that is not a string.
	if doc.text("missing") != "" || doc.text("b") != "" {
		t.Fatal("text of a missing field or a list is not empty")
	}
	if strictObject([]byte(strings.Repeat(`{"a":`, 999)+`1`+strings.Repeat(`}`, 999))) == nil {
		t.Fatal("an object at depth 999 is refused")
	}
}

func TestHasStrings(t *testing.T) {
	doc := strictObject([]byte(`{"a":"x","b":"y"}`))
	if !doc.hasStrings("a", "b") {
		t.Fatal("the exact fields are refused")
	}
	for _, fields := range [][]string{{"a"}, {"a", "b", "c"}, {"a", "c"}} {
		if doc.hasStrings(fields...) {
			t.Errorf("fields %v: accepted, want refused", fields)
		}
	}
	if strictObject([]byte(`{"a":"x","b":1}`)).hasStrings("a", "b") {
		t.Fatal("a number field is accepted as a string")
	}
}
