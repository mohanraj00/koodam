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
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

func TestAppUserContainerSecurityContext(t *testing.T) {
	for _, issuer := range []string{"", "https://issuer.example.com"} {
		t.Run("issuer="+issuer, func(t *testing.T) {
			av := &corev1beta1.AppVersion{Spec: corev1beta1.AppVersionSpec{
				Template: corev1beta1.AppVersionTemplate{PodTemplate: corev1beta1.PodTemplate{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: corev1beta1.AppContainerName, Image: "registry.example.com/app:test",
								Ports: []corev1.ContainerPort{{ContainerPort: 8080}}},
							{Name: pauseTestSide, Image: "registry.example.com/side:test"},
						},
						InitContainers: []corev1.Container{
							{Name: "init-user", Image: "registry.example.com/init:test"},
							{Name: "init-side", Image: "registry.example.com/side:test",
								RestartPolicy: new(corev1.ContainerRestartPolicyAlways)},
						},
					},
				}},
			}}
			before := av.DeepCopy()
			r := &AppVersionReconciler{Claim: AppClaimCheck{Issuer: issuer, Image: "registry.example.com/koodam:test"}}
			d, err := r.deployment(context.Background(), av, "")
			if err != nil {
				t.Fatal(err)
			}
			pod := d.Spec.Template.Spec
			init := pod.InitContainers
			if issuer != "" {
				if len(init) != 3 || init[0].Name != claimContainer {
					t.Fatal("The claim check must be the first init container.")
				}
				if !reflect.DeepEqual(init[0].SecurityContext, helperSecurityContext()) {
					t.Error("The claim check must keep the helper security context.")
				}
				init = init[1:]
			}
			if len(pod.Containers) != 2 || len(init) != 2 {
				t.Fatal("The pod must keep each user container and init container.")
			}
			for _, containers := range [][]corev1.Container{pod.Containers, init} {
				for _, c := range containers {
					sc := c.SecurityContext
					if sc == nil || sc.Capabilities == nil {
						t.Errorf("Container %s must have a capability drop.", c.Name)
						continue
					}
					if !reflect.DeepEqual(sc.Capabilities.Drop, []corev1.Capability{capabilityAll}) || len(sc.Capabilities.Add) != 0 {
						t.Errorf("Container %s must drop all capabilities and add none.", c.Name)
					}
					if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
						t.Errorf("Container %s must not permit privilege escalation.", c.Name)
					}
				}
			}
			if !reflect.DeepEqual(av, before) {
				t.Error("The pod builder must not change the user template.")
			}
		})
	}
}

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
		if err := c.add(&corev1.PodTemplateSpec{Spec: spec}, &corev1beta1.AppVersion{}, app); !isInvalidTemplate(err) {
			t.Errorf("%s: the template with the reserved name is accepted: %v", name, err)
		}
	}
	tmpl := &corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: pauseTestSide,
			Ports: []corev1.ContainerPort{{ContainerPort: claimPortBase}}}},
		InitContainers: []corev1.Container{{Name: "init"}}}}
	if err := c.add(tmpl, &corev1beta1.AppVersion{}, app); err != nil {
		t.Fatal(err)
	}
	if got := c.claimPorts(&tmpl.Spec, app); len(got) != 1 || got[0].ContainerPort != claimPortBase+1 {
		t.Errorf("The ports of the check container are %v.", got)
	}
	// The check container starts first and its probe waits for its last port. Then an init
	// container of the user cannot bind a port of the check before the check.
	first := tmpl.Spec.InitContainers[0]
	if first.Name != claimContainer || first.StartupProbe == nil ||
		first.StartupProbe.TCPSocket.Port.IntVal != claimPortBase+1 {
		t.Errorf("The first init container is %q with the startup probe %v.", first.Name, first.StartupProbe)
	}
	if got := c.claimPorts(&corev1.PodSpec{}, app); len(got) != 1 || got[0].Name != jobIngHTTPPortName {
		t.Errorf("With no check container, the ports are %v.", got)
	}
	// With the check off, an init container of the user with the reserved name has no effect.
	user := &corev1.PodSpec{InitContainers: []corev1.Container{{Name: claimContainer}}}
	if got := (AppClaimCheck{}).claimPorts(user, app); len(got) != 1 || got[0].Name != jobIngHTTPPortName {
		t.Errorf("With the check off, the ports are %v.", got)
	}
}
