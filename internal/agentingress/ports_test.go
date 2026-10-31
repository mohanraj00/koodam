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

package agentingress

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const web = "web"

// spec returns an Agent spec with the ingress port, one regular container and one init container.
func spec(port *int32, regular, initPort string) corev1alpha1.AgentSpec {
	return corev1alpha1.AgentSpec{IngressPort: port, PodTemplate: &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "agent", Ports: []corev1.ContainerPort{
			{Name: "metrics", ContainerPort: 9090}, {Name: regular, ContainerPort: 8080}}}},
		InitContainers: []corev1.Container{{Name: "setup", Ports: []corev1.ContainerPort{
			{Name: initPort, ContainerPort: 8081}}}},
	}}}
}

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec corev1alpha1.AgentSpec
		want error
	}{
		{"legacy agent with no template", corev1alpha1.AgentSpec{}, nil},
		{"legacy agent keeps the http names", spec(nil, ReservedPortName, ReservedPortName), nil},
		{"port 1", spec(new(int32(1)), web, web), nil},
		{"port 65535", spec(new(int32(65535)), web, web), nil},
		{"port with no template", corev1alpha1.AgentSpec{IngressPort: new(int32(8080))}, nil},
		{"port 0", spec(new(int32(0)), web, web), ErrPortRange},
		{"negative port", spec(new(int32(-1)), web, web), ErrPortRange},
		{"port 65536", spec(new(int32(65536)), web, web), ErrPortRange},
		{"main container http", spec(new(int32(8080)), ReservedPortName, web), ErrReservedPort},
		{"init container http", spec(new(int32(8080)), web, ReservedPortName), ErrReservedPort},
		{"other case is not the reserved name", spec(new(int32(8080)), "HTTP", "Http"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.spec.DeepCopy()
			if got := Check(tc.spec); got != tc.want {
				t.Fatalf("Check returned the wrong result: got error %t, want error %t", got != nil, tc.want != nil)
			}
			if !equality.Semantic.DeepEqual(*before, tc.spec) {
				t.Fatal("Check changed the spec")
			}
		})
	}
}

func TestCheckSidecar(t *testing.T) {
	s := spec(new(int32(8080)), web, web)
	s.PodTemplate.Spec.Containers = append(s.PodTemplate.Spec.Containers, corev1.Container{
		Name: "sidecar", Ports: []corev1.ContainerPort{{Name: ReservedPortName, ContainerPort: 8082}}})
	if Check(s) != ErrReservedPort {
		t.Fatal("Check permitted the name http on a sidecar container")
	}
}
