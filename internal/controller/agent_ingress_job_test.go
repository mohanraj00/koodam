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

// These tests are for the ingress port of the run Job of #147. They call runJob and the real
// withPodTemplate merge. The merge reads Secret metadata only for a Secret reference, and these
// templates have no Secret reference, so the empty fake API gets no read. The tests use no cluster,
// no gateway, no key and no Secret. Each failure message is fixed text and shows no object.

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/harness"
)

const jobIngGatewayURL = "http://gw.test:4000"

// jobIngInstaller is the installer image that the tests give to runJob.
const jobIngInstaller = "installer"

// jobIngWebPort is the name of the user port in the fixture containers.
const jobIngWebPort = "web"

// jobIngHTTPPortName is the fixed expected name of the ingress port. It is not the production
// constant, so a change to the production name makes the tests fail.
const jobIngHTTPPortName = "http"

// jobIngAgent returns an Agent with the port, the harness and the template. It has no env map, so
// the order of the env in the Job is fixed.
func jobIngAgent(port *int32, h string, tmpl *corev1alpha1.PodTemplate) *corev1alpha1.Agent {
	a := &corev1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: testTeam, UID: "uid-job"},
		Spec: corev1alpha1.AgentSpec{Image: testAgentImage, Command: []string{"run"}, Model: "m",
			Budget: resource.MustParse("1"), DeadlineSeconds: 60, IngressPort: port, PodTemplate: tmpl},
	}
	if h != "" {
		a.Spec.Harness = &corev1alpha1.HarnessSpec{Name: h}
	}
	return a
}

// jobIngWant is the only port that the agent container must have for port p.
func jobIngWant(p int32) []corev1.ContainerPort {
	return []corev1.ContainerPort{{Name: jobIngHTTPPortName, ContainerPort: p, Protocol: corev1.ProtocolTCP}}
}

// jobIngNoPorts is true if no container in cs has a port.
func jobIngNoPorts(cs []corev1.Container) bool {
	for _, c := range cs {
		if len(c.Ports) > 0 {
			return false
		}
	}
	return true
}

// jobIngMerge merges the template of a into the run Job of a with the real merge.
func jobIngMerge(t *testing.T, a *corev1alpha1.Agent) (corev1.PodTemplateSpec, corev1.PodTemplateSpec, error) {
	t.Helper()
	base := runJob(a, jobIngInstaller, jobIngGatewayURL).Spec.Template
	out, err := withPodTemplate(t.Context(), fake.NewClientBuilder().Build(), a.Namespace, base,
		a.Spec.PodTemplate, agentContainer)
	return base, out, err
}

func TestIngressJobAbsentPort(t *testing.T) {
	for _, h := range []string{"", harness.ClaudeCode, harness.Codex} {
		s := runJob(jobIngAgent(nil, h, nil), jobIngInstaller, jobIngGatewayURL).Spec.Template.Spec
		if !jobIngNoPorts(s.Containers) || !jobIngNoPorts(s.InitContainers) {
			t.Fatal("a run Job with no ingress port has a container port")
		}
	}
}

func TestIngressJobPorts(t *testing.T) {
	for _, p := range []int32{1, 8080, 65535} {
		for _, h := range []string{"", harness.ClaudeCode, harness.Codex} {
			job := runJob(jobIngAgent(new(p), h, nil), jobIngInstaller, jobIngGatewayURL)
			s := &job.Spec.Template.Spec
			if len(s.Containers) != 1 || s.Containers[0].Name != agentContainer {
				t.Fatal("the run Job does not have only the agent container")
			}
			ports := s.Containers[0].Ports
			switch {
			case !equality.Semantic.DeepEqual(ports, jobIngWant(p)):
				t.Fatal("the agent container does not have only the http TCP port with the ingress port")
			case ports[0].HostPort != 0 || ports[0].HostIP != "":
				t.Fatal("the ingress port has a host field")
			case !jobIngNoPorts(s.InitContainers):
				t.Fatal("an init container has a port")
			case (h != "") != (len(s.InitContainers) == 1):
				t.Fatal("the harness init container is not as before")
			}
			// With no ingress port, the Job must be the same as before. This also keeps the harness
			// and the Codex config.
			s.Containers[0].Ports = nil
			if !equality.Semantic.DeepEqual(job, runJob(jobIngAgent(nil, h, nil), jobIngInstaller, jobIngGatewayURL)) {
				t.Fatal("the ingress port changed a different part of the run Job")
			}
		}
	}
}

