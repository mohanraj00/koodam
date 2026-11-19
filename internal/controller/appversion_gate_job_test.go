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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/evidence"
)

// The fixture values of the gate tests.
const (
	gateTestImage = "registry.example.com/shop-gate@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	// gateTestOtherImage is an image that is not the gate image.
	gateTestOtherImage = "registry.example.com/other:v1"
	gateTestRoot       = "/project"
	gateTestFile       = "gates/release.json"
)

// The spec of the gate Job. These tests use no API server.
var _ = Describe("AppVersion gate Job", func() {
	const (
		subject = "koodam.dev/appversion/v1:store/v2:uid:sha256:1111"
		target  = "kd-v2.store.svc:8080"
	)
	newVersion := func() *corev1alpha1.AppVersion {
		return &corev1alpha1.AppVersion{
			ObjectMeta: metav1.ObjectMeta{Name: "v2", Namespace: "store", UID: "uid"},
			Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: "store-app"},
				Gate: &corev1alpha1.AppVersionGate{Nookku: corev1alpha1.NookkuGate{
					Image: gateTestImage, Root: gateTestRoot, GateFile: gateTestFile, DeadlineSeconds: new(int32(600))}}}}
	}

	It("gives the address of the Service of the version, with the first port of the app container", func() {
		deploy := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "first"}, {Name: corev1alpha1.AppContainerName,
				Ports: []corev1.ContainerPort{{ContainerPort: 8080}, {ContainerPort: 9090}}}}}}}}
		Expect(gateTarget(newVersion(), deploy)).To(Equal(target))
	})

	It("keeps the Job name in 63 characters for each valid version name", func() {
		av := newVersion()
		Expect(gateName(av)).To(Equal("kd-v2-gate"))
		long := strings.Repeat("a", 59)
		av.Name = long + "b"
		first := gateName(av)
		Expect(first).To(HaveLen(validation.DNS1123LabelMaxLength))
		Expect(validation.IsDNS1123Label(first)).To(BeEmpty())
		Expect(first).To(HaveSuffix("-gate"))
		av.Name = long + "c"
		Expect(gateName(av)).NotTo(Equal(first), "two long names with the same start get different Job names")
		av.Name = strings.Repeat("a", 55)
		Expect(gateName(av)).To(Equal("kd-" + av.Name + "-gate"))
	})

	It("makes a Job with no token, no Secret, a fixed argv and read-only mounts in the verify container", func() {
		av := newVersion()
		job := gateJob(av, target, subject)
		Expect(job.Name).To(Equal("kd-v2-gate"))
		Expect(metav1.IsControlledBy(job, av)).To(BeTrue())
		Expect(job.Spec.BackoffLimit).To(HaveValue(Equal(int32(gateBackoffLimit))))
		Expect(job.Spec.ActiveDeadlineSeconds).To(HaveValue(Equal(int64(600))))
		// The pod does not have the labels of the version, so it is not behind its Service.
		Expect(job.Spec.Template.Labels).To(Equal(map[string]string{gateLabel: "v2"}))

		spec := job.Spec.Template.Spec
		Expect(spec.AutomountServiceAccountToken).To(HaveValue(BeFalse()))
		Expect(spec.ServiceAccountName).To(BeEmpty())
		Expect(spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
		Expect(spec.Volumes).To(HaveLen(2))
		for _, v := range spec.Volumes {
			Expect(v.VolumeSource).To(Equal(corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}), v.Name)
		}
		Expect(spec.InitContainers).To(HaveLen(1))
		Expect(spec.Containers).To(HaveLen(1))
		gate, verify := spec.InitContainers[0], spec.Containers[0]
		Expect(gate.Command).To(Equal([]string{"python3", "-c", evidence.RunScript, gateTestRoot,
			gateWorkDir + "/project", gateTestFile, subject}))
		Expect(gate.Env).To(Equal([]corev1.EnvVar{{Name: gateHomeEnv, Value: gateHomeDir}, {Name: gateTargetEnv, Value: target}}))
		Expect(verify.Command).To(Equal([]string{"python3", "-c", evidence.VerifyScript, gateWorkDir + "/project", gateReportPath}))
		Expect(verify.Env).To(Equal([]corev1.EnvVar{{Name: gateHomeEnv, Value: gateHomeDir}}))
		for _, c := range []corev1.Container{gate, verify} {
			Expect(c.Image).To(Equal(gateTestImage))
			Expect(c.Args).To(BeEmpty())
			Expect(c.EnvFrom).To(BeEmpty())
			Expect(c.SecurityContext.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
			Expect(c.SecurityContext.Capabilities.Drop).To(Equal([]corev1.Capability{capabilityAll}))
			Expect(c.VolumeMounts).To(HaveLen(2))
			for _, m := range c.VolumeMounts {
				Expect(m.ReadOnly).To(Equal(c.Name == gateVerifyContainer), c.Name+" "+m.Name)
			}
		}
	})

	It("lets the gate pod reach only the pods of the version and the cluster DNS", func() {
		av := newVersion()
		np := gatePolicy(av, []corev1.ContainerPort{{ContainerPort: 15080}, {ContainerPort: 15081}})
		Expect(np.Name).To(Equal(gatePolicyName(av)))
		Expect(metav1.IsControlledBy(np, av)).To(BeTrue())
		Expect(np.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{gateLabel: "v2"}))
		Expect(np.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
		Expect(np.Spec.Ingress).To(BeEmpty())
		Expect(np.Spec.Egress).To(HaveLen(2))
		toVersion, toDNS := np.Spec.Egress[0], np.Spec.Egress[1]
		Expect(toVersion.To).To(Equal([]networkingv1.NetworkPolicyPeer{{
			PodSelector: &metav1.LabelSelector{MatchLabels: appVersionLabels(av)}}}))
		Expect(toVersion.Ports).To(HaveLen(2))
		Expect(toVersion.Ports[1].Port.IntValue()).To(Equal(15081))
		Expect(toDNS.To).To(Equal([]networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{corev1.LabelMetadataName: "kube-system"}}}}))
		for _, p := range toDNS.Ports {
			Expect(p.Port.IntValue()).To(Equal(53))
		}
		// The rule of the version policy names only the gate pod of this version, on the same ports.
		in := gateIngress(av, []corev1.ContainerPort{{ContainerPort: 15080}})
		Expect(in.From).To(Equal([]networkingv1.NetworkPolicyPeer{{
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{gateLabel: "v2"}}}}))
		Expect(in.Ports).To(HaveLen(1))
		Expect(in.Ports[0].Port.IntValue()).To(Equal(15080))
	})

	It("gives the gate policy a name that is not the name of the policy of a version", func() {
		av, other := newVersion(), newVersion()
		other.Name = av.Name + "-gate"
		name := gatePolicy(av, nil).Name
		Expect(name).To(Equal("kd-v2.gate"))
		Expect(name).NotTo(Equal(appVersionName(other)))
		Expect(gateName(av)).To(Equal(appVersionName(other)), "the Job name is not sufficient for the policy")
		av.Name = strings.Repeat("a", 60)
		Expect(validation.IsDNS1123Subdomain(gatePolicy(av, nil).Name)).To(BeEmpty())
	})

	It("gives the weights only to a version with no gate or with an accepted pass", func() {
		av := newVersion()
		setEvidence := func(status metav1.ConditionStatus, verdict corev1alpha1.EvidenceVerdict) {
			av.Status.Conditions = []metav1.Condition{{Type: corev1alpha1.AppVersionEvidence, Status: status}}
			av.Status.Evidence = &corev1alpha1.EvidenceRecord{Verdict: verdict}
		}
		Expect([]bool{gatePassed(av), gateOpen(av)}).To(Equal([]bool{false, true}), "no condition")
		av.Status.Conditions = []metav1.Condition{{Type: corev1alpha1.AppVersionEvidence, Status: metav1.ConditionTrue}}
		Expect([]bool{gatePassed(av), gateOpen(av)}).To(Equal([]bool{false, true}), "a True condition with no record")
		av.Status.Conditions[0].Status = metav1.ConditionFalse
		Expect([]bool{gatePassed(av), gateOpen(av)}).To(Equal([]bool{false, false}), "a False condition")
		setEvidence(metav1.ConditionFalse, corev1alpha1.EvidenceFail)
		Expect([]bool{gatePassed(av), gateOpen(av)}).To(Equal([]bool{false, false}), "a fail record")
		setEvidence(metav1.ConditionUnknown, corev1alpha1.EvidencePass)
		Expect([]bool{gatePassed(av), gateOpen(av)}).To(Equal([]bool{false, false}), "a pass record with no True condition")
		setEvidence(metav1.ConditionTrue, corev1alpha1.EvidencePass)
		Expect([]bool{gatePassed(av), gateOpen(av)}).To(Equal([]bool{true, false}), "an accepted pass")
		av.Spec.Gate, av.Status = nil, corev1alpha1.AppVersionStatus{}
		Expect([]bool{gatePassed(av), gateOpen(av)}).To(Equal([]bool{true, false}), "no gate")
	})

	It("uses the default deadline if the spec has none", func() {
		av := newVersion()
		av.Spec.Gate.Nookku.DeadlineSeconds = nil
		Expect(gateJob(av, target, subject).Spec.ActiveDeadlineSeconds).To(HaveValue(Equal(int64(gateDeadlineSeconds))))
	})

	It("knows the pod of the gate Job by its containers, and not by the subject in its argv", func() {
		av := newVersion()
		same := func(change func(*corev1.PodSpec)) bool {
			pod := gatePod(av, target, subject)
			change(&pod)
			return isGatePod(&pod, av, target)
		}
		Expect(same(func(*corev1.PodSpec) {})).To(BeTrue())
		Expect(same(func(p *corev1.PodSpec) { *p = gatePod(av, target, "other subject") })).To(BeTrue())
		for name, change := range map[string]func(*corev1.PodSpec){
			"a different verify image": func(p *corev1.PodSpec) { p.Containers[0].Image = gateTestOtherImage },
			"a different run image":    func(p *corev1.PodSpec) { p.InitContainers[0].Image = gateTestOtherImage },
			"a different verify argv":  func(p *corev1.PodSpec) { p.Containers[0].Command = []string{"true"} },
			"a different gate file":    func(p *corev1.PodSpec) { p.InitContainers[0].Command[5] = "gates/other.json" },
			"a different target":       func(p *corev1.PodSpec) { *p = gatePod(av, "kd-v1.store.svc:8080", subject) },
			"an env from a Secret":     func(p *corev1.PodSpec) { p.Containers[0].EnvFrom = make([]corev1.EnvFromSource, 1) },
			"args":                     func(p *corev1.PodSpec) { p.Containers[0].Args = []string{"x"} },
			"a second container":       func(p *corev1.PodSpec) { p.Containers = append(p.Containers, corev1.Container{Name: "x"}) },
			"no init container":        func(p *corev1.PodSpec) { p.InitContainers = nil },
			"an ephemeral container":   func(p *corev1.PodSpec) { p.EphemeralContainers = make([]corev1.EphemeralContainer, 1) },
		} {
			Expect(same(change)).To(BeFalse(), name)
		}
	})
})
