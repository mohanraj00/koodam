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

package harness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestVerifyRealBundle verifies the cosign bundle of the codex release rust-v0.163.0 against the
// embedded Sigstore trust anchors. The sha256 is the sha256 of the binary in the release archive.
func TestVerifyRealBundle(t *testing.T) {
	b, err := os.ReadFile("testdata/codex-0.163.0-x86_64-unknown-linux-musl.sigstore")
	if err != nil {
		t.Fatal(err)
	}
	got, p, err := verifyBundle(b, "0.163.0")
	if err != nil || got != "399ff69c431c066e40de318fba45c2b07f45a76b19939d247ee4828fc70c4215" || *p.LogIndex != 3267983038 {
		t.Fatalf("verifyBundle = %q, %d, %v", got, *p.LogIndex, err)
	}
	if _, _, err := verifyBundle(b, "0.163.1"); err == nil || !strings.Contains(err.Error(), identity) {
		t.Fatalf("verifyBundle for a different tag = %v, want an identity error", err)
	}
}

// identity is in the error for a wrong certificate identity.
const identity = "identity"

// fakeSigstore is a test Fulcio CA and a test Rekor key. It replaces the trust anchors.
type fakeSigstore struct {
	inter              *x509.Certificate
	interKey, rekorKey *ecdsa.PrivateKey
}

