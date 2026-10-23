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

package podtemplate

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	main     = "workspace"
	login    = "koodam-login"
	home     = "home"
	data     = "data"
	custom   = "custom"
	homeDir  = "/home/koodam"
	second   = "spec.podTemplate.spec.containers[1].name"
	mainPath = "spec.podTemplate.spec.containers[0].volumeMounts[0].mountPath"
)

var root = field.NewPath("spec", "podTemplate")

// base returns a template like the one that the Workspace controller makes.
func base() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: new(false),
			EnableServiceLinks:           new(false),
			SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: new(true)},
			ServiceAccountName:           "ws",
			RestartPolicy:                corev1.RestartPolicyAlways,
			InitContainers:               []corev1.Container{{Name: login, Image: "init"}},
			Containers: []corev1.Container{{
				Name:         main,
				Image:        "koodam/workspace",
				Ports:        []corev1.ContainerPort{{Name: "ssh", ContainerPort: 2222}},
				Env:          []corev1.EnvVar{{Name: "HOME", Value: homeDir}},
				VolumeMounts: []corev1.VolumeMount{{Name: home, MountPath: homeDir}},
			}},
			Volumes: []corev1.Volume{{Name: home}, {Name: login}},
		},
	}
}

func withLabels(t corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	t.Labels = map[string]string{"koodam.dev/workspace": "w"}
	t.Annotations = map[string]string{"koodam.dev/hash": "1"}
	return t
}

func TestMergeNil(t *testing.T) {
	b := base()
	out, errs := Merge(b, nil, main, root)
	if len(errs) > 0 || out.Spec.Containers[0].Image != "koodam/workspace" {
		t.Fatalf("Merge(nil) = %v, %v", out.Spec.Containers, errs)
	}
}

func TestMergeKeepsKoodamFields(t *testing.T) {
	gpu := resource.MustParse("1")
	user := &User{
		Labels:      map[string]string{"team": "a"},
		Annotations: map[string]string{"note": "b"},
		Spec: corev1.PodSpec{
			RuntimeClassName: new("gvisor"),
			NodeSelector:     map[string]string{"pool": "agents"},
			InitContainers:   []corev1.Container{{Name: "fetch", Image: "busybox"}},
			Containers: []corev1.Container{
				{Name: "proxy", Image: "envoy"},
				{
					Name:         main,
					Image:        custom,
					Env:          []corev1.EnvVar{{Name: "TEAM", Value: "a"}},
					VolumeMounts: []corev1.VolumeMount{{Name: data, MountPath: "/data"}},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{"nvidia.com/gpu": gpu},
					},
				},
			},
			Volumes: []corev1.Volume{{Name: data}},
		},
	}
	out, errs := Merge(withLabels(base()), user, main, root)
	if len(errs) > 0 {
		t.Fatalf("Merge: %v", errs)
	}
	s := out.Spec
	if *s.RuntimeClassName != "gvisor" || s.NodeSelector["pool"] != "agents" {
		t.Errorf("user pod fields lost: %v %v", s.RuntimeClassName, s.NodeSelector)
	}
	if *s.AutomountServiceAccountToken || s.ServiceAccountName != "ws" || s.SecurityContext == nil ||
		s.RestartPolicy != corev1.RestartPolicyAlways {
		t.Errorf("Koodam pod fields lost: %+v", s)
	}
	if names := containerNames(s.InitContainers); names != "koodam-login,fetch" {
		t.Errorf("init containers = %s", names)
	}
	if names := containerNames(s.Containers); names != main+",proxy" {
		t.Errorf("containers = %s", names)
	}
	c := s.Containers[0]
	if c.Image != custom || len(c.Ports) != 1 || len(c.Env) != 2 || len(c.VolumeMounts) != 2 {
		t.Errorf("main container = %+v", c)
	}
	if q := c.Resources.Limits["nvidia.com/gpu"]; q.Cmp(gpu) != 0 {
		t.Errorf("resources = %v", c.Resources)
	}
	if len(s.Volumes) != 3 || s.Volumes[2].Name != data {
		t.Errorf("volumes = %v", s.Volumes)
	}
	if out.Labels["team"] != "a" || out.Labels["koodam.dev/workspace"] != "w" ||
		out.Annotations["note"] != "b" || out.Annotations["koodam.dev/hash"] != "1" {
		t.Errorf("metadata = %v %v", out.Labels, out.Annotations)
	}
}

func TestMergeDoesNotChangeBase(t *testing.T) {
	b := base()
	user := &User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: main, Env: []corev1.EnvVar{{Name: "A", Value: "1"}}}}}}
	if _, errs := Merge(b, user, main, root); len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(b.Spec.Containers[0].Env) != 1 {
		t.Errorf("Merge changed base: %v", b.Spec.Containers[0].Env)
	}
}

