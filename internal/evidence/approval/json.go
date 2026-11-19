// Package approval verifies Entelos decisions offline for the evidence gate.
package approval

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// object is a JSON object that strictObject read.
type object map[string]any

func (o object) text(name string) string {
	value, _ := o[name].(string)
	return value
}

func (o object) hasStrings(fields ...string) bool {
	if len(o) != len(fields) {
		return false
	}
	for _, field := range fields {
		if _, ok := o[field].(string); !ok {
			return false
		}
	}
	return true
}

func strictObject(raw []byte) object {
	if !utf8.Valid(raw) || !json.Valid(raw) || !validSurrogates(raw) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, ok := readValue(decoder, 0)
	if !ok {
		return nil
	}
	doc, _ := value.(object)
	return doc
}

func readValue(decoder *json.Decoder, depth int) (any, bool) {
	token, err := decoder.Token()
	if err != nil || depth >= 1000 {
		return nil, false
	}
	switch token {
	case json.Delim('{'):
		doc := make(object)
		for decoder.More() {
			name, err := decoder.Token()
			key, ok := name.(string)
			if _, found := doc[key]; err != nil || !ok || found {
				return nil, false
			}
			value, ok := readValue(decoder, depth+1)
			if !ok {
				return nil, false
			}
			doc[key] = value
		}
		_, err := decoder.Token()
		return doc, err == nil
	case json.Delim('['):
		var list []any
		for decoder.More() {
			value, ok := readValue(decoder, depth+1)
			if !ok {
				return nil, false
			}
			list = append(list, value)
		}
		_, err := decoder.Token()
		return list, err == nil
	}
	if number, ok := token.(json.Number); ok {
		if strings.ContainsAny(number.String(), ".eE") {
			_, err := number.Float64()
			return number, err == nil
		}
		return number, len(strings.TrimPrefix(number.String(), "-")) <= 4300
	}
	return token, true
}

// validSurrogates checks Unicode escapes in JSON with valid syntax.
func validSurrogates(raw []byte) bool {
	for i := bytes.IndexByte(raw, '\\'); i >= 0; i = bytes.IndexByte(raw, '\\') {
		raw = raw[i+1:]
		if raw[0] != 'u' {
			raw = raw[1:]
			continue
		}
		first, _ := strconv.ParseUint(string(raw[1:5]), 16, 16)
		raw = raw[5:]
		if !utf16.IsSurrogate(rune(first)) {
			continue
		}
		if !bytes.HasPrefix(raw, []byte(`\u`)) {
			return false
		}
		second, _ := strconv.ParseUint(string(raw[2:6]), 16, 16)
		if utf16.DecodeRune(rune(first), rune(second)) == utf8.RuneError {
			return false
		}
		raw = raw[6:]
	}
	return true
}