// fakeOpts changes one part of a fake bundle.
type fakeOpts struct {
	uri, issuerV1, issuerV2, kind, logID string
	signed                               time.Time
	integrated                           time.Time
	badSig, badSET                       bool
	bodySig, bodyCert                    []byte
	ca                                   *fakeSigstore
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newCert(t *testing.T, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, key *ecdsa.PrivateKey) (*x509.Certificate, []byte) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// makeCA makes a root and an intermediate certificate. It returns the CA and the PEM of each.
func makeCA(t *testing.T) (*fakeSigstore, []byte, []byte) {
	now := time.Now()
	ca := func(cn string) *x509.Certificate {
		return &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
			NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true,
			BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	}
	rootKey, f := newKey(t), &fakeSigstore{interKey: newKey(t), rekorKey: newKey(t)}
	root, rootPEM := newCert(t, ca("root"), ca("root"), &rootKey.PublicKey, rootKey)
	inter, interPEM := newCert(t, ca("intermediate"), root, &f.interKey.PublicKey, rootKey)
	f.inter = inter
	return f, rootPEM, interPEM
}

// newFakeSigstore replaces the trust anchors with a test CA and a test Rekor key until the test ends.
func newFakeSigstore(t *testing.T) *fakeSigstore {
	f, root, inter := makeCA(t)
	der, err := x509.MarshalPKIXPublicKey(&f.rekorKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	old := trust
	trust = newTrust(root, inter, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	t.Cleanup(func() { trust = old })
	return f
}

// bundle returns a cosign bundle for the binary sum of the codex release rust-v0.9.1, and
// the Rekor entry body in the bundle.
func (f *fakeSigstore) bundle(t *testing.T, sum string, edit func(*fakeOpts)) (string, string) {
	now := time.Now().Truncate(time.Second)
	o := fakeOpts{uri: "https://github.com/openai/codex/.github/workflows/rust-release.yml@refs/tags/rust-v0.9.1",
		issuerV1: githubIssuer, issuerV2: githubIssuer, kind: "hashedrekord", logID: trust.rekorID,
		signed: now.Add(-time.Hour), integrated: now.Add(-time.Hour + time.Minute), ca: f}
	if edit != nil {
		edit(&o)
	}
	u, err := url.Parse(o.uri)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: o.signed, NotAfter: o.signed.Add(10 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs: []*url.URL{u}}
	if o.issuerV1 != "" {
		tmpl.ExtraExtensions = append(tmpl.ExtraExtensions, pkix.Extension{Id: oidIssuerV1, Value: []byte(o.issuerV1)})
	}
	if o.issuerV2 != "" {
		v, err := asn1.MarshalWithParams(o.issuerV2, "utf8")
		if err != nil {
			t.Fatal(err)
		}
		tmpl.ExtraExtensions = append(tmpl.ExtraExtensions, pkix.Extension{Id: oidIssuerV2, Value: v})
	}
	key := newKey(t)
	_, certPEM := newCert(t, tmpl, o.ca.inter, &key.PublicKey, o.ca.interKey)
	digest, _ := hex.DecodeString(sum)
	if o.badSig {
		digest = []byte("a different binary")
	}
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest)
	if err != nil {
		t.Fatal(err)
	}
	if o.bodySig == nil {
		o.bodySig = sig
	}
	if o.bodyCert == nil {
		o.bodyCert = certPEM
	}
	b64 := base64.StdEncoding.EncodeToString
	body := b64(fmt.Appendf(nil, `{"apiVersion":"0.0.1","kind":%q,"spec":{"data":{"hash":{"algorithm":"sha256",`+
		`"value":%q}},"signature":{"content":%q,"publicKey":{"content":%q}}}}`, o.kind, sum, b64(o.bodySig), b64(o.bodyCert)))
	idx := int64(42)
	p := rekorPayload{Body: body, IntegratedTime: o.integrated.Unix(), LogID: o.logID, LogIndex: &idx}
	msg, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if o.badSET {
		msg = append(msg, ' ')
	}
	h := sha256.Sum256(msg)
	set, err := ecdsa.SignASN1(rand.Reader, f.rekorKey, h[:])
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"base64Signature": b64(sig), "cert": b64(certPEM),
		"rekorBundle": map[string]any{"SignedEntryTimestamp": b64(set), "Payload": p}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b), body
}

func TestVerifyBundle(t *testing.T) {
	f := newFakeSigstore(t)
	other, _, _ := makeCA(t)
	s := sum([]byte("codex binary"))
	for _, c := range []struct {
		name string
		edit func(*fakeOpts)
		want string
	}{
		{"valid", nil, ""},
		{"only the old issuer", func(o *fakeOpts) { o.issuerV2 = "" }, ""},
		{"only the new issuer", func(o *fakeOpts) { o.issuerV1 = "" }, ""},
		{"bad signature", func(o *fakeOpts) { o.badSig = true }, "signature is not valid"},
		{"wrong workflow", func(o *fakeOpts) { o.uri = strings.Replace(o.uri, "rust-release", "other", 1) }, identity},
		{"wrong repository", func(o *fakeOpts) { o.uri = strings.Replace(o.uri, "openai", "attacker", 1) }, identity},
		{"wrong tag", func(o *fakeOpts) { o.uri += "1" }, identity},
		{"a branch ref", func(o *fakeOpts) { o.uri = strings.Replace(o.uri, "tags/rust-v0.9.1", "heads/main", 1) },
			identity},
		{"wrong new issuer", func(o *fakeOpts) { o.issuerV2 = "https://accounts.google.com" }, "issuer is"},
		{"wrong old issuer", func(o *fakeOpts) { o.issuerV1 = "https://accounts.google.com" }, "issuer is"},
		{"no issuer", func(o *fakeOpts) { o.issuerV1, o.issuerV2 = "", "" }, "no issuer"},
		{"bad SET", func(o *fakeOpts) { o.badSET = true }, "signed entry timestamp"},
		{"wrong log ID", func(o *fakeOpts) { o.logID = strings.Repeat("0", 64) }, "log ID"},
		{"expired at integratedTime", func(o *fakeOpts) { o.integrated = o.signed.Add(11 * time.Minute) },
			"expired"},
		{"before the certificate", func(o *fakeOpts) { o.integrated = o.signed.Add(-time.Minute) }, "expired"},
		{"other CA", func(o *fakeOpts) { o.ca = other }, "unknown authority"},
		{"Rekor body with another signature", func(o *fakeOpts) { o.bodySig = []byte("other") },
			"does not have the signature"},
		{"Rekor body with another certificate", func(o *fakeOpts) { o.bodyCert = []byte("other") },
			"does not have the signature"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, _ := f.bundle(t, s, c.edit)
			got, _, err := verifyBundle([]byte(b), "0.9.1")
			if c.want == "" {
				if err != nil || got != s {
					t.Fatalf("verifyBundle = %q, %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("verifyBundle error = %v, want %q", err, c.want)
			}
		})
	}
	b, _ := f.bundle(t, s, nil)
	for _, c := range []struct{ bundle, want string }{
		{`{`, "read the bundle"},
		{strings.Replace(b, `"base64Signature":"`, `"base64Signature":"!`, 1), "read the bundle signature"},
		{strings.Replace(b, `"cert":"`, `"cert":"!`, 1), "read the bundle certificate"},
	} {
		if _, _, err := verifyBundle([]byte(c.bundle), "0.9.1"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("verifyBundle error = %v, want %q", err, c.want)
		}
	}
}