func TestMergeRefuses(t *testing.T) {
	tests := []struct {
		name string
		user User
		path string
	}{
		{"label", User{Labels: map[string]string{"koodam.dev/workspace": "x"}},
			"spec.podTemplate.metadata.labels[koodam.dev/workspace]"},
		{"annotation", User{Annotations: map[string]string{"koodam.dev/a": "x"}},
			"spec.podTemplate.metadata.annotations[koodam.dev/a]"},
		{"token", User{Spec: corev1.PodSpec{AutomountServiceAccountToken: new(true)}},
			"spec.podTemplate.spec.automountServiceAccountToken"},
		{"service links", User{Spec: corev1.PodSpec{EnableServiceLinks: new(true)}},
			"spec.podTemplate.spec.enableServiceLinks"},
		{"pod security", User{Spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{}}},
			"spec.podTemplate.spec.securityContext"},
		{"service account", User{Spec: corev1.PodSpec{ServiceAccountName: "admin"}},
			"spec.podTemplate.spec.serviceAccountName"},
		{"old service account", User{Spec: corev1.PodSpec{DeprecatedServiceAccount: "admin"}},
			"spec.podTemplate.spec.serviceAccountName"},
		{"restart", User{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever}},
			"spec.podTemplate.spec.restartPolicy"},
		{"deadline", User{Spec: corev1.PodSpec{ActiveDeadlineSeconds: new(int64(1))}},
			"spec.podTemplate.spec.activeDeadlineSeconds"},
		{"host network", User{Spec: corev1.PodSpec{HostNetwork: true}}, "spec.podTemplate.spec.hostNetwork"},
		{"host PID", User{Spec: corev1.PodSpec{HostPID: true}}, "spec.podTemplate.spec.hostPID"},
		{"host IPC", User{Spec: corev1.PodSpec{HostIPC: true}}, "spec.podTemplate.spec.hostIPC"},
		{"host users", User{Spec: corev1.PodSpec{HostUsers: new(true)}}, "spec.podTemplate.spec.hostUsers"},
		{"share process", User{Spec: corev1.PodSpec{ShareProcessNamespace: new(true)}},
			"spec.podTemplate.spec.shareProcessNamespace"},
		{"init name", User{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: login}}}},
			"spec.podTemplate.spec.initContainers[0].name"},
		{"init has main name", User{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: main}}}},
			"spec.podTemplate.spec.initContainers[0].name"},
		{"sidecar name", User{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: main}, {Name: login}}}},
			second},
		{"volume name", User{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: home}}}},
			"spec.podTemplate.spec.volumes[0].name"},
		{"main ports", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, Ports: []corev1.ContainerPort{{ContainerPort: 22}}}}}},
			"spec.podTemplate.spec.containers[0]"},
		{"main security", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, SecurityContext: &corev1.SecurityContext{Privileged: new(true)}}}}},
			"spec.podTemplate.spec.containers[0]"},
		{"main env", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, Env: []corev1.EnvVar{{Name: "HOME", Value: "/root"}}}}}},
			"spec.podTemplate.spec.containers[0].env[0].name"},
		{"main mount name", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, VolumeMounts: []corev1.VolumeMount{{Name: home, MountPath: "/x"}}}}}},
			"spec.podTemplate.spec.containers[0].volumeMounts[0].name"},
		{"sidecar mounts login", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "proxy", VolumeMounts: []corev1.VolumeMount{{Name: login, MountPath: "/x"}}}}}},
			"spec.podTemplate.spec.containers[0].volumeMounts[0].name"},
		{"init mounts login", User{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{
			Name: "fetch", VolumeMounts: []corev1.VolumeMount{{Name: login, MountPath: "/x"}}}}}},
			"spec.podTemplate.spec.initContainers[0].volumeMounts[0].name"},
		{"main mount path", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, VolumeMounts: []corev1.VolumeMount{{Name: data, MountPath: "/home/koodam/.ssh"}}}}}},
			mainPath},
		{"main mount double slash", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, VolumeMounts: []corev1.VolumeMount{{Name: data, MountPath: "/home//koodam"}}}}}},
			mainPath},
		{"main mount dot dot", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, VolumeMounts: []corev1.VolumeMount{{Name: data, MountPath: "/home/x/../koodam"}}}}}},
			mainPath},
		{"main mount dot", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, VolumeMounts: []corev1.VolumeMount{{Name: data, MountPath: "/home/./koodam/.ssh"}}}}}},
			mainPath},
		{"ephemeral", User{Spec: corev1.PodSpec{EphemeralContainers: []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}}}},
			"spec.podTemplate.spec.ephemeralContainers"},
		{"main twice", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: main}, {Name: main}}}},
			second},
		{"sidecar twice", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a"}, {Name: "a"}}}},
			second},
		{"init and sidecar", User{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "a"}}, Containers: []corev1.Container{{Name: "a"}}}},
			"spec.podTemplate.spec.containers[0].name"},
		{"main mount over path", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: main, VolumeMounts: []corev1.VolumeMount{{Name: data, MountPath: "/home/"}}}}}},
			mainPath},
		{"label subdomain", User{Labels: map[string]string{"core.koodam.dev/x": "y"}},
			"spec.podTemplate.metadata.labels[core.koodam.dev/x]"},
		{"sidecar securityContext", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "a", SecurityContext: &corev1.SecurityContext{Privileged: new(true)}}}}},
			"spec.podTemplate.spec.containers[0].securityContext"},
		{"init securityContext", User{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{
			Name: "a", SecurityContext: &corev1.SecurityContext{RunAsUser: new(int64(0))}}}}},
			"spec.podTemplate.spec.initContainers[0].securityContext"},
		{"host port", User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "a", Ports: []corev1.ContainerPort{{ContainerPort: 80, HostPort: 80}}}}}},
			"spec.podTemplate.spec.containers[0].ports[0].hostPort"},
		{"host path", User{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "root", VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}}}},
			"spec.podTemplate.spec.volumes[0].hostPath"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errs := Merge(base(), &tt.user, main, root)
			if len(errs) == 0 {
				t.Fatalf("Merge accepted %+v", tt.user)
			}
			if len(out.Spec.Containers) > 0 {
				t.Errorf("Merge returned a template with an error")
			}
			if !strings.HasPrefix(errs[0].Field, tt.path) {
				t.Errorf("error field = %q, want prefix %q (%v)", errs[0].Field, tt.path, errs)
			}
		})
	}
}

