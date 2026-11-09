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

package ingress

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

// parseIDs returns a random trace-id, and the trace-id and a random parent-id in lower-case hex.
// The IDs are not all zero. The test keeps them in memory only.
func parseIDs(t *testing.T) (traceID [traceIDSize]byte, trace, parent string) {
	t.Helper()
	b := make([]byte, traceIDSize+parentIDSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal("cannot read random bytes")
	}
	b[0] |= 1
	b[traceIDSize] |= 1
	copy(traceID[:], b)
	return traceID, hex.EncodeToString(b[:traceIDSize]), hex.EncodeToString(b[traceIDSize:])
}

// parseValue joins the fields of a traceparent value with dashes.
func parseValue(fields ...string) string {
	return strings.Join(fields, "-")
}

func TestParseValid(t *testing.T) {
	for _, tc := range []struct {
		name, version, flags, suffix string
		want                         byte
	}{
		{"version 00 not sampled", "00", "00", "", 0},
		{"version 00 sampled", "00", "01", "", sampledFlag},
		{"version 00 unknown flags", "00", "fe", "", 0},
		{"version 00 all flags", "00", "ff", "", sampledFlag},
		{"higher version core only", "01", "03", "", sampledFlag},
		{"higher version empty suffix", "0a", "00", "-", 0},
		{"higher version unknown fields", "01", "a1", "-x,Y-", sampledFlag},
		{"highest version unknown fields", "fe", "02", "-zz", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, trace, parent := parseIDs(t)
			traceID, flags, ok := parse(parseValue(tc.version, trace, parent, tc.flags) + tc.suffix)
			if !ok || traceID != want || flags != tc.want {
				t.Error("parse did not return the trace-id and only the sampled flag")
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	_, trace, parent := parseIDs(t)
	v00, v01 := parseValue("00", trace, parent, "01"), parseValue("01", trace, parent, "01")
	zeroTrace, zeroParent := strings.Repeat("0", 2*traceIDSize), strings.Repeat("0", 2*parentIDSize)
	set := func(v string, i int, c byte) string {
		b := []byte(v)
		b[i] = c
		return string(b)
	}
	for _, tc := range []struct{ name, v string }{
		{litCaseEmptyValue, ""},
		{"version 00 too short", v00[:coreSize-1]},
		{"higher version too short", v01[:coreSize-1]},
		{"version 00 too long", v00 + "0"},
		{"version 00 with dash suffix", v00 + "-"},
		{"version 00 with unknown fields", v00 + "-00"},
		{"higher version suffix without dash", v01 + "0"},
		{"long version", parseValue("000", trace, parent, "01")},
		{litCaseShortTraceID, parseValue("00", trace[1:], parent, "010")},
		{litCaseShortParentID, parseValue("00", trace+"0", parent[1:], "01")},
		{"no dash after version", set(v00, traceIDAt-1, '_')},
		{"no dash after trace-id", set(v00, parentIDAt-1, '_')},
		{"no dash after parent-id", set(v00, flagsAt-1, '_')},
		{"upper-case version", parseValue("0A", trace, parent, "01")},
		{"upper-case trace-id", set(v00, traceIDAt, 'A')},
		{"upper-case parent-id", set(v00, parentIDAt, 'F')},
		{"upper-case flags", parseValue("00", trace, parent, "0A")},
		{"non-hex version", parseValue("0g", trace, parent, "01")},
		{"non-hex trace-id", set(v00, parentIDAt-2, 'g')},
		{"non-hex parent-id", set(v00, flagsAt-2, 'x')},
		{"non-hex flags", set(v00, coreSize-1, 'g')},
		{"non-ASCII byte", set(v00, traceIDAt, 0xc3)},
		{litCaseLeadingSpace, " " + v00},
		{"version ff", parseValue("ff", trace, parent, "01")},
		{"version ff with suffix", parseValue("ff", trace, parent, "01", "00")},
		{litCaseZeroTraceID, parseValue("00", zeroTrace, parent, "01")},
		{litCaseZeroParentID, parseValue("00", trace, zeroParent, "01")},
		{"higher version zero trace-id", parseValue("01", zeroTrace, parent, "01")},
		{"higher version zero parent-id", parseValue("01", trace, zeroParent, "01")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			traceID, flags, ok := parse(tc.v)
			if ok || traceID != ([traceIDSize]byte{}) || flags != 0 {
				t.Error("parse did not refuse the value with an empty result")
			}
		})
	}
}

func TestLowerHex(t *testing.T) {
	if !lowerHex("") || !lowerHex(hexDigits) {
		t.Error("lowerHex refused lower-case hex characters")
	}
	for _, s := range []string{"A", "F", "g", "-", " ", "\xc3"} {
		if lowerHex(s) {
			t.Error("lowerHex accepted a character that is not lower-case hex")
		}
	}
}
