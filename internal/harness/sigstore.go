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
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// The Sigstore public-good trust anchors: the Fulcio root and intermediate certificates and the
// Rekor public key. Source: https://github.com/sigstore/root-signing/blob/main/targets/trusted_root.json,
// read on 2026-10-20. The Fulcio certificates are valid from 2022-04-13. The Rekor key is the key
// of https://rekor.sigstore.dev (log ID c0d23d6a...). The log2025-1 Rekor v2 log is not included,
// because the codex bundles use rekor.sigstore.dev.
const (
	fulcioRootPEM = `
-----BEGIN CERTIFICATE-----
MIIB9zCCAXygAwIBAgIUALZNAPFdxHPwjeDloDwyYChAO/4wCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MTEwMDcxMzU2NTlaFw0zMTEwMDUxMzU2NThaMCoxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjERMA8GA1UEAxMIc2lnc3RvcmUwdjAQBgcqhkjOPQIBBgUrgQQAIgNiAAT7
XeFT4rb3PQGwS4IajtLk3/OlnpgangaBclYpsYBr5i+4ynB07ceb3LP0OIOZdxex
X69c5iVuyJRQ+Hz05yi+UF3uBWAlHpiS5sh0+H2GHE7SXrk1EC5m1Tr19L9gg92j
YzBhMA4GA1UdDwEB/wQEAwIBBjAPBgNVHRMBAf8EBTADAQH/MB0GA1UdDgQWBBRY
wB5fkUWlZql6zJChkyLQKsXF+jAfBgNVHSMEGDAWgBRYwB5fkUWlZql6zJChkyLQ
KsXF+jAKBggqhkjOPQQDAwNpADBmAjEAj1nHeXZp+13NWBNa+EDsDP8G1WWg1tCM
WP/WHPqpaVo0jhsweNFZgSs0eE7wYI4qAjEA2WB9ot98sIkoF3vZYdd3/VtWB5b9
TNMea7Ix/stJ5TfcLLeABLE4BNJOsQ4vnBHJ
-----END CERTIFICATE-----
`
	fulcioIntermediatePEM = `
-----BEGIN CERTIFICATE-----
MIICGjCCAaGgAwIBAgIUALnViVfnU0brJasmRkHrn/UnfaQwCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MjA0MTMyMDA2MTVaFw0zMTEwMDUxMzU2NThaMDcxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjEeMBwGA1UEAxMVc2lnc3RvcmUtaW50ZXJtZWRpYXRlMHYwEAYHKoZIzj0C
AQYFK4EEACIDYgAE8RVS/ysH+NOvuDZyPIZtilgUF9NlarYpAd9HP1vBBH1U5CV7
7LSS7s0ZiH4nE7Hv7ptS6LvvR/STk798LVgMzLlJ4HeIfF3tHSaexLcYpSASr1kS
0N/RgBJz/9jWCiXno3sweTAOBgNVHQ8BAf8EBAMCAQYwEwYDVR0lBAwwCgYIKwYB
BQUHAwMwEgYDVR0TAQH/BAgwBgEB/wIBADAdBgNVHQ4EFgQU39Ppz1YkEZb5qNjp
KFWixi4YZD8wHwYDVR0jBBgwFoAUWMAeX5FFpWapesyQoZMi0CrFxfowCgYIKoZI
zj0EAwMDZwAwZAIwPCsQK4DYiZYDPIaDi5HFKnfxXx6ASSVmERfsynYBiX2X6SJR
nZU84/9DZdnFvvxmAjBOt6QpBlc4J/0DxvkTCqpclvziL6BCCPnjdlIB3Pu3BxsP
mygUY7Ii2zbdCdliiow=
-----END CERTIFICATE-----
`
	rekorKeyPEM = `
-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE2G2Y+2tabdTV5BcGiBIx0a9fAFwr
kBbmLSGtks4L3qX6yYY0zufBnhC8Ur/iy55GhWP/9A/bY2LhC30M9+RYtw==
-----END PUBLIC KEY-----
`
)

