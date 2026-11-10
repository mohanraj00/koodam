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

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
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

// The bundled gateway uses Recreate, so an upgrade never runs 2 gateway pods (#192).
func TestBundledGatewayRolloutHasNoOverlap(t *testing.T) {
	out, err := helmTemplate(t, "")
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	found := false
	for _, doc := range objects(t, out, "Deployment") {
		var d appsv1.Deployment
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatal(err)
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name != litellmContainer {
				continue
			}
			found = true
			if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
				t.Fatalf("the gateway replicas = %v, want 1", d.Spec.Replicas)
			}
			if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
				t.Fatalf("the gateway strategy = %q, want %q", d.Spec.Strategy.Type, appsv1.RecreateDeploymentStrategyType)
			}
		}
	}
	if !found {
		t.Fatal("the chart has no litellm container")
	}
}
