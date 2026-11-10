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

package chart

import (
	"strings"
	"testing"
)

// The controller issues TestKeys only for the bundled gateway, which has 1 replica and 1 worker (#192).
func TestTestKeyFlagOnlyForBundledGateway(t *testing.T) {
	const flag = "--test-key-single-instance-gateway"
	for _, c := range []struct {
		name string
		set  []string
		want bool
	}{
		{"bundled", nil, true},
		{"existing", []string{"gateway.mode=existing", "gateway.url=http://gateway.example:4000"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := helmTemplate(t, "", c.set...)
			if err != nil {
				t.Fatalf("helm template failed: %v\n%s", err, out)
			}
			deploys := objects(t, out, "Deployment")
			got := false
			for _, d := range deploys {
				if strings.Contains(d, "name: koodam-controller-manager") || strings.Contains(d, "--gateway-url=") {
					got = got || strings.Contains(d, flag)
				}
			}
			if got != c.want {
				t.Fatalf("flag %s present = %v, want %v", flag, got, c.want)
			}
		})
	}
}
