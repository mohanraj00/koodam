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

package tenanttoken

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Unix(1_800_000_000, 0)

func testKey(t *testing.T, fill byte) ed25519.PrivateKey {
	t.Helper()
	key, err := PrivateKey(bytes.Repeat([]byte{fill}, SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func public(t *testing.T, key ed25519.PrivateKey) ed25519.PublicKey {
	t.Helper()
	pub, err := PublicKey(PublicText(key))
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestSignAndVerify(t *testing.T) {
	key := testKey(t, 1)
	for _, tenant := range []string{"acme", "a b", "tenant-ü", strings.Repeat("<", maxTenant)} {
		token := Sign(key, tenant, now.Add(time.Minute))
		if len(token) > maxToken {
			t.Fatalf("The token of %q has %d bytes.", tenant, len(token))
		}
		got, err := Verify(public(t, key), token, now)
		if err != nil || got != tenant {
			t.Fatalf("Verify(%q) = %q, %v", tenant, got, err)
		}
	}
}

func TestVerifyRefuses(t *testing.T) {
	key, other := testKey(t, 1), testKey(t, 2)
	pub := public(t, key)
	good := Sign(key, "acme", now.Add(time.Minute))
	dot := strings.LastIndexByte(good, '.')
	// The data of a different tenant with the signature of the token for "acme".
	swapped := Sign(key, "other", now.Add(time.Minute))
	swapped = swapped[:strings.LastIndexByte(swapped, '.')] + good[dot:]
	sign := func(data string) string {
		signed := version + base64.RawURLEncoding.EncodeToString([]byte(data))
		return signed + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signed)))
	}
	cases := []struct {
		name, token string
		key         ed25519.PublicKey
		want        error
	}{
		{"an empty token", "", pub, ErrSyntax},
		{"no version", strings.TrimPrefix(good, version), pub, ErrSyntax},
		{"a different version", "v2." + strings.TrimPrefix(good, version), pub, ErrSyntax},
		{"no signature part", good[:dot], pub, ErrSyntax},
		{"a signature that is not base64", good[:dot] + ".*", pub, ErrSyntax},
		{"a token that is too large", good + strings.Repeat("A", maxToken), pub, ErrSyntax},
		{"the key of a different pod", Sign(other, "acme", now.Add(time.Minute)), pub, ErrSignature},
		{"a changed tenant label", swapped, pub, ErrSignature},
		{"a cut signature", good[:len(good)-2], pub, ErrSignature},
		{"a public key that is not valid", good, pub[:8], ErrSignature},
		{"the end time", Sign(key, "acme", now), pub, ErrExpired},
		{"a time after the end time", Sign(key, "acme", now.Add(-time.Second)), pub, ErrExpired},
		{"no tenant label", Sign(key, "", now.Add(time.Minute)), pub, ErrSyntax},
		{"a tenant label that is too long", Sign(key, strings.Repeat("a", maxTenant+1), now.Add(time.Minute)), pub, ErrSyntax},
		{"an unknown field", sign(`{"tenant":"acme","end":1900000000,"x":1}`), pub, ErrSyntax},
		{"data after the object", sign(`{"tenant":"acme","end":1900000000}{}`), pub, ErrSyntax},
		{"data that is not an object", sign(`"acme"`), pub, ErrSyntax},
	}
	for _, c := range cases {
		if got, err := Verify(c.key, c.token, now); !errors.Is(err, c.want) || got != "" {
			t.Errorf("%s: Verify = %q, %v; the error must be %v", c.name, got, err, c.want)
		}
	}
}

func TestKeyTexts(t *testing.T) {
	if _, err := PrivateKey(make([]byte, SeedSize-1)); err == nil {
		t.Error("A short seed must give an error.")
	}
	for _, text := range []string{"", "*", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := PublicKey(text); err == nil {
			t.Errorf("PublicKey(%q) must give an error.", text)
		}
	}
}