// trust holds the parsed trust anchors. It is a variable for the tests.
var trust = newTrust([]byte(fulcioRootPEM), []byte(fulcioIntermediatePEM), []byte(rekorKeyPEM))

type trustAnchors struct {
	roots, intermediates *x509.CertPool
	rekor                *ecdsa.PublicKey
	rekorID              string
}

func newTrust(root, intermediate, rekor []byte) trustAnchors {
	t := trustAnchors{roots: x509.NewCertPool(), intermediates: x509.NewCertPool()}
	if !t.roots.AppendCertsFromPEM(root) || !t.intermediates.AppendCertsFromPEM(intermediate) {
		panic("harness: a Fulcio certificate is not valid")
	}
	blk, _ := pem.Decode(rekor)
	if blk == nil {
		panic("harness: the Rekor key is not PEM")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		panic(err)
	}
	t.rekor = k.(*ecdsa.PublicKey)
	id := sha256.Sum256(blk.Bytes)
	t.rekorID = hex.EncodeToString(id[:])
	return t
}

// The Fulcio certificate extensions with the OIDC issuer. The first is the old form with the raw
// string. The second is a DER UTF8String.
var (
	oidIssuerV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidIssuerV2 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
)

const githubIssuer = "https://token.actions.githubusercontent.com"

// rekorPayload is the Rekor entry in the bundle. The fields are in the order of the canonical
// JSON that the signed entry timestamp signs.
type rekorPayload struct {
	Body           string `json:"body"`
	IntegratedTime int64  `json:"integratedTime"`
	LogID          string `json:"logID"`
	LogIndex       *int64 `json:"logIndex"`
}

// verifyBundle verifies the cosign bundle of the codex release tag rust-v<version> and returns
// the sha256 of the binary and the Rekor entry. These checks must all pass:
//  1. The Rekor key signs the entry (the signed entry timestamp, SET).
//  2. The entry has the signature and the certificate of the bundle.
//  3. The certificate chains to the Fulcio root at the integratedTime of the entry.
//  4. The certificate identity is the openai/codex release workflow on the tag, with the GitHub
//     Actions issuer.
//  5. The certificate key signs the sha256 of the binary.
func verifyBundle(b []byte, version string) (string, rekorPayload, error) {
	var bundle struct {
		Base64Signature string `json:"base64Signature"`
		Cert            string `json:"cert"`
		RekorBundle     struct {
			SignedEntryTimestamp string       `json:"SignedEntryTimestamp"`
			Payload              rekorPayload `json:"Payload"`
		} `json:"rekorBundle"`
	}
	if err := json.Unmarshal(b, &bundle); err != nil {
		return "", rekorPayload{}, fmt.Errorf("read the bundle: %w", err)
	}
	p := bundle.RekorBundle.Payload
	if p.LogIndex == nil || *p.LogIndex < 0 {
		return "", p, errors.New("the bundle has no Rekor log index")
	}
	e, err := readEntry(p.Body)
	if err != nil {
		return "", p, fmt.Errorf("the bundle: %w", err)
	}
	if err := verifySET(p, bundle.RekorBundle.SignedEntryTimestamp); err != nil {
		return "", p, err
	}
	sig, err := base64.StdEncoding.DecodeString(bundle.Base64Signature)
	if err != nil {
		return "", p, fmt.Errorf("read the bundle signature: %w", err)
	}
	certPEM, err := base64.StdEncoding.DecodeString(bundle.Cert)
	if err != nil {
		return "", p, fmt.Errorf("read the bundle certificate: %w", err)
	}
	if !bytes.Equal(e.sig, sig) || !bytes.Equal(e.cert, certPEM) {
		return "", p, errors.New("the Rekor entry does not have the signature and the certificate of the bundle")
	}
	cert, err := verifyCert(certPEM, time.Unix(p.IntegratedTime, 0), version)
	if err != nil {
		return "", p, err
	}
	key, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", p, errors.New("the bundle certificate key is not an ECDSA key")
	}
	digest, _ := hex.DecodeString(e.sum)
	if !ecdsa.VerifyASN1(key, digest, sig) {
		return "", p, errors.New("the bundle signature is not valid for the sha256 of the binary")
	}
	return e.sum, p, nil
}

