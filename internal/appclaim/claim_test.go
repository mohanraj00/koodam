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

package appclaim

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

var (
	now     = time.Unix(1_800_000_000, 0)
	testCfg = Config{Issuer: "https://issuer.example.com", Audience: testAud, CallerClaim: "sub", TenantClaim: "tenant"}
	enc     = base64.RawURLEncoding.EncodeToString
)

const (
	keyID      = "kid"
	testAud    = "shop"
	testCaller = "caller-1"
	testTenant = "team-a"
)

// header makes the header of a token.
func header(alg, kid string) map[string]any {
	h := map[string]any{"alg": alg}
	if kid != "" {
		h[keyID] = kid
	}
	return h
}

// fixture has 2 test keys that the test makes, and the JWKS with their public parts.
type fixture struct {
	rsa  *rsa.PrivateKey
	ec   *ecdsa.PrivateKey
	jwks []byte
	keys *KeySet
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	var err error
	if f.rsa, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if f.ec, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	point, err := f.ec.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	// The last 2 keys are not for signatures, so the check ignores them.
	f.jwks = fmt.Appendf(nil, `{"keys":[{"kty":"RSA","kid":"r1","use":"sig","n":%q,"e":%q},`+
		`{"kty":"EC","kid":"e1","crv":"P-256","x":%q,"y":%q},`+
		`{"kty":"RSA","kid":"e2","use":"enc","n":%q,"e":"AQAB"},{"kty":"oct","kid":"h1","k":"AAAA"}]}`,
		enc(f.rsa.N.Bytes()), enc(big.NewInt(int64(f.rsa.E)).Bytes()), enc(point[1:33]), enc(point[33:]),
		enc(f.rsa.N.Bytes()))
	if f.keys, err = ParseKeySet(f.jwks); err != nil {
		t.Fatal(err)
	}
	return f
}

// sign makes a token with the algorithm of the header.
func (f *fixture) sign(t *testing.T, head, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(head)
	c, _ := json.Marshal(claims)
	input := enc(h) + "." + enc(c)
	sum := sha256.Sum256([]byte(input))
	var sig []byte
	var err error
	if head["alg"] == algES256 {
		r, s, e := ecdsa.Sign(rand.Reader, f.ec, sum[:])
		sig, err = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...), e
	} else {
		sig, err = rsa.SignPKCS1v15(rand.Reader, f.rsa, crypto.SHA256, sum[:])
	}
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + enc(sig)
}

func claims(change func(map[string]any)) map[string]any {
	c := map[string]any{"iss": testCfg.Issuer, "aud": []string{"other", testAud}, "exp": now.Unix() + 60,
		"sub": testCaller, "tenant": testTenant}
	if change != nil {
		change(c)
	}
	return c
}

