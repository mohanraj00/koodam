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

// Package tenanttoken has the signed token that binds a model call of an App pod to the tenant
// of a request. The claim check container signs the token. The egress proxy examines it.
package tenanttoken

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	// Header is the header with the token. The claim check container gives it to the app, and
	// the app sends it with each model call.
	Header = "Koodam-Tenant-Token"
	// SeedSize is the length of the private seed of a key.
	SeedSize = ed25519.SeedSize

	// version starts each token. The signature includes it.
	version = "v1."
	// maxToken is the largest token. A token of the largest tenant label is smaller.
	maxToken = 4096
	// maxTenant is the largest tenant label, as in the claim check.
	maxTenant = 253
)

// The errors of Verify.
var (
	// ErrSyntax tells that the text is not a token of this version.
	ErrSyntax = errors.New("the tenant token is not valid")
	// ErrSignature tells that the key of the pod did not sign the token.
	ErrSignature = errors.New("the tenant token has a false signature")
	// ErrExpired tells that the token is at its end time or after it.
	ErrExpired = errors.New("the tenant token is after its end time")
)

// body is the signed data of a token.
type body struct {
	// Tenant is the tenant label of the request.
	Tenant string `json:"tenant"`
	// End is the end time of the token, in seconds of Unix time.
	End int64 `json:"end"`
}

// PrivateKey gives back the private key of a seed. The seed is the content of the key file of
// the claim check container.
func PrivateKey(seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != SeedSize {
		return nil, errors.New("the seed of the tenant token key does not have the correct length")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// PublicText gives back the public key of a private key as text. The controller writes this
// text to the status of the AppVersion.
func PublicText(key ed25519.PrivateKey) string {
	pub, _ := key.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub)
}

// PublicKey reads the text of PublicText.
func PublicKey(text string) (ed25519.PublicKey, error) {
	pub, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("the public key of the tenant token is not valid")
	}
	return pub, nil
}

// Sign gives back the token of a tenant label with an end time.
func Sign(key ed25519.PrivateKey, tenant string, end time.Time) string {
	// The data has a string and an integer only, so Marshal gives no error.
	data, _ := json.Marshal(body{Tenant: tenant, End: end.Unix()})
	signed := version + base64.RawURLEncoding.EncodeToString(data)
	return signed + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signed)))
}

// Verify gives back the tenant label of a token that the key signed and that is before its end
// time. It examines the signature before it reads the data.
func Verify(key ed25519.PublicKey, token string, now time.Time) (string, error) {
	dot := strings.LastIndexByte(token, '.')
	if len(token) > maxToken || !strings.HasPrefix(token, version) || dot < len(version) {
		return "", ErrSyntax
	}
	sig, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return "", ErrSyntax
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte(token[:dot]), sig) {
		return "", ErrSignature
	}
	data, err := base64.RawURLEncoding.DecodeString(token[len(version):dot])
	if err != nil {
		return "", ErrSyntax
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b body
	if err := dec.Decode(&b); err != nil || b.Tenant == "" || len(b.Tenant) > maxTenant {
		return "", ErrSyntax
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", ErrSyntax
	}
	if !now.Before(time.Unix(b.End, 0)) {
		return "", ErrExpired
	}
	return b.Tenant, nil
}
