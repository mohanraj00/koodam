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

package ingress

import (
	"testing"

	"github.com/mohanraj00/koodam/internal/agentingress"
)

// noPanic runs call and fails the test if call panics. A resolver with no reader must refuse, not
// panic; a panic fails only this case.
func noPanic(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if recover() != nil {
			t.Fatal("the resolver panicked")
		}
	}()
	call()
}

// TestTargetBoundaryUnconfigured proves that a resolver that did not come from newTargetResolver
// refuses each valid route with ErrTargetUnavailable and makes no read. A route that is not valid
// stays ErrRoute. TestTargetCheckPoliciesUnconfigured has the startup check.
func TestTargetBoundaryUnconfigured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T, *targetReader) *targetResolver
	}{
		{"nil resolver", func(*testing.T, *targetReader) *targetResolver { return nil }},
		{"zero value", func(*testing.T, *targetReader) *targetResolver { return &targetResolver{} }},
		{"nil reader", func(t *testing.T, f *targetReader) *targetResolver {
			return &targetResolver{guards: tgResolver(t, f).guards}
		}},
		{"nil guard map", func(_ *testing.T, f *targetReader) *targetResolver {
			return &targetResolver{reader: f}
		}},
		{"empty guard map", func(_ *testing.T, f *targetReader) *targetResolver {
			return &targetResolver{reader: f, guards: map[string]agentingress.PolicyGuard{}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTargetWorld(t.Context())
			r := tc.build(t, f)
			for _, namespace := range []string{tgNamespace, tgOther} {
				noPanic(t, func() {
					got, err := r.resolve(f.ctx, namespace, tgAgentName)
					refusedTarget(t, got, err, ErrTargetUnavailable)
				})
			}
			noPanic(t, func() {
				got, err := r.resolve(f.ctx, tgUpperNamespace, tgAgentName)
				refusedTarget(t, got, err, ErrRoute)
			})
			if len(f.ops) != 0 {
				t.Fatal("an unconfigured resolver read an object")
			}
		})
	}
}
