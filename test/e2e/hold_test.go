//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authnv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// The Hold tests stop a workload during a live run. The admission policy permits a Hold only with
// the name of the requester in spec.user, so the test reads that name from the API server.
var _ = Describe("Holds", Ordered, Label("hold"), func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	AfterEach(dumpOnFailure)

	It("stops an Agent run: it blocks the key and leaves no access", func() {
		a := sampleAgent("held", callsScript(1, 600))
		r := start(a)

		By("holding the Agent during the run")
		end := time.Now()
		h := makeHold("e2e-agent-stop", corev1beta1.HoldTarget{Kind: corev1beta1.HoldTargetAgent,
			Namespace: runNS, Name: a.Name})
		expectNoAccess(a, r, end)

		By("checking the phase, the Revoked condition and the key")
		Eventually(func(g Gomega) {
			g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
			g.Expect(a.Status.Phase).To(Equal(corev1beta1.AgentFailed))
			c := meta.FindStatusCondition(a.Status.Conditions, "Revoked")
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(c.Reason).To(Equal("Stopped"))
		}, time.Minute).Should(Succeed(), "The Agent did not end as Stopped.")
		Expect(keyInfo(a.Status.KeyHash).Blocked).To(BeTrue(), "The gateway did not block the run key.")
		expectHeld(h)
		expectStopEvent(runNS, a.Name, h.Name)

		By("releasing the Hold and checking that the run stays ended")
		releaseHold(h)
		Consistently(func() corev1beta1.AgentPhase {
			_ = k8s.Get(ctx, client.ObjectKeyFromObject(a), a)
			return a.Status.Phase
		}, 10*time.Second, time.Second).Should(Equal(corev1beta1.AgentFailed))
	})
})

// holdUser returns the user name of the test client, as the API server sees it.
func holdUser() string {
	GinkgoHelper()
	r, err := clientset.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{},
		metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(r.Status.UserInfo.Username).NotTo(BeEmpty())
	return r.Status.UserInfo.Username
}

// makeHold makes a Hold with the action Stop.
func makeHold(name string, t corev1beta1.HoldTarget) *corev1beta1.Hold {
	GinkgoHelper()
	return makeHoldFor(name, corev1beta1.HoldStop, t)
}

// makeHoldFor makes a Hold with the action. The cleanup releases the Hold and then deletes it,
// because the admission policy refuses the delete of a Hold with no release.
func makeHoldFor(name string, action corev1beta1.HoldAction, t corev1beta1.HoldTarget) *corev1beta1.Hold {
	GinkgoHelper()
	h := &corev1beta1.Hold{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1beta1.HoldSpec{Target: t, Action: action,
			Reason: "The e2e test holds the workload.", User: holdUser()}}
	Expect(k8s.Create(ctx, h)).To(Succeed())
	DeferCleanup(func() {
		releaseHold(h)
		Expect(client.IgnoreNotFound(k8s.Delete(ctx, h))).To(Succeed())
	})
	return h
}

// releaseHold writes spec.release if the Hold has none.
func releaseHold(h *corev1beta1.Hold) {
	GinkgoHelper()
	err := k8s.Get(ctx, client.ObjectKeyFromObject(h), h)
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	if h.Spec.Release != nil {
		return
	}
	patch := client.MergeFrom(h.DeepCopy())
	h.Spec.Release = &corev1beta1.HoldRelease{User: holdUser(), Reason: "The e2e test is complete."}
	Expect(k8s.Patch(ctx, h, patch)).To(Succeed())
}

// expectHeld waits until the Held condition of the Hold is true with the reason Stopped.
func expectHeld(h *corev1beta1.Hold) {
	GinkgoHelper()
	expectHeldWith(h, "Stopped")
}

// expectHeldWith waits until the Held condition of the Hold is true with the reason.
func expectHeldWith(h *corev1beta1.Hold, reason string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(h), h)).To(Succeed())
		c := meta.FindStatusCondition(h.Status.Conditions, corev1beta1.HoldHeld)
		g.Expect(c).NotTo(BeNil())
		g.Expect(c.Status).To(Equal(metav1.ConditionTrue), c.Message)
		g.Expect(c.Reason).To(Equal(reason))
	}, 2*time.Minute).Should(Succeed(), "The Hold does not show the reason "+reason+".")
}

