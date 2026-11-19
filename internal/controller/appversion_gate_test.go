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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/evidence"
)

// The evidence gate of an AppVersion. envtest has no kubelet and no Job controller, so each test
// writes the status of the Deployment, the Job and the gate pod. The versions have no model, so
// they have no key.
var _ = Describe("AppVersion evidence gate", func() {
	const (
		ns        = "default"
		digest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		appImage  = "registry.example.com/app@" + digest
		resultSHA = "3333333333333333333333333333333333333333333333333333333333333333"
		gateID    = "gate-20261119-030000-0a1b2c3d"
		// healthPath is the path of the readiness probe of the fixture.
		healthPath = "/healthz"
	)
	var (
		ctx  = context.Background()
		rec  *AppVersionReconciler
		now  = time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
		name string
		n    int
	)
	nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
	run := func() {
		GinkgoHelper()
		_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
		Expect(err).NotTo(HaveOccurred())
	}
	version := func() *corev1alpha1.AppVersion {
		av := &corev1alpha1.AppVersion{}
		Expect(k8sClient.Get(ctx, nn(name), av)).To(Succeed())
		return av
	}
	expectEvidence := func(status metav1.ConditionStatus, reason string) *corev1alpha1.AppVersion {
		GinkgoHelper()
		av := version()
		c := meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionEvidence)
		Expect(c).NotTo(BeNil())
		Expect([]string{string(c.Status), c.Reason}).To(Equal([]string{string(status), reason}), c.Message)
		return av
	}
	newVersion := func(image string, gated bool) {
		app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: name + "-app", Namespace: ns}}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: app.Name},
				Template: corev1alpha1.AppVersionTemplate{PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: corev1alpha1.AppContainerName, Image: image,
						Ports: []corev1.ContainerPort{{Name: jobIngHTTPPortName, ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: healthPath, Port: intstr.FromString(jobIngHTTPPortName)}}}}}}}}}}
		if gated {
			av.Spec.Gate = &corev1alpha1.AppVersionGate{Nookku: corev1alpha1.NookkuGate{
				Image: gateTestImage, Root: gateTestRoot, GateFile: gateTestFile, DeadlineSeconds: new(int32(600))}}
		}
		Expect(k8sClient.Create(ctx, av)).To(Succeed())
		DeferCleanup(func() {
			if av := version(); controllerutil.RemoveFinalizer(av, appFinalizer) {
				Expect(k8sClient.Update(ctx, av)).To(Succeed())
			}
			Expect(k8sClient.Delete(ctx, av)).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, app))).To(Succeed())
		})
	}
	// podsReady writes the Deployment status of a version with each pod Ready.
	podsReady := func() {
		GinkgoHelper()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1}
		Expect(k8sClient.Status().Update(ctx, d)).To(Succeed())
		run()
	}
	// ready gives back the gate Job that the controller makes after each pod is Ready.
	getJob := func() *batchv1.Job {
		GinkgoHelper()
		job := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name+"-gate"), job)).To(Succeed())
		return job
	}
	ready := func() *batchv1.Job {
		GinkgoHelper()
		podsReady()
		return getJob()
	}
	// end writes the end of the Job to its status: Complete, or Failed.
	end := func(job *batchv1.Job, complete bool) {
		GinkgoHelper()
		t := metav1.NewTime(now)
		cond := func(kind batchv1.JobConditionType) batchv1.JobCondition {
			return batchv1.JobCondition{Type: kind, Status: corev1.ConditionTrue, LastTransitionTime: t}
		}
		job.Status = batchv1.JobStatus{StartTime: &t, Conditions: []batchv1.JobCondition{
			cond(batchv1.JobFailureTarget), cond(batchv1.JobFailed)}}
		if complete {
			job.Status = batchv1.JobStatus{StartTime: &t, CompletionTime: &t, Succeeded: 1,
				Conditions: []batchv1.JobCondition{cond(batchv1.JobSuccessCriteriaMet), cond(batchv1.JobComplete)}}
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
	}
	// pod makes a pod of the Job that succeeded, with the report as its termination message.
	pod := func(job *batchv1.Job, report string, change func(*corev1.PodSpec)) {
		GinkgoHelper()
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: job.Name + "-", Namespace: ns,
			Labels:          map[string]string{batchv1.JobNameLabel: job.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))}},
			Spec: *job.Spec.Template.Spec.DeepCopy()}
		if change != nil {
			change(&p.Spec)
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, p))).To(Succeed()) })
		p.Status = corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{
			Name: gateVerifyContainer, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0, Message: report}}}}}
		Expect(k8sClient.Status().Update(ctx, p)).To(Succeed())
	}
	report := func(exit int, verdict, subject string) string {
		return fmt.Sprintf(`{"v":1,"exit":%d,"gate":%q,"subject":%q,"verdict":%q,"sha256":%q,"format":1,"nookku":"0.5.9"}`,
			exit, gateID, subject, verdict, resultSHA)
	}
	subject := func() string {
		return evidence.AppVersionSubject(ns, name, version().UID, digest)
	}

	BeforeEach(func() {
		n++
		name = fmt.Sprintf("gated-%d", n)
		rec = &AppVersionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Now: func() time.Time { return now }}
	})

	It("gives a version with no gate no condition Evidence and no Job", func() {
		newVersion(appImage, false)
		run()
		podsReady()
		Expect(meta.FindStatusCondition(version().Status.Conditions, corev1alpha1.AppVersionEvidence)).To(BeNil())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+name+"-gate"), &batchv1.Job{}))).To(BeTrue())
	})

	It("refuses a gated version with an image that has no digest", func() {
		newVersion("registry.example.com/app:v1", true)
		run()
		expectEvidence(metav1.ConditionFalse, corev1alpha1.EvidenceImageNotPinned)
	})

	It("waits for the pods, then makes the gate Job with the subject of the version", func() {
		newVersion(appImage, true)
		run()
		expectEvidence(metav1.ConditionUnknown, corev1alpha1.EvidenceWaiting)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+name+"-gate"), &batchv1.Job{}))).To(BeTrue())

		job := ready()
		av := expectEvidence(metav1.ConditionUnknown, corev1alpha1.EvidenceRunning)
		Expect(metav1.IsControlledBy(job, av)).To(BeTrue())
		Expect(isGatePod(&job.Spec.Template.Spec, av, "kd-"+name+".default.svc:8080")).To(BeTrue())
		argv := job.Spec.Template.Spec.InitContainers[0].Command
		Expect(argv[len(argv)-1]).To(Equal(subject()))
	})

	It("deletes the gate Job in the revoke of a version, and does not say that the Job runs", func() {
		newVersion(appImage, true)
		run()
		ready()
		expectEvidence(metav1.ConditionUnknown, corev1alpha1.EvidenceRunning)

		app := &corev1alpha1.App{}
		Expect(k8sClient.Get(ctx, nn(name+"-app"), app)).To(Succeed())
		Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		run()
		av := expectEvidence(metav1.ConditionUnknown, corev1alpha1.EvidenceWaiting)
		Expect(meta.IsStatusConditionTrue(av.Status.Conditions, corev1alpha1.AppVersionRevoked)).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+name+"-gate"), &batchv1.Job{}))).To(BeTrue())
	})

	It("accepts a pass, writes the record one time, and keeps it against a later Job", func() {
		newVersion(appImage, true)
		run()
		job := ready()
		pod(job, report(0, "pass", subject()), nil)
		end(job, true)
		run()
		av := expectEvidence(metav1.ConditionTrue, corev1alpha1.EvidencePassed)
		Expect(av.Status.Evidence).To(Equal(&corev1alpha1.EvidenceRecord{
			Source: corev1alpha1.EvidenceSourceNookkuGate, Subject: subject(), Verdict: corev1alpha1.EvidencePass,
			ID: gateID, Digest: "sha256:" + resultSHA, Integrity: corev1alpha1.EvidenceSeal, NookkuVersion: "0.5.9",
			CheckedAt: metav1.NewTime(now.Local())}))

		// The first Job goes. A new Job with the same name and a fail report does not change the record.
		Expect(k8sClient.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		run()
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn(job.Name), &batchv1.Job{}))).To(BeTrue())
		later := gateJob(av, "kd-"+name+".default.svc:8080", subject())
		Expect(k8sClient.Create(ctx, later)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, later))).To(Succeed()) })
		pod(later, report(1, "fail", subject()), nil)
		end(later, true)
		run()
		Expect(expectEvidence(metav1.ConditionTrue, corev1alpha1.EvidencePassed).Status.Evidence).To(Equal(av.Status.Evidence))
	})

	It("opens the NetworkPolicy of the version to the gate pod only until the gate ends", func() {
		// With the pods of the egress proxy, the version has a default-deny NetworkPolicy.
		rec.Egress = testEgress
		rec.Egress.Namespace, rec.Egress.PodLabels = "egress-system", map[string]string{"gate-test/role": "egress"}
		newVersion(appImage, true)
		run()
		ports := []corev1.ContainerPort{{Name: jobIngHTTPPortName, ContainerPort: 8080}}
		policy := func(s string) *networkingv1.NetworkPolicy {
			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, nn(s), np)).To(Succeed())
			return np
		}
		Expect(policy("kd-" + name).Spec.Ingress).To(Equal([]networkingv1.NetworkPolicyIngressRule{gateIngress(version(), ports)}))
		// The policy of the gate pod does not exist before the Job.
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+name+"-gate"), &networkingv1.NetworkPolicy{}))).To(BeTrue())

		job := ready()
		av := version()
		gate := policy("kd-" + name + "-gate")
		Expect(metav1.IsControlledBy(gate, av)).To(BeTrue())
		Expect(gate.Spec).To(Equal(gatePolicy(av, ports).Spec))
		pod(job, report(0, "pass", subject()), nil)
		end(job, true)
		run()
		expectEvidence(metav1.ConditionTrue, corev1alpha1.EvidencePassed)
		run()
		Expect(policy("kd-" + name).Spec.Ingress).To(BeEmpty())
	})

	It("waits if a NetworkPolicy with the name of the gate policy is not from the version", func() {
		newVersion(appImage, true)
		other := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "kd-" + name + "-gate", Namespace: ns}}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, other)).To(Succeed()) })
		run()
		podsReady()
		expectEvidence(metav1.ConditionUnknown, corev1alpha1.EvidenceWaiting)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+name+"-gate"), &batchv1.Job{}))).To(BeTrue())
	})

	DescribeTable("gives each end state of the gate from the status of the Job and the pod",
		func(reason string, complete bool, message func() string, change func(*corev1.PodSpec)) {
			newVersion(appImage, true)
			run()
			job := ready()
			if message != nil {
				pod(job, message(), change)
			}
			end(job, complete)
			run()
			av := expectEvidence(metav1.ConditionFalse, reason)
			Expect(av.Status.Evidence != nil).To(Equal(reason == corev1alpha1.EvidenceFailed))
			// A False state is terminal: a later pass of the same Job does not change it.
			pod(job, report(0, "pass", subject()), nil)
			run()
			expectEvidence(metav1.ConditionFalse, reason)
		},
		Entry("a fail", corev1alpha1.EvidenceFailed, true, func() string { return report(1, "fail", subject()) }, nil),
		Entry("exit code 2", corev1alpha1.EvidenceNotVerified, true, func() string { return report(2, "", "") }, nil),
		Entry("a report that is not JSON", corev1alpha1.EvidenceNotVerified, true, func() string { return "Traceback" }, nil),
		Entry("a verdict that does not agree with the exit code", corev1alpha1.EvidenceNotVerified, true,
			func() string { return report(1, "pass", subject()) }, nil),
		Entry("a Job with no pod that succeeded", corev1alpha1.EvidenceNotVerified, true, nil, nil),
		Entry("a pod with a different verify image", corev1alpha1.EvidenceNotVerified, true,
			func() string { return report(0, "pass", subject()) },
			func(p *corev1.PodSpec) { p.Containers[0].Image = gateTestOtherImage }),
		Entry("2 pods with a report", corev1alpha1.EvidenceNotVerified, true,
			func() string { pod(getJob(), report(0, "pass", subject()), nil); return report(0, "pass", subject()) }, nil),
		Entry("a subject of a different object", corev1alpha1.EvidenceSubjectMismatch, true,
			func() string { return report(0, "pass", evidence.AppVersionSubject(ns, "other", "uid", digest)) }, nil),
		Entry("a Job that failed", corev1alpha1.EvidenceJobFailed, false, nil, nil),
	)
})
