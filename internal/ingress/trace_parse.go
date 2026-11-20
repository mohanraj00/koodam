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
	"encoding/hex"
	"strings"
)

// The sizes of the IDs in bytes, and the offsets of the fields in a traceparent value (W3C Trace
// Context 2021, 3.2.2). The format is vv-<trace-id>-<parent-id>-ff.
const (
	traceIDSize  = 16
	parentIDSize = 8
	traceIDAt    = 3
	parentIDAt   = traceIDAt + 2*traceIDSize + 1
	flagsAt      = parentIDAt + 2*parentIDSize + 1
	coreSize     = flagsAt + 2 // The size of a version 00 value.
	sampledFlag  = 0x01
	hexDigits    = "0123456789abcdef"
)

// parse checks the traceparent value v. It returns the trace-id and only the sampled flag. For a
// version higher than 00, it checks the core fields and the dash after them. It does not read the
// fields after the core (W3C Trace Context 2021, 3.2.4).
func parse(v string) (traceID [traceIDSize]byte, flags byte, ok bool) {
	if len(v) < coreSize {
		return traceID, 0, false
	}
	version, trace := v[:traceIDAt-1], v[traceIDAt:parentIDAt-1]
	parent, flag := v[parentIDAt:flagsAt-1], v[flagsAt:coreSize]
	valid := v[traceIDAt-1] == '-' && v[parentIDAt-1] == '-' && v[flagsAt-1] == '-' &&
		lowerHex(version) && lowerHex(trace) && lowerHex(parent) && lowerHex(flag) &&
		version != "ff" && (len(v) == coreSize || (version != "00" && v[coreSize] == '-')) &&
		strings.Trim(trace, "0") != "" && strings.Trim(parent, "0") != ""
	if !valid {
		return traceID, 0, false
	}
	var f [1]byte
	_, errTrace := hex.Decode(traceID[:], []byte(trace))
	_, errFlags := hex.Decode(f[:], []byte(flag))
	if errTrace != nil || errFlags != nil {
		return [traceIDSize]byte{}, 0, false
	}
	return traceID, f[0] & sampledFlag, true
}

// lowerHex tells if s has only lower-case hex characters.
func lowerHex(s string) bool {
	return strings.Trim(s, hexDigits) == ""
}
