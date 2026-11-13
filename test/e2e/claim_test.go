//go:build e2e
// +build e2e

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

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/gomega"
)

// The issuer of the App claim check in the suite. The test is the issuer: it makes a key at its
// start, gives the public part to the chart, and signs the tokens of its requests.
const claimIssuer, claimAudience = "https://issuer.e2e.koodam.test", "koodam-e2e-apps"

var (
	claimOnce sync.Once
	claimKey  *ecdsa.PrivateKey
	claimFile string
)

// claimValues gives back a Helm values file that sets the claim check of the Apps.
func claimValues() string {
	claimOnce.Do(func() {
		var err error
		claimKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		point, err := claimKey.PublicKey.Bytes()
		Expect(err).NotTo(HaveOccurred())
		enc := base64.RawURLEncoding.EncodeToString
		jwks := fmt.Sprintf(`{"keys":[{"kty":"EC","kid":"e2e","crv":"P-256","x":"%s","y":"%s"}]}`,
			enc(point[1:33]), enc(point[33:]))
		dir, err := os.MkdirTemp("", "koodam-e2e-claim")
		Expect(err).NotTo(HaveOccurred())
		claimFile = filepath.Join(dir, "values.yaml")
		values := fmt.Sprintf("apps:\n  claimCheck:\n    issuer: %s\n    audience: %s\n"+
			"    tenantClaim: tenant\n    jwks: '%s'\n", claimIssuer, claimAudience, jwks)
		Expect(os.WriteFile(claimFile, []byte(values), 0o600)).To(Succeed())
	})
	return claimFile
}

// claimToken signs a token of the test issuer for one hour.
func claimToken(caller string) string {
	claimValues()
	enc := base64.RawURLEncoding.EncodeToString
	input := enc([]byte(`{"alg":"ES256","kid":"e2e"}`)) + "." + enc(fmt.Appendf(nil,
		`{"iss":%q,"aud":%q,"sub":%q,"tenant":"team-e2e","exp":%d}`,
		claimIssuer, claimAudience, caller, time.Now().Add(time.Hour).Unix()))
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, claimKey, sum[:])
	Expect(err).NotTo(HaveOccurred())
	return input + "." + enc(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))
}