// verifySET verifies the signed entry timestamp of the Rekor log over the canonical JSON of p.
func verifySET(p rekorPayload, set string) error {
	if p.LogID != trust.rekorID {
		return fmt.Errorf("the Rekor log ID %q is not the ID of the Rekor key", p.LogID)
	}
	sig, err := base64.StdEncoding.DecodeString(set)
	if err != nil {
		return fmt.Errorf("read the signed entry timestamp: %w", err)
	}
	// The values are base64, hex and numbers, so json.Marshal gives the canonical JSON.
	msg, err := json.Marshal(p)
	if err != nil {
		return err
	}
	h := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(trust.rekor, h[:], sig) {
		return errors.New("the signed entry timestamp of the Rekor entry is not valid")
	}
	return nil
}

// verifyCert verifies the Fulcio certificate chain at the time t and the certificate identity.
func verifyCert(certPEM []byte, t time.Time, version string) (*x509.Certificate, error) {
	blk, _ := pem.Decode(certPEM)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New("the bundle certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("read the bundle certificate: %w", err)
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: trust.roots, Intermediates: trust.intermediates,
		CurrentTime: t, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}})
	if err != nil {
		return nil, fmt.Errorf("the bundle certificate at %s: %w", t.UTC().Format(time.RFC3339), err)
	}
	want := "https://github.com/openai/codex/.github/workflows/rust-release.yml@refs/tags/rust-v" + version
	if len(cert.URIs) != 1 || cert.URIs[0].String() != want {
		return nil, fmt.Errorf("the bundle certificate identity is %v, not %s", cert.URIs, want)
	}
	n := 0
	for _, x := range cert.Extensions {
		var issuer string
		switch {
		case x.Id.Equal(oidIssuerV1):
			issuer = string(x.Value)
		case x.Id.Equal(oidIssuerV2):
			if rest, err := asn1.UnmarshalWithParams(x.Value, &issuer, "utf8"); err != nil || len(rest) != 0 {
				return nil, errors.New("the bundle certificate issuer is not valid")
			}
		default:
			continue
		}
		if issuer != githubIssuer {
			return nil, fmt.Errorf("the bundle certificate issuer is %q, not %s", issuer, githubIssuer)
		}
		n++
	}
	if n == 0 {
		return nil, errors.New("the bundle certificate has no issuer")
	}
	return cert, nil
}

// rekorEntry is the part of a Rekor hashedrekord entry that verifyBundle uses.
type rekorEntry struct {
	sum       string
	sig, cert []byte
}

// readEntry reads a Rekor hashedrekord entry with a sha256. body is the entry in base64.
func readEntry(body string) (rekorEntry, error) {
	b, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return rekorEntry{}, fmt.Errorf("read the log entry: %w", err)
	}
	var e struct {
		Kind string `json:"kind"`
		Spec struct {
			Data struct {
				Hash struct {
					Algorithm string `json:"algorithm"`
					Value     string `json:"value"`
				} `json:"hash"`
			} `json:"data"`
			Signature struct {
				Content   []byte `json:"content"`
				PublicKey struct {
					Content []byte `json:"content"`
				} `json:"publicKey"`
			} `json:"signature"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return rekorEntry{}, fmt.Errorf("read the log entry: %w", err)
	}
	h := e.Spec.Data.Hash
	if e.Kind != "hashedrekord" || h.Algorithm != "sha256" || !sha256RE.MatchString(h.Value) {
		return rekorEntry{}, fmt.Errorf("the log entry is not a hashedrekord with a sha256 (kind %q, algorithm %q)",
			e.Kind, h.Algorithm)
	}
	return rekorEntry{sum: h.Value, sig: e.Spec.Signature.Content, cert: e.Spec.Signature.PublicKey.Content}, nil
}