// expectQuarantinePolicy checks the admission policies of a quarantine on a quarantined Agent, its
// Job and its pod. The test client is a cluster admin. The policies have no exception for an
// admin, so each namespace user gets the same refusals. The budget of the quarantine refuses the
// eviction of the pod, as at a node drain.
func expectQuarantinePolicy(a *corev1beta1.Agent, pod string) {
	GinkgoHelper()
	const label = "koodam.dev/quarantine"
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: pod}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: "kd-" + a.Name}}
	all := []client.Object{a, job, p}
	Eventually(func(g Gomega) {
		for _, o := range all {
			g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(o), o)).To(Succeed())
			g.Expect(o.GetLabels()).To(HaveKeyWithValue(label, "true"), "%T has no quarantine label", o)
		}
	}, time.Minute).Should(Succeed())
	refused := func(what string, err error) {
		GinkgoHelper()
		Expect(apierrors.IsForbidden(err)).To(BeTrue(), "The API server did not refuse %s: %v", what, err)
	}
	labels := func(o client.Object, patch string) error {
		return k8s.Patch(ctx, o, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":`+patch+`}}`)))
	}
	refused("the Foreground delete of the Agent",
		k8s.Delete(ctx, a, client.PropagationPolicy(metav1.DeletePropagationForeground)))
	refused("the delete of the pod", k8s.Delete(ctx, p))
	refused("the delete of the Job", k8s.Delete(ctx, job))
	for _, o := range all {
		refused(fmt.Sprintf("the removal of the label of %T", o), labels(o, `{"`+label+`":null}`))
	}
	refused("a new value of the pod label "+agentLabel, labels(p, `{"`+agentLabel+`":"other"}`))
	refused("a deadline on the pod", k8s.Patch(ctx, p,
		client.RawPatch(types.StrategicMergePatchType, []byte(`{"spec":{"activeDeadlineSeconds":1}}`))))
	// The disruption controller writes the status of the budget after a short time. Before that,
	// the API server also refuses the eviction.
	Eventually(func(g Gomega) {
		err := clientset.CoreV1().Pods(a.Namespace).EvictV1(ctx, &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: pod}})
		g.Expect(apierrors.IsTooManyRequests(err)).To(BeTrue(), "The API server did not refuse the eviction: %v", err)
		pdb, err := clientset.PolicyV1().PodDisruptionBudgets(a.Namespace).Get(ctx,
			"kd-quarantine-agent-"+a.Name, metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pdb.Status.ObservedGeneration).To(Equal(pdb.Generation))
		g.Expect(pdb.Status.DisruptionsAllowed).To(BeZero())
	}, time.Minute).Should(Succeed())
	Expect(k8s.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
	Expect(p.DeletionTimestamp.IsZero()).To(BeTrue(), "The eviction deleted the pod.")
}

// expectStopEvent waits for the Warning event Stopped on the workload, with the Hold as the related
// object.
func expectStopEvent(namespace, name, hold string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		l, err := clientset.EventsV1().Events(namespace).List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		found := false
		for _, e := range l.Items {
			found = found || stopEvent(e, name, hold)
		}
		g.Expect(found).To(BeTrue())
	}, time.Minute).Should(Succeed(), fmt.Sprintf("No Stopped event of the Hold %s is on %s.", hold, name))
}

func stopEvent(e eventsv1.Event, name, hold string) bool {
	return e.Reason == "Stopped" && e.Type == "Warning" && e.Regarding.Name == name &&
		e.Related != nil && e.Related.Name == hold
}

// holdKey reads the gateway key of the Workspace and checks that it works.
func holdKey(ws *corev1beta1.Workspace) (key, hash string) {
	GinkgoHelper()
	Eventually(func() bool {
		_ = k8s.Get(ctx, client.ObjectKeyFromObject(ws), ws)
		return meta.IsStatusConditionTrue(ws.Status.Conditions, "GatewayKey")
	}, 2*time.Minute, time.Second).Should(BeTrue(), "The gateway key is not active.")
	s := &corev1.Secret{}
	Expect(k8s.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: ws.Name + "-gateway"}, s)).To(Succeed())
	key = string(s.Data[wsKeyField])
	Expect(key != "").To(BeTrue(), "The gateway Secret has no key.")
	code, _ := modelCall(ctx, key)
	Expect(code).To(Equal(http.StatusOK), "The Workspace key did not work.")
	return key, ws.Status.KeyHash
}