func TestIngressJobMergeKeepsPort(t *testing.T) {
	side := corev1.Container{Name: "side", Image: "side-img",
		Ports: []corev1.ContainerPort{{Name: jobIngWebPort, ContainerPort: 9090, Protocol: corev1.ProtocolTCP}}}
	setup := corev1.Container{Name: "setup", Image: "setup-img",
		Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9091}}}
	main := corev1.Container{Name: agentContainer, Image: "user-img", Args: []string{"--flag"},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m")}}}
	tmpl := &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
		Containers: []corev1.Container{main, side}, InitContainers: []corev1.Container{setup}}}
	for _, h := range []string{"", harness.ClaudeCode} {
		a := jobIngAgent(new(int32(8080)), h, tmpl)
		before := a.DeepCopy()
		base, out, err := jobIngMerge(t, a)
		if err != nil {
			t.Fatal("the merge refused a permitted template")
		}
		s := out.Spec
		inits := s.InitContainers
		if h != "" {
			if len(inits) != 2 || len(inits[0].Ports) > 0 || inits[0].Name != base.Spec.InitContainers[0].Name {
				t.Fatal("the harness init container is not first, or it has a port")
			}
			inits = inits[1:]
		}
		c := s.Containers
		switch {
		case !equality.Semantic.DeepEqual(a, before):
			t.Fatal("the merge changed the Agent or its pod template")
		case !equality.Semantic.DeepEqual(base, runJob(before, jobIngInstaller, jobIngGatewayURL).Spec.Template):
			t.Fatal("the merge changed the base template")
		case !equality.Semantic.DeepEqual(base.Spec.Containers[0].Ports, jobIngWant(8080)):
			t.Fatal("the base agent container does not have only the ingress port")
		case len(c) != 2 || c[0].Name != agentContainer || c[1].Name != side.Name:
			t.Fatal("the merged pod does not have the agent container first and the sidecar")
		case !equality.Semantic.DeepEqual(c[0].Ports, jobIngWant(8080)):
			t.Fatal("the merge did not keep only the ingress port in the agent container")
		case c[0].Image != main.Image || !equality.Semantic.DeepEqual(c[0].Args, main.Args) ||
			!c[0].Resources.Limits.Cpu().Equal(resource.MustParse("500m")):
			t.Fatal("the merge did not use the permitted user fields of the agent container")
		case !equality.Semantic.DeepEqual(c[1].Ports, side.Ports):
			t.Fatal("the merge changed the sidecar ports")
		case len(inits) != 1 || !equality.Semantic.DeepEqual(inits[0].Ports, setup.Ports):
			t.Fatal("the merge changed the init container ports")
		}
	}
}

func TestIngressJobMergeRefusesMainPorts(t *testing.T) {
	user := corev1.Container{Name: agentContainer,
		Ports: []corev1.ContainerPort{{Name: jobIngWebPort, ContainerPort: 9090}}}
	tmpl := &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{user}}}
	for _, port := range []*int32{nil, new(int32(8080))} {
		a := jobIngAgent(port, "", tmpl)
		before := a.DeepCopy()
		base, out, err := jobIngMerge(t, a)
		switch {
		case err == nil || !isInvalidTemplate(err):
			t.Fatal("the merge did not refuse a user port in the agent container")
		case !equality.Semantic.DeepEqual(out, base):
			t.Fatal("the refused merge did not return the base template")
		case !equality.Semantic.DeepEqual(a, before):
			t.Fatal("the refused merge changed the Agent")
		}
	}
}
