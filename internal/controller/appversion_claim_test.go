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

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestClaimListenPorts(t *testing.T) {
	app := []corev1.ContainerPort{{Name: "a", ContainerPort: claimPortBase}, {Name: "b", ContainerPort: 8080},
		{Name: "c", ContainerPort: claimPortBase + 2}}
	if got := (AppClaimCheck{}).listenPorts(app); len(got) != 3 || got[0].Name != "a" {
		t.Errorf("With no issuer, the ports are the ports of the app container: %v", got)
	}
	got := AppClaimCheck{Issuer: "i"}.listenPorts(app)
	want := []int32{claimPortBase + 1, claimPortBase + 3, claimPortBase + 4}
	for i, p := range got {
		if len(got) != 3 || p.ContainerPort != want[i] || p.Name != "" {
			t.Errorf("listen port %d: %v, want %d", i, p, want[i])
		}
	}
}
