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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mohanraj00/koodam/internal/opspage"
)

type check struct {
	t      *testing.T
	url    string
	bodies []string
}

// get sends one GET request and keeps the response headers and body for the key check.
func (c *check) get(path, token string, want int) []byte {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), http.MethodGet, c.url+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != want {
		c.t.Fatalf("GET %s gave %d, want %d", path, resp.StatusCode, want)
	}
	if path == "/" || !strings.HasPrefix(path, "/v1/") {
		if resp.Header.Get("Content-Security-Policy") != opspage.Policy {
			c.t.Fatalf("GET %s does not have the page policy", path)
		}
	}
	headers, _ := json.Marshal(resp.Header)
	c.bodies = append(c.bodies, string(headers), string(body))
	return body
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type runs struct {
	Items []struct{ Namespace, Name, Phase string } `json:"items"`
}

type groups struct {
	Items []struct {
		LabelValue string  `json:"labelValue"`
		Spend      float64 `json:"spend"`
	} `json:"items"`
}

func spendOf(g groups) map[string]float64 {
	m := map[string]float64{}
	for _, item := range g.Items {
		m[item.LabelValue] = item.Spend
	}
	return m
}

// TestExample starts the example, reads the page and the kit, reads the API with the example
// tokens, and checks that no response has a key value, a key hash or a token.
func TestExample(t *testing.T) {
	now := time.Now().UTC()
	ex, err := newExample(now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ex.close)
	server := httptest.NewServer(ex.handler)
	t.Cleanup(server.Close)
	c := &check{t: t, url: server.URL}

	if page := string(c.get("/", "", http.StatusOK)); !strings.Contains(page, `src="page.js"`) {
		t.Fatal("the page does not load page.js")
	}
	c.get("/page.js", "", http.StatusOK)
	c.get("/page.css", "", http.StatusOK)
	var pin struct{ Files map[string]string }
	if err := json.Unmarshal(opspage.Pin, &pin); err != nil || len(pin.Files) == 0 {
		t.Fatal("the kit pin is not valid")
	}
	for _, name := range slices.Sorted(maps.Keys(pin.Files)) {
		sum := sha256.Sum256(c.get("/kit/"+name, "", http.StatusOK))
		if hex.EncodeToString(sum[:]) != pin.Files[name] {
			t.Fatalf("the served kit file %s does not match the pin", name)
		}
	}

	c.get("/v1/runs", "", http.StatusUnauthorized)
	c.get("/v1/runs", "not-a-token", http.StatusUnauthorized)
	all := decode[runs](t, c.get("/v1/runs", adminToken, http.StatusOK))
	if len(all.Items) != 3 || all.Items[0].Name != "summarize-docs" || all.Items[2].Namespace != namespaceB {
		t.Fatalf("the admin runs are %+v", all.Items)
	}
	teamA := decode[runs](t, c.get("/v1/runs", teamAToken, http.StatusOK))
	if len(teamA.Items) != 2 || teamA.Items[0].Namespace != namespaceA || teamA.Items[1].Namespace != namespaceA {
		t.Fatalf("the team-a viewer sees %+v", teamA.Items)
	}

	period := "/v1/spend?from=" + now.Format(time.DateOnly) + "&to=" + now.AddDate(0, 0, 1).Format(time.DateOnly)
	byNamespace := spendOf(decode[groups](t, c.get(period+"&groupBy=namespace", adminToken, http.StatusOK)))
	if !maps.Equal(byNamespace, map[string]float64{namespaceA: 1.75, namespaceB: 0.75}) {
		t.Fatalf("the spend by namespace is %v", byNamespace)
	}
	byLabel := spendOf(decode[groups](t, c.get(period+"&groupBy=label:"+costGroup, adminToken, http.StatusOK)))
	if !maps.Equal(byLabel, map[string]float64{"research": 2, "support": 0.5}) {
		t.Fatalf("the spend by label is %v", byLabel)
	}
	limited := spendOf(decode[groups](t, c.get(period+"&groupBy=namespace", teamAToken, http.StatusOK)))
	if !maps.Equal(limited, map[string]float64{namespaceA: 1.75}) {
		t.Fatalf("the team-a viewer sees the spend %v", limited)
	}
	c.get("/v1/openapi.json", adminToken, http.StatusOK)

	secrets := append(slices.Clone(ex.keys), adminToken, teamAToken, masterKey)
	for _, body := range c.bodies {
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Fatal("a response has a key value, a key hash or a token")
			}
		}
	}
}
