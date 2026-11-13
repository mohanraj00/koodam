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

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohanraj00/koodam/internal/controller"
)

// TestWorkspaceImageFor checks the tag rule of the default workspace image.
func TestWorkspaceImageFor(t *testing.T) {
	const repo, latest = "ghcr.io/mohanraj00/koodam-workspace:", "latest"
	for v, tag := range map[string]string{
		"v0.2.0":                               "0.2.0",
		"v0.2.0-rc.1":                          "0.2.0-rc.1",
		"v0.2.0+dirty":                         latest,
		"v0.0.0-20261021000000-abcdef123456":   latest,
		"v0.2.1-0.20261021000000-abcdef123456": latest,
		"(devel)":                              latest,
		"":                                     latest,
		"0.2.0":                                latest,
	} {
		if got := workspaceImageFor(v); got != repo+tag {
			t.Errorf("workspaceImageFor(%q) = %q, want %q", v, got, repo+tag)
		}
	}
}

// TestCheckEgress checks the egress proxy flags.
func TestCheckEgress(t *testing.T) {
	ok := controller.EgressProxy{IP: "10.96.0.53", Port: 3128, ClusterDomain: "cluster.local"}
	bad := map[string]func(*controller.EgressProxy){
		"an IPv6 IP":   func(e *controller.EgressProxy) { e.IP = "fd00::53" },
		"a name":       func(e *controller.EgressProxy) { e.IP = "proxy.koodam" },
		"port 0":       func(e *controller.EgressProxy) { e.Port = 0 },
		"a bad domain": func(e *controller.EgressProxy) { e.ClusterDomain = "Cluster Local" },
		"no domain":    func(e *controller.EgressProxy) { e.ClusterDomain = "" },
	}
	if err := checkEgress(ok); err != nil {
		t.Fatal(err)
	}
	if err := checkEgress(controller.EgressProxy{}); err != nil {
		t.Fatalf("no proxy: %v", err)
	}
	for name, change := range bad {
		e := ok
		change(&e)
		if checkEgress(e) == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLoadClaim(t *testing.T) {
	dir := t.TempDir()
	bad, good := filepath.Join(dir, "bad.json"), filepath.Join(dir, "good.json")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	point, _ := key.PublicKey.Bytes()
	enc := base64.RawURLEncoding.EncodeToString
	jwks := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-256","x":%q,"y":%q}]}`, enc(point[1:33]), enc(point[33:]))
	if os.WriteFile(bad, []byte(`{"keys":[]}`), 0o600) != nil || os.WriteFile(good, []byte(jwks), 0o600) != nil {
		t.Fatal("The test files are not writable.")
	}
	full := controller.AppClaimCheck{Issuer: "https://issuer.example.com", Audience: "shop", CallerClaim: "sub",
		Image: "img"}
	off, err := loadClaim(controller.AppClaimCheck{Audience: "shop"}, "")
	if c := off; err != nil || c != (controller.AppClaimCheck{}) {
		t.Errorf("With no issuer, the check is off: %v, %v", c, err)
	}
	if c, err := loadClaim(full, good); err != nil || c.JWKS != jwks {
		t.Errorf("valid configuration: %v", err)
	}
	noAudience, noImage := full, full
	noAudience.Audience, noImage.Image = "", ""
	for name, err := range map[string]error{
		"no audience": second(loadClaim(noAudience, good)), "no image": second(loadClaim(noImage, good)),
		"no file": second(loadClaim(full, "")), "no key": second(loadClaim(full, bad)),
	} {
		if err == nil {
			t.Errorf("%s: the configuration is accepted", name)
		}
	}
}

func second(_ controller.AppClaimCheck, err error) error { return err }