func TestVerifyAcceptsBothAlgorithms(t *testing.T) {
	f := newFixture(t)
	if len(f.keys.keys) != 2 {
		t.Fatalf("The key set has %d keys, not the 2 signature keys.", len(f.keys.keys))
	}
	for _, head := range []map[string]any{
		header(algRS256, "r1"), header(algES256, "e1"), header(algRS256, ""), header(algES256, ""),
	} {
		id, err := testCfg.Verify(f.keys, f.sign(t, head, claims(func(c map[string]any) { c["aud"] = testAud })), now)
		if err != nil || id != (Identity{Caller: testCaller, Tenant: testTenant}) {
			t.Errorf("%v: %v, %v", head, id, err)
		}
	}
	noTenant := testCfg
	noTenant.TenantClaim = ""
	id, err := noTenant.Verify(f.keys, f.sign(t, header(algES256, ""),
		claims(func(c map[string]any) { delete(c, "tenant") })), now)
	if err != nil || id != (Identity{Caller: testCaller}) {
		t.Errorf("no tenant claim: %v, %v", id, err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	f := newFixture(t)
	rs := header(algRS256, "r1")
	good := f.sign(t, rs, claims(nil))
	parts := strings.Split(good, ".")
	other, _ := json.Marshal(claims(func(c map[string]any) { c["sub"] = "admin" }))
	set := func(k string, v any) string {
		return f.sign(t, rs, claims(func(c map[string]any) {
			if v == nil {
				delete(c, k)
			} else {
				c[k] = v
			}
		}))
	}
	cases := []struct {
		name, token string
		want        Refusal
	}{
		{"empty", "", RefusedSyntax},
		{"2 parts", parts[0] + "." + parts[1], RefusedSyntax},
		{"too large", good + strings.Repeat("A", maxToken), RefusedSyntax},
		{"header not JSON", "e30x." + parts[1] + "." + parts[2], RefusedSyntax},
		{"alg none", enc([]byte(`{"alg":"none"}`)) + "." + parts[1] + ".AAAA", RefusedAlgorithm},
		{"alg HS256", f.sign(t, header("HS256", ""), claims(nil)), RefusedAlgorithm},
		{"key ID not known", f.sign(t, header(algRS256, "x"), claims(nil)), RefusedKey},
		{"key of the other algorithm", f.sign(t, header(algRS256, "e1"), claims(nil)), RefusedKey},
		{"encryption key", f.sign(t, header(algRS256, "e2"), claims(nil)), RefusedKey},
		{"changed payload", parts[0] + "." + enc(other) + "." + parts[2], RefusedSignature},
		{"RSA signature for ES256", enc([]byte(`{"alg":"ES256"}`)) + "." + parts[1] + "." + parts[2], RefusedSignature},
		{"issuer", set("iss", "https://other.example.com"), RefusedIssuer},
		{"audience", set("aud", []string{"other"}), RefusedAudience},
		{"no audience", set("aud", nil), RefusedAudience},
		{"expired", set("exp", now.Unix()-31), RefusedTime},
		{"no exp", set("exp", nil), RefusedTime},
		{"exp is text", set("exp", "never"), RefusedTime},
		{"not yet valid", set("nbf", now.Unix()+31), RefusedTime},
		{"no caller", set("sub", nil), RefusedCaller},
		{"caller is not text", set("sub", 7), RefusedCaller},
		{"caller with a control character", set("sub", "a\nb"), RefusedCaller},
		{"no tenant", set("tenant", nil), RefusedTenant},
		{"tenant too long", set("tenant", strings.Repeat("t", maxLabel+1)), RefusedTenant},
	}
	for _, c := range cases {
		_, err := testCfg.Verify(f.keys, c.token, now)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
		if err != nil && c.token != "" && strings.Contains(err.Error(), parts[1][:20]) {
			t.Errorf("%s: the error has a part of the token", c.name)
		}
	}
	if _, err := testCfg.Verify(nil, good, now); !errors.Is(err, RefusedSyntax) {
		t.Errorf("no key set: %v", err)
	}
	// The clock difference that the check accepts.
	if _, err := testCfg.Verify(f.keys, set("exp", now.Unix()-29), now); err != nil {
		t.Errorf("exp in the leeway: %v", err)
	}
}

func TestParseKeySetRefuses(t *testing.T) {
	small, _ := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // The test needs a key that is too small.
	for name, doc := range map[string]string{
		"not JSON":      "{",
		"no key":        `{"keys":[]}`,
		"small RSA key": `{"keys":[{"kty":"RSA","n":"` + enc(small.N.Bytes()) + `","e":"AQAB"}]}`,
		"even exponent": `{"keys":[{"kty":"RSA","n":"` + enc(make([]byte, 256)) + `","e":"Ag"}]}`,
		"point not on the curve": `{"keys":[{"kty":"EC","crv":"P-256","x":"` + enc(bytes.Repeat([]byte{1}, 32)) +
			`","y":"` + enc(bytes.Repeat([]byte{1}, 32)) + `"}]}`,
		"other curve": `{"keys":[{"kty":"EC","crv":"P-384","x":"AA","y":"AA"}]}`,
	} {
		if _, err := ParseKeySet([]byte(doc)); err == nil {
			t.Errorf("%s: the key set is accepted", name)
		}
	}
	if err := (Config{Issuer: "i", Audience: "a"}).Valid(); err == nil {
		t.Error("A configuration with no caller claim is accepted.")
	}
}
