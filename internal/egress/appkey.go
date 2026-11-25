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

// appKeyInPod is false if the model call has the gateway key of an AppVersion and the source
// pod is not a pod of that AppVersion. An App pod can send its key to a different pod. This
// check stops the use of that key through the proxy, where the call has no tenant token.
//
// The check is defense in depth, not a full bind. It finds a key only in the locations and the
// forms that the pinned gateway version reads: each header and each query value, also after the
// removal of the key words, and in the base64 text of a Basic value.
func (p *Proxy) appKeyInPod(r *http.Request, sp *Span) bool {
	if p.AppKeyOwner == nil {
		return true
	}
	in := true
	examine := func(key string) {
		hash := sha256.Sum256([]byte(key))
		namespace, version, found := p.AppKeyOwner(hex.EncodeToString(hash[:]))
		if found && (sp.App == "" || sp.Namespace != namespace || sp.AppVersion != version) {
			in = false
		}
	}
	for _, values := range r.Header {
		for _, v := range values {
			keyTexts(v, examine)
		}
	}
	// The gateway gets only the query pairs that this parser accepts: ReverseProxy removes each
	// other pair before the rewrite function.
	for _, values := range r.URL.Query() {
		for _, v := range values {
			keyTexts(v, examine)
		}
	}
	return in
}

// keyTexts calls found for each text in v that can be a gateway key of Koodam.
func keyTexts(v string, found func(key string)) {
	scanKeys(v, found)
	for _, word := range keyWords {
		if !strings.HasPrefix(v, word) {
			continue
		}
		// The gateway removes the word at each position, so the word can divide a key.
		v = strings.ReplaceAll(v, word, "")
		scanKeys(v, found)
		// A Basic value can have the key in base64 text.
		scanKeys(basicText(v), found)
		return
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
