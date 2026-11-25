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

// keyWords are the words that the gateway removes, at each position, from the value of a key
// header that starts with the word.
var keyWords = []string{"Bearer ", "bearer ", "Basic "}

// The limits of the key scan of one call. A call of a client has one key, and its text is short.
// The limits keep the memory and the time of the scan small for a call with much text.
const (
	// maxKeyTexts is the number of key texts that the proxy examines.
	maxKeyTexts = 16
	// maxKeyValue is the length of a value that the proxy changes or decodes to find a key.
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
// forms that the pinned gateway version reads: each header and each query value, also after the
// removal of the key words, and in the base64 text of a Basic value.
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
	texts int
	// out tells that the call has the key of an AppVersion and the pod is not of that AppVersion.
	out bool
	// limit tells that the call is over a limit of the scan.
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

// value examines each text in v that can be a gateway key of Koodam.
func (s *keyScan) value(v string) {
	scanKeys(v, s.found)
	for _, word := range keyWords {
		if !strings.HasPrefix(v, word) {
			continue
		}
		if len(v) > maxKeyValue {
			s.limit = true
			return
		}
		// The gateway removes the word at each position, so the word can divide a key.
		v = strings.ReplaceAll(v, word, "")
		scanKeys(v, s.found)
		// A Basic value can have the key in base64 text.
		scanKeys(basicText(v), s.found)
		return
	}
}

// query examines the value of each pair of a raw query. It does not make a map of the query,
// so a long query uses no memory in the scan. ReverseProxy removes each pair that the query
// parser refuses before the gateway gets the call. The scan examines such a pair also.
func (s *keyScan) query(raw string) {
	for raw != "" {
		var pair string
		pair, raw, _ = strings.Cut(raw, "&")
		_, v, _ := strings.Cut(pair, "=")
		if strings.ContainsAny(v, "%+") {
			if len(v) > maxKeyValue {
				s.limit = true
				continue
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

// basicText decodes base64 text as the decoder of the gateway does. It ignores each character
// that is not in the alphabet. It ignores a pad character in the first half of a group, and it
// stops at the pad characters that complete a group.
func basicText(v string) string {
	var out []byte
	var left byte
	group, pads := 0, 0
	for i := 0; i < len(v); i++ {
		if v[i] == '=' {
			if group >= 2 {
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
	return string(out)
}

// scanKeys calls found for each keyPrefix in v with the hexadecimal characters after it.
func scanKeys(v string, found func(key string)) {
	for i := 0; ; i += len(keyPrefix) {
		j := strings.Index(v[i:], keyPrefix)
		if j < 0 {
			return
		}
		i += j
		end := i + len(keyPrefix)
		for end < len(v) && strings.IndexByte("0123456789abcdef", v[end]) >= 0 {
			end++
		}
		if end > i+len(keyPrefix) {
			found(v[i:end])
		}
	}
}
