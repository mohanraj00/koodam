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

package egress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

// ReasonAppKeyOutOfPod tells that the model call has the gateway key of an AppVersion, and the
// source pod is not a pod of that AppVersion.
const ReasonAppKeyOutOfPod = "AppKeyOutOfPod"

// keyPrefix is the start of each gateway key that Koodam makes. Hexadecimal characters in
// lower case follow it. The values must agree with gateway.NewKeyValue.
const keyPrefix = "sk-kd-"

// basicWord is the word that the gateway removes from a value before it decodes base64 text.
const basicWord = "Basic "

// keyWords are the words that the gateway removes, at each position, from a key value that
// starts with the word.
var keyWords = []string{"Bearer ", "bearer ", basicWord}

var keyPrefixBytes = []byte(keyPrefix)

// The limits of the key scan of one call. A call of a client has one key, and its text is short.
// The limits keep the memory and the time of the scan small for a call with much text.
const (
	// maxKeyTexts is the number of key texts that the proxy examines.
	maxKeyTexts = 16
	// maxKeyValue is the length of a header value, a query value or a path that the proxy
	// examines.
	maxKeyValue = 64 << 10
)

// ReasonKeyScanLimit tells that the model call has more key texts, or a longer key value, than
// the proxy examines. The proxy refuses such a call, because a key can be in the part that it
// did not examine.
const ReasonKeyScanLimit = "KeyScanLimit"

// appKeyReason gives the reason to refuse the model call because of a gateway key in it, or "".
// The reason is ReasonAppKeyOutOfPod if the call has the gateway key of an AppVersion and the
// source pod is not a pod of that AppVersion. An App pod can send its key to a different pod.
// This check stops the use of that key through the proxy, where the call has no tenant token.
//
// The check is defense in depth, not a full bind. It finds a key only in the locations and the
// forms that the pinned gateway version reads. The scan examines more than the gateway reads:
// each header value, each query value and the path, in each form of keyScan.value.
func (p *Proxy) appKeyReason(r *http.Request, sp *Span) string {
	if p.AppKeyOwner == nil {
		return ""
	}
	s := newKeyScan(p.AppKeyOwner, sp)
	for _, values := range r.Header {
		for _, v := range values {
			s.value(v)
		}
	}
	s.query(r.URL.RawQuery)
	s.value(r.URL.Path)
	switch {
	case s.out:
		return ReasonAppKeyOutOfPod
	case s.limit:
		return ReasonKeyScanLimit
	}
	return ""
}

// keyScan examines the key texts of one model call.
type keyScan struct {
	owner func(keyHash string) (namespace, appVersion string, ok bool)
	sp    *Span
	found func(key string)
	// buf has the decoded base64 text of one value. The scan uses it again for each value.
	buf   []byte
	texts int
	// out tells that the call has the key of an AppVersion and the pod is not of that AppVersion.
	out bool
	// limit tells that the call is over a limit of the scan. The scan stops then.
	limit bool
}

// newKeyScan makes the scan of one call. It makes the function value of text one time.
func newKeyScan(owner func(string) (string, string, bool), sp *Span) *keyScan {
	s := &keyScan{owner: owner, sp: sp}
	s.found = s.text
	return s
}

// text examines one text that can be a gateway key.
func (s *keyScan) text(key string) {
	if s.texts++; s.texts > maxKeyTexts {
		s.limit = true
		return
	}
	hash := sha256.Sum256([]byte(key))
	namespace, version, found := s.owner(hex.EncodeToString(hash[:]))
	if found && (s.sp.App == "" || s.sp.Namespace != namespace || s.sp.AppVersion != version) {
		s.out = true
	}
}

// value examines each text in v that can be a gateway key of Koodam. The gateway reads a key
// from a value in these forms, and the scan examines each of them for each value:
//
//   - The value with no change.
//   - The value with one key word removed at each position. The gateway does this for a value
//     that starts with the word, and for the key of a WebSocket. Thus a word can divide a key.
//   - The base64 text of each of these, after the removal of the word "Basic ". A pass-through
//     endpoint of the gateway can read the key from such text.
func (s *keyScan) value(v string) {
	if s.limit {
		return
	}
	scanKeys(v, s.found)
	if len(v) > maxKeyValue {
		// The scan does not change or decode a longer value, and a key can be in it.
		s.limit = true
		return
	}
	s.basic(v)
	for _, word := range keyWords {
		if strings.Contains(v, word) {
			t := strings.ReplaceAll(v, word, "")
			scanKeys(t, s.found)
			s.basic(t)
		}
	}
}