func TestMergeResources(t *testing.T) {
	b := base()
	b.Spec.Containers[0].Resources.Limits = corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceCPU: resource.MustParse("1")}
	user := &User{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: main, Resources: corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), "nvidia.com/gpu": resource.MustParse("1")},
	}}}}}
	out, errs := Merge(b, user, main, root)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	l := out.Spec.Containers[0].Resources.Limits
	if l.Memory().String() != "1Gi" || l.Cpu().String() != "2" || len(l) != 3 {
		t.Errorf("limits = %v", l)
	}
	if b.Spec.Containers[0].Resources.Limits.Cpu().String() != "1" {
		t.Errorf("Merge changed the base limits: %v", b.Spec.Containers[0].Resources.Limits)
	}
}

func TestMergeCopiesBase(t *testing.T) {
	b := base()
	for _, user := range []*User{nil, {Spec: corev1.PodSpec{NodeSelector: map[string]string{"a": "b"}}}} {
		out, errs := Merge(b, user, main, root)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		*out.Spec.AutomountServiceAccountToken = true
		out.Spec.SecurityContext.RunAsNonRoot = new(false)
		out.Spec.InitContainers[0].Image = "changed"
		out.Spec.Containers[0].Env[0].Value = "changed"
		if *b.Spec.AutomountServiceAccountToken || !*b.Spec.SecurityContext.RunAsNonRoot ||
			b.Spec.InitContainers[0].Image != "init" || b.Spec.Containers[0].Env[0].Value != homeDir {
			t.Errorf("a change to the output changed base (user %v)", user)
		}
	}
}

func TestValidateNil(t *testing.T) {
	if errs := Validate(base(), nil, main, root); errs != nil {
		t.Errorf("Validate(nil) = %v", errs)
	}
}

func TestValidateAcceptsFalseHostUsers(t *testing.T) {
	user := &User{Spec: corev1.PodSpec{HostUsers: new(false), ShareProcessNamespace: new(false)}}
	if errs := Validate(base(), user, main, root); len(errs) > 0 {
		t.Errorf("Validate: %v", errs)
	}
}

func TestValidateMountPathPrefix(t *testing.T) {
	user := &User{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: main, VolumeMounts: []corev1.VolumeMount{{Name: data, MountPath: "/home/koodam2"}}}}}}
	if errs := Validate(base(), user, main, root); len(errs) > 0 {
		t.Errorf("Validate refused a path next to a Koodam mount: %v", errs)
	}
}

func containerNames(cs []corev1.Container) string {
	n := make([]string, 0, len(cs))
	for _, c := range cs {
		n = append(n, c.Name)
	}
	return strings.Join(n, ",")
}

func TestValidateAcceptsOtherDomains(t *testing.T) {
	u := &User{Labels: map[string]string{"notkoodam.dev/x": "y", "koodam.dev": "z", "example.com/koodam.dev": "w"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Ports: []corev1.ContainerPort{{ContainerPort: 80}}}}}}
	if errs := Validate(base(), u, main, root); len(errs) > 0 {
		t.Errorf("Validate refused %v", errs)
	}
}
