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

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

func TestClaimListenPorts(t *testing.T) {
	app := []corev1.ContainerPort{{Name: "a", ContainerPort: claimPortBase}, {Name: "b", ContainerPort: 8080},
		{Name: "c", ContainerPort: claimPortBase + 2}}
	if got := (AppClaimCheck{}).listenPorts(app, nil); len(got) != 3 || got[0].Name != "a" {
		t.Errorf("With no issuer, the ports are the ports of the app container: %v", got)
	}
	// A port of a different container of the pod is not a listen port.
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: pauseTestSide,
		Ports: []corev1.ContainerPort{{ContainerPort: claimPortBase + 1}}}},
		InitContainers: []corev1.Container{{Name: "init", Ports: []corev1.ContainerPort{{ContainerPort: claimPortBase + 3}}}}}
	got := AppClaimCheck{Issuer: "i"}.listenPorts(app, pod)
	want := []int32{claimPortBase + 4, claimPortBase + 5, claimPortBase + 6}
	for i, p := range got {
		if len(got) != 3 || p.ContainerPort != want[i] || p.Name != "" {
			t.Errorf("listen port %d: %v, want %d", i, p, want[i])
		}
	}
}

// A template with the name of the check container is not valid, and the Service uses the ports
// of the check container.
func TestClaimAdd(t *testing.T) {
	c := AppClaimCheck{Issuer: "i"}
	app := []corev1.ContainerPort{{Name: jobIngHTTPPortName, ContainerPort: 8080}}
	for name, spec := range map[string]corev1.PodSpec{
		"container":      {Containers: []corev1.Container{{Name: claimContainer}}},
		"init container": {InitContainers: []corev1.Container{{Name: claimContainer}}},
		"volume":         {Volumes: []corev1.Volume{{Name: claimContainer}}},
	} {
		if err := c.add(&corev1.PodTemplateSpec{Spec: spec}, &corev1alpha1.AppVersion{}, app); !isInvalidTemplate(err) {
			t.Errorf("%s: the template with the reserved name is accepted: %v", name, err)
		}
	}
	tmpl := &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: pauseTestSide,
		Ports: []corev1.ContainerPort{{ContainerPort: claimPortBase}}}}}}
	if err := c.add(tmpl, &corev1alpha1.AppVersion{}, app); err != nil {
		t.Fatal(err)
	}
	if got := claimPorts(&tmpl.Spec, app); len(got) != 1 || got[0].ContainerPort != claimPortBase+1 {
		t.Errorf("The ports of the check container are %v.", got)
	}
	if got := claimPorts(&corev1.PodSpec{}, app); len(got) != 1 || got[0].Name != jobIngHTTPPortName {
		t.Errorf("With no check container, the ports are %v.", got)
	}
}
