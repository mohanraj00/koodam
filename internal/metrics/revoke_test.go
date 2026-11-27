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

package metrics

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mohanraj00/koodam/internal/gateway"
)

type blockGateway struct {
	gateway.Gateway
	err   error
	calls []string
}

func (g *blockGateway) BlockKey(_ context.Context, key string) error {
	g.calls = append(g.calls, key)
	return g.err
}

func (g *blockGateway) Usage(context.Context, string) (gateway.Usage, error) {
	return gateway.Usage{Spend: 2}, nil
}

func TestRevokeFailures(t *testing.T) {
	r := NewRevokeFailures()
	failing := &blockGateway{err: errors.New("gateway down")}
	agent := r.Gateway(failing, "Agent")
	ok := &blockGateway{}
	workspace := r.Gateway(ok, "Workspace")

	for range 2 {
		if err := agent.BlockKey(context.Background(), "hash-a"); !errors.Is(err, failing.err) {
			t.Fatalf("BlockKey error = %v, want %v", err, failing.err)
		}
	}
	if err := workspace.BlockKey(context.Background(), "hash-w"); err != nil {
		t.Fatalf("BlockKey error = %v, want nil", err)
	}
	if u, err := workspace.Usage(context.Background(), "hash-w"); err != nil || u.Spend != 2 {
		t.Fatalf("Usage = %v, %v, want the inner result", u, err)
	}
	if !slices.Equal(failing.calls, []string{"hash-a", "hash-a"}) || !slices.Equal(ok.calls, []string{"hash-w"}) {
		t.Fatalf("calls = %v and %v, want each call to reach the inner gateway", failing.calls, ok.calls)
	}

	// The full output has only the kind label, so no key hash is a label value.
	want := `# HELP koodam_revoke_failures_total Number of failed gateway key blocks in a revoke, by the kind of the key owner.
# TYPE koodam_revoke_failures_total counter
koodam_revoke_failures_total{kind="Agent"} 2
koodam_revoke_failures_total{kind="AppVersion"} 0
koodam_revoke_failures_total{kind="TestKey"} 0
koodam_revoke_failures_total{kind="Workspace"} 0
`
	if err := testutil.CollectAndCompare(r, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeFailuresUnknownKind(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Gateway did not panic for a kind that is not in RevokeKinds")
		}
	}()
	NewRevokeFailures().Gateway(&blockGateway{}, "Other")
}

var (
	descName = regexp.MustCompile(`fqName: "([^"]+)"`)
	docName  = regexp.MustCompile("(?m)^\\| `(koodam_[a-z_]+)` \\|")
)

// TestMetricsDoc checks that docs/metrics.md lists each metric of the controller, and no other
// metric.
func TestMetricsDoc(t *testing.T) {
	ch := make(chan *prometheus.Desc, 16)
	NewCollector(nil).Describe(ch)
	NewRevokeFailures().Describe(ch)
	close(ch)
	var code []string
	for d := range ch {
		m := descName.FindStringSubmatch(d.String())
		if m == nil {
			t.Fatalf("no name in %s", d)
		}
		code = append(code, m[1])
	}
	doc, err := os.ReadFile("../../docs/metrics.md")
	if err != nil {
		t.Fatal(err)
	}
	matches := docName.FindAllStringSubmatch(string(doc), -1)
	listed := make([]string, 0, len(matches))
	for _, m := range matches {
		listed = append(listed, m[1])
	}
	slices.Sort(code)
	slices.Sort(listed)
	if !slices.Equal(code, listed) {
		t.Fatalf("docs/metrics.md lists %v, the code has %v", listed, code)
	}
}
