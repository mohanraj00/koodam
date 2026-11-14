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

// Package appclaim verifies the signed claim of a caller at the ingress of an App. The claim is
// a JWT from the issuer that the operator configures. The package never writes a token, a
// signature or a claim that it does not know to a log, a span or an error.
package appclaim

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// maxToken is the largest token that the check reads.
	maxToken = 8 << 10
	// maxLabel is the largest caller or tenant label.
	maxLabel = 253
	// leeway is the clock difference that the check accepts for exp and nbf.
	leeway = 30 * time.Second
	// minRSABits is the smallest RSA key that the check accepts.
	minRSABits = 2048
	algRS256   = "RS256"
	algES256   = "ES256"
)

// Refusal is the reason that a token is not valid. It is a fixed word, and it never has a part
// of the token.
type Refusal string

func (r Refusal) Error() string { return "the claim is not valid: " + string(r) }

// The reasons of a refusal.
const (
	RefusedSyntax    Refusal = "syntax"
	RefusedAlgorithm Refusal = "algorithm"
	RefusedKey       Refusal = "key"
	RefusedSignature Refusal = "signature"
	RefusedIssuer    Refusal = "issuer"
	RefusedAudience  Refusal = "audience"
	RefusedTime      Refusal = "time"
	RefusedCaller    Refusal = "caller"
	RefusedTenant    Refusal = "tenant"
)

// Config has the values that the operator configures for the check.
type Config struct {
	// Issuer is the necessary value of the claim iss.
	Issuer string
	// Audience is a value that the claim aud must have.
	Audience string
	// CallerClaim is the name of the claim with the caller label.
	CallerClaim string
	// TenantClaim is the name of the claim with the tenant label. If it is empty, the check
	// reads no tenant label.
	TenantClaim string
}

// Valid gives back an error if a necessary value is empty.
func (c Config) Valid() error {
	if c.Issuer == "" || c.Audience == "" || c.CallerClaim == "" {
		return errors.New("the issuer, the audience and the caller claim are necessary")
	}
	return nil
}

// Identity has the labels of a caller from a valid claim.
type Identity struct{ Caller, Tenant string }

// KeySet has the public keys of the issuer.
type KeySet struct{ keys []publicKey }

type publicKey struct {
	kid string
	rsa *rsa.PublicKey
	ec  *ecdsa.PublicKey
}

// ParseKeySet reads a JWKS document. It keeps the RSA keys and the P-256 keys that are for
// signatures, and it ignores each other key. It gives back an error if no key stays.
func ParseKeySet(data []byte) (*KeySet, error) {
	var doc struct {
		Keys []struct {
			Kty, Kid, Use, Crv, N, E, X, Y string
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, errors.New("the JWKS is not valid JSON")
	}
	set := &KeySet{}
	for _, k := range doc.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pk := publicKey{kid: k.Kid}
		switch {
		case k.Kty == "RSA":
			// The bit length of the number is the size of the key. Zero bytes at the start do not count.
			mod := new(big.Int).SetBytes(decode(k.N))
			e := decode(k.E)
			if mod.BitLen() < minRSABits || len(e) == 0 || len(e) > 4 {
				continue
			}
			exp := int(new(big.Int).SetBytes(e).Int64())
			if exp < 3 || exp%2 == 0 {
				continue
			}
			pk.rsa = &rsa.PublicKey{N: mod, E: exp}
		case k.Kty == "EC" && k.Crv == "P-256":
			x, y := decode(k.X), decode(k.Y)
			if len(x) != 32 || len(y) != 32 {
				continue
			}
			// The parse refuses a point that is not on the curve.
			ec, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err != nil {
				continue
			}
			pk.ec = ec
		default:
			continue
		}
		set.keys = append(set.keys, pk)
	}
	if len(set.keys) == 0 {
		return nil, errors.New("the JWKS has no RSA key or P-256 key for signatures")
	}
	return set, nil
}

// decode reads base64url with no padding. It gives back nil for a value that is not valid.
func decode(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// find gives back the keys that can verify a token with this algorithm and key ID. A token
// with no key ID can use each key of the algorithm.
func (s *KeySet) find(alg, kid string) []publicKey {
	var out []publicKey
	for _, k := range s.keys {
		if (kid == "" || k.kid == kid) && ((alg == algRS256 && k.rsa != nil) || (alg == algES256 && k.ec != nil)) {
			out = append(out, k)
		}
	}
	return out
}

// Verify checks the signature and the claims of a token. It accepts RS256 and ES256 only.
func (c Config) Verify(keys *KeySet, token string, now time.Time) (Identity, error) {
	if len(token) > maxToken || keys == nil {
		return Identity{}, RefusedSyntax
	}
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 3 {
		return Identity{}, RefusedSyntax
	}
	var head struct{ Alg, Kid string }
	sig := decode(parts[2])
	if json.Unmarshal(decode(parts[0]), &head) != nil || len(sig) == 0 {
		return Identity{}, RefusedSyntax
	}
	if head.Alg != algRS256 && head.Alg != algES256 {
		return Identity{}, RefusedAlgorithm
	}
	candidates := keys.find(head.Alg, head.Kid)
	if len(candidates) == 0 {
		return Identity{}, RefusedKey
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	ok := false
	for _, k := range candidates {
		if k.rsa != nil {
			ok = ok || rsa.VerifyPKCS1v15(k.rsa, crypto.SHA256, sum[:], sig) == nil
		} else if len(sig) == 64 {
			ok = ok || ecdsa.Verify(k.ec, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
		}
	}
	if !ok {
		return Identity{}, RefusedSignature
	}
	return c.claims(decode(parts[1]), now)
}

// claims checks the payload of a token with a valid signature.
func (c Config) claims(payload []byte, now time.Time) (Identity, error) {
	var set map[string]json.RawMessage
	if json.Unmarshal(payload, &set) != nil {
		return Identity{}, RefusedSyntax
	}
	if iss, ok := text(set["iss"]); !ok || iss != c.Issuer {
		return Identity{}, RefusedIssuer
	}
	if !hasAudience(set["aud"], c.Audience) {
		return Identity{}, RefusedAudience
	}
	// exp is necessary. A token with no end time is not valid.
	exp, ok := seconds(set["exp"])
	if !ok || !now.Before(exp.Add(leeway)) {
		return Identity{}, RefusedTime
	}
	if raw, has := set["nbf"]; has {
		if nbf, ok := seconds(raw); !ok || now.Add(leeway).Before(nbf) {
			return Identity{}, RefusedTime
		}
	}
	id := Identity{}
	if id.Caller, ok = label(set[c.CallerClaim]); !ok {
		return Identity{}, RefusedCaller
	}
	if c.TenantClaim != "" {
		if id.Tenant, ok = label(set[c.TenantClaim]); !ok {
			return Identity{}, RefusedTenant
		}
	}
	return id, nil
}

// text reads a JSON string.
func text(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// label reads a caller or tenant label: a string of 1 to maxLabel printable characters.
func label(raw json.RawMessage) (string, bool) {
	s, ok := text(raw)
	if !ok || s == "" || len(s) > maxLabel || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return s, true
}

// hasAudience is true if the claim aud is the audience or a list with the audience.
func hasAudience(raw json.RawMessage, want string) bool {
	if one, ok := text(raw); ok {
		return one == want
	}
	var list []string
	return json.Unmarshal(raw, &list) == nil && slices.Contains(list, want)
}

// seconds reads a JSON number of seconds from the Unix epoch.
func seconds(raw json.RawMessage) (time.Time, bool) {
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if len(raw) == 0 || d.Decode(&n) != nil {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil || f < 0 || f > 1e11 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}