// basic examines the base64 text of v. The decoder of the gateway is the decoder of its Python
// version, and 2 behaviors are in use. The scan examines the result of each.
func (s *keyScan) basic(v string) {
	// The decoded text of a shorter value cannot hold a key.
	if len(v) < len(keyPrefix)*4/3 {
		return
	}
	v = strings.ReplaceAll(v, basicWord, "")
	for _, stop := range []bool{true, false} {
		s.buf = basicText(s.buf[:0], v, stop)
		scanKeyBytes(s.buf, s.found)
	}
}

// query examines the value of each pair of a raw query. It does not make a map of the query,
// so a long query uses no memory in the scan. ReverseProxy removes each pair that the query
// parser refuses before the gateway gets the call. The scan examines such a pair also.
func (s *keyScan) query(raw string) {
	for raw != "" && !s.limit {
		var pair string
		pair, raw, _ = strings.Cut(raw, "&")
		_, v, _ := strings.Cut(pair, "=")
		if strings.ContainsAny(v, "%+") {
			if len(v) > maxKeyValue {
				s.limit = true
				return
			}
			u, err := url.QueryUnescape(v)
			if err != nil {
				continue
			}
			v = u
		}
		s.value(v)
	}
}

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// basicText adds the decoded base64 text of v to out, as the decoder of the gateway does it.
// The decoder ignores each character that is not in the alphabet. For a pad character, the
// behavior is different between the Python versions:
//
//   - With stop, the decoder ignores a pad character in the first half of a group, and it stops
//     at the pad characters that complete a group. Python before 3.13 does this.
//   - With no stop, the decoder ignores each pad character. Python 3.13 and later do this.
//
// Where the Python decoder gives an error, basicText gives the text before the error. The
// gateway accepts no key then, so the scan examines more than is necessary.
func basicText(out []byte, v string, stop bool) []byte {
	var left byte
	group, pads := 0, 0
	for i := 0; i < len(v); i++ {
		if v[i] == '=' {
			if stop && group >= 2 {
				if pads++; group+pads >= 4 {
					break
				}
			}
			continue
		}
		d := strings.IndexByte(base64Alphabet, v[i])
		if d < 0 {
			continue
		}
		pads = 0
		b := byte(d)
		switch group {
		case 0:
			left = b
		case 1:
			out, left = append(out, left<<2|b>>4), b&0x0f
		case 2:
			out, left = append(out, left<<4|b>>2), b&0x03
		case 3:
			out = append(out, left<<6|b)
		}
		group = (group + 1) % 4
	}
	return out
}

const hexDigits = "0123456789abcdef"

// scanKeys calls found for each keyPrefix in v with the hexadecimal characters after it.
func scanKeys(v string, found func(key string)) {
	for i := 0; ; i += len(keyPrefix) {
		j := strings.Index(v[i:], keyPrefix)
		if j < 0 {
			return
		}
		i += j
		end := i + len(keyPrefix)
		for end < len(v) && strings.IndexByte(hexDigits, v[end]) >= 0 {
			end++
		}
		if end > i+len(keyPrefix) {
			found(v[i:end])
		}
	}
}

// scanKeyBytes is scanKeys for bytes. It makes a string only for a text that it finds.
func scanKeyBytes(v []byte, found func(key string)) {
	for i := 0; ; i += len(keyPrefix) {
		j := bytes.Index(v[i:], keyPrefixBytes)
		if j < 0 {
			return
		}
		i += j
		end := i + len(keyPrefix)
		for end < len(v) && strings.IndexByte(hexDigits, v[end]) >= 0 {
			end++
		}
		if end > i+len(keyPrefix) {
			found(string(v[i:end]))
		}
	}
}
