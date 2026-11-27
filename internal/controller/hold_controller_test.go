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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// These specs call the reconcilers directly. They show that a Hold with the action Stop uses the
// revoke path of each kind: the key block comes before the pod stop.
var _ = Describe("Hold stop", func() {
	const stopReason = "The run sends data to an unknown host."
	var (
		ctx   context.Context
		admin string
		rec   *events.FakeRecorder
		n     int
	)
	holds := func() *HoldReconciler {
		return &HoldReconciler{Client: k8sClient, Recorder: rec, AppsEnabled: true, EgressProxy: true}
	}
	// newHold makes a Hold as the admin. The cleanup releases it and then deletes it, because the
	// admission policy refuses the delete of a Hold with no release.
	newHold := func(t corev1alpha1.HoldTarget, action corev1alpha1.HoldAction) string {
		n++
		name := fmt.Sprintf("stop-%d", n)
		Expect(k8sClient.Create(ctx, &corev1alpha1.Hold{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1alpha1.HoldSpec{Target: t, Action: action, Reason: stopReason, User: admin}})).To(Succeed())
		DeferCleanup(func() {
			h := &corev1alpha1.Hold{}
			if apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Name: name}, h)) {
				return
			}
			if h.Spec.Release == nil {
				h.Spec.Release = &corev1alpha1.HoldRelease{User: admin, Reason: "test cleanup"}
				Expect(k8sClient.Update(ctx, h)).To(Succeed())
			}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, h))).To(Succeed())
		})
		return name
	}
	release := func(name string) {
		h := &corev1alpha1.Hold{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, h)).To(Succeed())
		h.Spec.Release = &corev1alpha1.HoldRelease{User: admin, Reason: "The investigation is complete."}
		Expect(k8sClient.Update(ctx, h)).To(Succeed())
	}
	held := func(name string) *metav1.Condition {
		_, err := holds().Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		Expect(err).NotTo(HaveOccurred())
		h := &corev1alpha1.Hold{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, h)).To(Succeed())
		c := meta.FindStatusCondition(h.Status.Conditions, corev1alpha1.HoldHeld)
		Expect(c).NotTo(BeNil())
		return c
	}
	// stopEvent reads the next event. A Warning event with the reason Stopped has the Hold name,
	// its user and its reason.
	stopEvent := func(hold string) {
		GinkgoHelper()
		var e string
		Eventually(rec.Events).Should(Receive(&e))
		Expect(e).To(HavePrefix("Warning Stopped "))
		Expect(e).To(ContainSubstring(hold))
		Expect(e).To(ContainSubstring(admin))
		Expect(e).To(ContainSubstring(stopReason))
	}

	BeforeEach(func() {
		ctx = context.Background()
		rec = events.NewFakeRecorder(20)
		review := &authenticationv1.SelfSubjectReview{}
		Expect(k8sClient.Create(ctx, review)).To(Succeed())
		admin = review.Status.UserInfo.Username
	})

	Context("an Agent", func() {
		const ns = "default"
		var (
			gw   *fakeGateway
			name string
		)
		key := func() types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: name} }
		agents := func() *AgentReconciler {
			return &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
				Gateway: gw, GatewayURL: testGatewayURL, Grace: time.Minute, JobTTL: 3600, Recorder: rec}
		}
		reconcileAgent := func() {
			_, err := agents().Reconcile(ctx, reconcile.Request{NamespacedName: key()})
			Expect(err).NotTo(HaveOccurred())
		}
		agent := func() *corev1alpha1.Agent {
			a := &corev1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, key(), a)).To(Succeed())
			return a
		}
		newAgent := func() {
			Expect(k8sClient.Create(ctx, &corev1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: corev1alpha1.AgentSpec{Image: "example.com/hold:test", Command: []string{"/hold"},
					Model: testGatewayModel, Budget: resource.MustParse("1"), DeadlineSeconds: 600},
			})).To(Succeed())
			DeferCleanup(func() {
				a := &corev1alpha1.Agent{}
				if k8sClient.Get(ctx, key(), a) == nil && (controllerutil.RemoveFinalizer(a, finalizer) ||
					controllerutil.RemoveFinalizer(a, quarantineFinalizer)) {
					controllerutil.RemoveFinalizer(a, quarantineFinalizer)
					Expect(k8sClient.Update(ctx, a)).To(Succeed())
				}
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx,
					&corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}))).To(Succeed())
			})
		}
		expectStopped := func() {
			GinkgoHelper()
			a := agent()
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentFailed))
			c := meta.FindStatusCondition(a.Status.Conditions, condRevoked)
			Expect(c).NotTo(BeNil())
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal(reasonStopped))
			Expect(a.Finalizers).NotTo(ContainElement(finalizer))
		}

		BeforeEach(func() {
			n++
			name = fmt.Sprintf("held-run-%d", n)
			gw = &fakeGateway{runBinding: types.NamespacedName{Namespace: ns, Name: "kd-" + name}}
		})

		It("blocks the key before it deletes the Job, and the run does not start again after the release", func() {
			newAgent()
			reconcileAgent()
			Expect(agent().Status.Phase).To(Equal(corev1alpha1.AgentRunning))
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldStop)
			c := held(hold)
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(reasonStopping))

			reconcileAgent()
			expectStopped()
			Expect(gw.accessAtBlock).To(BeTrue(), "the key must be blocked before the access is deleted")
			Expect(gw.order).To(Equal([]string{"create", pauseTestBlock, pauseTestUsage}))
			Expect(gw.blocked).To(Equal([]string{gateway.HashKey(gw.created[0].Value)}))
			job := &batchv1.Job{}
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "kd-" + name}, job)
			Expect(apierrors.IsNotFound(err) || !job.DeletionTimestamp.IsZero()).To(BeTrue(), "the Job stays: %v", err)
			stopEvent(hold)

			c = held(hold)
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal(reasonStopped))
			Expect(c.Message).To(Equal("The 1 workloads of the target are stopped."))
			var e string
			Eventually(rec.Events).Should(Receive(&e))
			Expect(e).To(HavePrefix("Normal Stopped "))

			release(hold)
			reconcileAgent()
			expectStopped()
			Expect(gw.created).To(HaveLen(1), "a released Hold does not start the run again")
			c = held(hold)
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(reasonReleased))
			Expect(c.Message).To(ContainSubstring(admin))
		})

		It("stops a run that did not start, with no key", func() {
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldStop)
			c := held(hold)
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Message).To(Equal("No workload of the target exists."))
			var e string
			Eventually(rec.Events).Should(Receive(&e))
			Expect(e).To(HavePrefix("Normal Stopped "))

			newAgent()
			reconcileAgent()
			expectStopped()
			Expect(gw.created).To(BeEmpty())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "kd-" + name}, &batchv1.Job{})).
				NotTo(Succeed())
			stopEvent(hold)
		})

		It("does not stop the run for a released Hold", func() {
			released := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldStop)
			release(released)
			newAgent()
			reconcileAgent()
			Expect(agent().Status.Phase).To(Equal(corev1alpha1.AgentRunning))
			Expect(gw.created).To(HaveLen(1))
			Expect(rec.Events).To(BeEmpty())
		})

		job := func() *batchv1.Job {
			j := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "kd-" + name}, j)).To(Succeed())
			return j
		}
		// quarantineRun quarantines a run that started. The key is blocked, and the Job stays with no
		// deadline.
		quarantineRun := func() string {
			GinkgoHelper()
			newAgent()
			reconcileAgent()
			Expect(job().Spec.ActiveDeadlineSeconds).NotTo(BeNil())
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			c := held(hold)
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(reasonQuarantining))

			reconcileAgent()
			a := agent()
			Expect(a.Status.Phase).To(Equal(corev1alpha1.AgentRunning))
			Expect(a.Finalizers).To(ContainElements(finalizer, quarantineFinalizer))
			Expect(quarantined(a.Status.Conditions)).To(BeTrue())
			Expect(gw.blocked).To(Equal([]string{gateway.HashKey(gw.created[0].Value)}))
			j := job()
			Expect(j.DeletionTimestamp.IsZero()).To(BeTrue())
			Expect(j.Spec.ActiveDeadlineSeconds).To(BeNil())
			var e string
			Eventually(rec.Events).Should(Receive(&e))
			Expect(e).To(HavePrefix("Warning Quarantined "))
			Expect(e).To(ContainSubstring(hold))

			// A second reconcile keeps the quarantine and writes no second event.
			reconcileAgent()
			Expect(agent().Status.Phase).To(Equal(corev1alpha1.AgentRunning))
			Expect(rec.Events).To(BeEmpty())
			c = held(hold)
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal(reasonQuarantined))
			Eventually(rec.Events).Should(Receive(&e))
			Expect(e).To(HavePrefix("Normal Quarantined "))
			return hold
		}

		It("quarantines a run, keeps its Job, and stops the run after the release", func() {
			hold := quarantineRun()
			release(hold)
			reconcileAgent()
			expectStopped()
			a := agent()
			Expect(a.Finalizers).NotTo(ContainElement(quarantineFinalizer))
			Expect(meta.FindStatusCondition(a.Status.Conditions, corev1alpha1.HoldHeld)).To(BeNil())
			j := &batchv1.Job{}
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "kd-" + name}, j)
			Expect(apierrors.IsNotFound(err) || !j.DeletionTimestamp.IsZero()).To(BeTrue(), "the Job stays: %v", err)
			Expect(gw.created).To(HaveLen(1), "the run does not start again")
		})

		It("keeps a deleted Agent and its Job until the release", func() {
			hold := quarantineRun()
			Expect(k8sClient.Delete(ctx, agent())).To(Succeed())
			reconcileAgent()
			Expect(agent().DeletionTimestamp.IsZero()).To(BeFalse())
			Expect(job().DeletionTimestamp.IsZero()).To(BeTrue())
			release(hold)
			reconcileAgent()
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key(), &corev1alpha1.Agent{}))).To(BeTrue())
		})

		It("quarantines a run that is deleted before the Hold, with no new finalizer", func() {
			newAgent()
			reconcileAgent()
			Expect(k8sClient.Delete(ctx, agent())).To(Succeed())
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			reconcileAgent()
			a := agent()
			Expect(a.DeletionTimestamp.IsZero()).To(BeFalse())
			Expect(a.Finalizers).To(Equal([]string{finalizer}), "the finalizer of the run keeps the Agent")
			Expect(agentQuarantined(a)).To(BeTrue())
			Expect(gw.blocked).To(Equal([]string{gateway.HashKey(gw.created[0].Value)}))
			j := job()
			Expect(j.Spec.ActiveDeadlineSeconds).To(BeNil())
			Expect(j.DeletionTimestamp.IsZero()).To(BeTrue())
			release(hold)
			reconcileAgent()
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key(), &corev1alpha1.Agent{}))).To(BeTrue())
		})

		It("removes the Job deadline of a run that shows Pending", func() {
			// The Job exists, but the status update of the start did not occur.
			newAgent()
			reconcileAgent()
			a := agent()
			a.Status.Phase = corev1alpha1.AgentPending
			Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
			Expect(job().Spec.ActiveDeadlineSeconds).NotTo(BeNil())
			newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			reconcileAgent()
			Expect(agentQuarantined(agent())).To(BeTrue())
			Expect(job().Spec.ActiveDeadlineSeconds).To(BeNil())
		})

		It("quarantines a run that did not start, and the run does not start", func() {
			newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			newAgent()
			reconcileAgent()
			reconcileAgent()
			Expect(gw.created).To(BeEmpty())
			Expect(agent().Finalizers).To(ContainElement(quarantineFinalizer))
			Expect(agentQuarantined(agent())).To(BeTrue())
		})

		It("reports a quarantine with no egress proxy", func() {
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			r := holds()
			r.EgressProxy = false
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: hold}})
			Expect(err).NotTo(HaveOccurred())
			h := &corev1alpha1.Hold{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: hold}, h)).To(Succeed())
			c := meta.FindStatusCondition(h.Status.Conditions, corev1alpha1.HoldHeld)
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(reasonNoEgressProxy))
			var e string
			Eventually(rec.Events).Should(Receive(&e))
			Expect(e).To(HavePrefix("Warning NoEgressProxy "))
		})
	})

	Context("a Workspace", func() {
		const name = "ws"
		var (
			gw  *wsGateway
			ws  *WorkspaceReconciler
			ns  string
			sts *appsv1.StatefulSet
		)
		nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
		reconcileWorkspace := func() {
			_, err := ws.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
			Expect(err).NotTo(HaveOccurred())
		}
		workspace := func() *corev1alpha1.Workspace {
			w := &corev1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, nn(name), w)).To(Succeed())
			return w
		}
		replicas := func() int32 {
			Expect(k8sClient.Get(ctx, nn(name), sts)).To(Succeed())
			return *sts.Spec.Replicas
		}
		secret := func() bool {
			err := k8sClient.Get(ctx, nn(name+"-gateway"), &corev1.Secret{})
			Expect(client.IgnoreNotFound(err)).To(Succeed())
			return err == nil
		}

		BeforeEach(func() {
			n++
			ns = fmt.Sprintf("wshold-%d", n)
			profile := "team-" + ns
			gw = &wsGateway{keys: map[string]bool{}}
			sts = &appsv1.StatefulSet{}
			ws = &WorkspaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
				Image: "img:hold", Gateway: gw, GatewayURL: "http://gw.hold.test:4000", KeyTTL: time.Hour, Recorder: rec}
			o := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns,
				Labels: map[string]string{corev1alpha1.ProfileLabel: profile}}}
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: profile},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{ns}}}
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: corev1alpha1.WorkspaceSpec{Storage: resource.MustParse("1Gi"),
					Gateway: &corev1alpha1.WorkspaceGateway{Model: "m", BudgetPerDay: resource.MustParse("1")}}})).To(Succeed())
			DeferCleanup(func() {
				w := &corev1alpha1.Workspace{}
				if k8sClient.Get(ctx, nn(name), w) == nil && (controllerutil.RemoveFinalizer(w, workspaceFinalizer) ||
					controllerutil.RemoveFinalizer(w, quarantineFinalizer)) {
					controllerutil.RemoveFinalizer(w, quarantineFinalizer)
					Expect(k8sClient.Update(ctx, w)).To(Succeed())
				}
				known := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
				for _, d := range []client.Object{known, p, o} {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, d))).To(Succeed())
				}
			})
		})

		It("blocks the key before it stops the pod, and starts again with a new key after the release", func() {
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			old := workspace().Status.KeyHash
			Expect(old).NotTo(BeEmpty())

			var replicasAtBlock int32
			var secretAtBlock bool
			gw.atBlock = func() { replicasAtBlock, secretAtBlock = replicas(), secret() }
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetWorkspace, Namespace: ns, Name: name},
				corev1alpha1.HoldStop)
			reconcileWorkspace()
			Expect(replicasAtBlock).To(Equal(int32(1)))
			Expect(secretAtBlock).To(BeTrue())
			Expect(replicas()).To(Equal(int32(0)))
			Expect(secret()).To(BeFalse())
			Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage}))
			w := workspace()
			Expect(w.Status.Phase).To(Equal(corev1alpha1.WorkspaceSuspended))
			Expect(w.Status.KeyHash).To(BeEmpty())
			Expect(meta.IsStatusConditionTrue(w.Status.Conditions, corev1alpha1.HoldHeld)).To(BeTrue())
			Expect(meta.FindStatusCondition(w.Status.Conditions, condReady).Reason).To(Equal(reasonHeld))
			stopEvent(hold)
			Expect(held(hold).Status).To(Equal(metav1.ConditionTrue))

			// A second reconcile keeps the stop and makes no key.
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(0)))
			Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage}))

			release(hold)
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			w = workspace()
			Expect(w.Status.KeyHash).NotTo(BeEmpty())
			Expect(w.Status.KeyHash).NotTo(Equal(old))
			Expect(meta.FindStatusCondition(w.Status.Conditions, corev1alpha1.HoldHeld)).To(BeNil())
		})

		It("quarantines the Workspace: blocks the key and keeps the pod, then stops and starts again", func() {
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			old := workspace().Status.KeyHash
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetWorkspace, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			Expect(secret()).To(BeFalse())
			Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage}))
			w := workspace()
			Expect(w.Finalizers).To(ContainElement(quarantineFinalizer))
			Expect(w.Status.KeyHash).To(BeEmpty())
			Expect(workspaceQuarantined(w)).To(BeTrue())
			var e string
			Eventually(rec.Events).Should(Receive(&e))
			Expect(e).To(HavePrefix("Warning Quarantined "))
			c := held(hold)
			Expect(c.Reason).To(Equal(reasonQuarantined))
			Expect(c.Status).To(Equal(metav1.ConditionTrue))

			// A delete waits for the release.
			Expect(k8sClient.Delete(ctx, workspace())).To(Succeed())
			reconcileWorkspace()
			Expect(workspace().DeletionTimestamp.IsZero()).To(BeFalse())
			Expect(replicas()).To(Equal(int32(1)))
			release(hold)
			reconcileWorkspace()
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn(name), &corev1alpha1.Workspace{}))).To(BeTrue())
			Expect(old).NotTo(BeEmpty())
		})

		It("stops the pod after the release of a quarantine, and starts again with a new key", func() {
			reconcileWorkspace()
			old := workspace().Status.KeyHash
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetWorkspace, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			reconcileWorkspace()
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			// The quarantined pod stops after the scale to 0.
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name + "-0", Namespace: ns,
				Labels: map[string]string{WorkspaceLabel: name}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "example.com/ws:test"}}}}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			release(hold)
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(0)))
			Expect(workspace().Finalizers).To(ContainElement(quarantineFinalizer))
			res, err := ws.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(podGoneRecheck))
			Expect(replicas()).To(Equal(int32(0)), "the Workspace waits for the stop of the pod")
			Expect(workspace().Finalizers).To(ContainElement(quarantineFinalizer))
			Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, nn(name+"-0"), &corev1.Pod{}))
			}).Should(BeTrue())
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			w := workspace()
			Expect(w.Finalizers).NotTo(ContainElement(quarantineFinalizer))
			Expect(w.Status.KeyHash).NotTo(BeEmpty())
			Expect(w.Status.KeyHash).NotTo(Equal(old))
			Expect(meta.FindStatusCondition(w.Status.Conditions, corev1alpha1.HoldHeld)).To(BeNil())
		})

		It("stops the pod also when the pod template is not valid", func() {
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			w := workspace()
			w.Spec.PodTemplate = &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{Name: "globals", VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			}}
			Expect(k8sClient.Update(ctx, w)).To(Succeed())
			reconcileWorkspace()
			Expect(workspace().Status.Phase).To(Equal(corev1alpha1.WorkspaceFailed))
			Expect(replicas()).To(Equal(int32(1)), "the controller keeps the StatefulSet of a template that is not valid")
			Expect(secret()).To(BeTrue())

			newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetWorkspace, Namespace: ns, Name: name},
				corev1alpha1.HoldStop)
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(0)))
			Expect(secret()).To(BeFalse())
			Expect(workspace().Status.Phase).To(Equal(corev1alpha1.WorkspaceSuspended))
		})

		It("stops each workload of a Profile, also in a namespace that the Profile binds later", func() {
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(1)))
			profile := "team-" + ns
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetProfile, Name: profile},
				corev1alpha1.HoldStop)
			reconcileWorkspace()
			Expect(replicas()).To(Equal(int32(0)))
			Expect(held(hold).Status).To(Equal(metav1.ConditionTrue))

			// A second namespace that the Profile lists, but with no label, is not bound yet.
			later := ns + "-b"
			o := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: later}}
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed()) })
			p := &corev1alpha1.Profile{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: profile}, p)).To(Succeed())
			p.Spec.Namespaces = append(p.Spec.Namespaces, later)
			Expect(k8sClient.Update(ctx, p)).To(Succeed())
			h, err := stopHold(ctx, k8sClient, corev1alpha1.HoldTargetAgent, later, "job-1", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(h).To(BeNil())

			o.Labels = map[string]string{corev1alpha1.ProfileLabel: profile}
			Expect(k8sClient.Update(ctx, o)).To(Succeed())
			h, err = stopHold(ctx, k8sClient, corev1alpha1.HoldTargetAgent, later, "job-1", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(h).NotTo(BeNil())
			Expect(h.Name).To(Equal(hold))
			// The label change of the namespace reconciles the Agents in it.
			a := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "job-1", Namespace: later},
				Spec: corev1alpha1.AgentSpec{Image: "example.com/hold:test", Command: []string{"/hold"},
					Model: testGatewayModel, Budget: resource.MustParse("1"), DeadlineSeconds: 600}}
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, a))).To(Succeed()) })
			reqs := profileHoldMapper(k8sClient, corev1alpha1.HoldTargetAgent, nil)(ctx, o)
			Expect(reqs).To(ConsistOf(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)}))
			c := held(hold)
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Message).To(Equal("1 of the 2 workloads of the target wait for the stop."))
		})
	})

	Context("the versions of an App", func() {
		const ns = "default"
		var (
			gw      *wsGateway
			av      *AppVersionReconciler
			appName string
			name    string
		)
		nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
		reconcileVersion := func() {
			_, err := av.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
			Expect(err).NotTo(HaveOccurred())
		}

		BeforeEach(func() {
			n++
			appName, name = fmt.Sprintf("held-%d", n), fmt.Sprintf("held-%d-v1", n)
			gw = &wsGateway{keys: map[string]bool{}}
			av = &AppVersionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
				Gateway: gw, GatewayURL: testGatewayURL, KeyTTL: time.Hour, Recorder: rec,
				Egress: EgressProxy{IP: "10.96.0.51", Port: 8080, ClusterDomain: testEgress.ClusterDomain}}
			// A long drain time shows that the stop does not wait for it.
			app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
				Spec: corev1alpha1.AppSpec{MaxLiveVersions: 2, DrainSeconds: new(int32(3600))}}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: corev1alpha1.AppVersionSpec{
					AppRef: corev1alpha1.AppReference{Name: appName}, BudgetPerDay: new(resource.MustParse("5")),
					Template: corev1alpha1.AppVersionTemplate{Model: testGatewayModel,
						PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name: corev1alpha1.AppContainerName, Image: "registry.example.com/hold:v1",
							Ports: []corev1.ContainerPort{{Name: "hold-http", ContainerPort: 8080}},
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/hold-ready", Port: intstr.FromString("hold-http")}}},
						}}}}},
				}})).To(Succeed())
			DeferCleanup(func() {
				v := &corev1alpha1.AppVersion{}
				if k8sClient.Get(ctx, nn(name), v) == nil && (controllerutil.RemoveFinalizer(v, appFinalizer) ||
					controllerutil.RemoveFinalizer(v, quarantineFinalizer)) {
					controllerutil.RemoveFinalizer(v, quarantineFinalizer)
					Expect(k8sClient.Update(ctx, v)).To(Succeed())
				}
				om := metav1.ObjectMeta{Name: "kd-" + name, Namespace: ns}
				for _, o := range []client.Object{&corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}},
					&corev1.Secret{ObjectMeta: om}, &appsv1.Deployment{ObjectMeta: om}, &corev1.Service{ObjectMeta: om},
					&rbacv1.RoleBinding{ObjectMeta: om}, &rbacv1.Role{ObjectMeta: om}, &corev1.ServiceAccount{ObjectMeta: om},
					app} {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed())
				}
			})
		})

		It("quarantines a version that is deleted before the Hold, with no new finalizer", func() {
			reconcileVersion()
			v := &corev1alpha1.AppVersion{}
			Expect(k8sClient.Get(ctx, nn(name), v)).To(Succeed())
			Expect(k8sClient.Delete(ctx, v)).To(Succeed())
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAppVersion, Namespace: ns, Name: name},
				corev1alpha1.HoldQuarantine)
			reconcileVersion()
			Expect(k8sClient.Get(ctx, nn(name), v)).To(Succeed())
			Expect(v.DeletionTimestamp.IsZero()).To(BeFalse())
			Expect(v.Finalizers).To(Equal([]string{appFinalizer}), "the finalizer of the version keeps it")
			Expect(appVersionQuarantined(v)).To(BeTrue())
			Expect(gw.order).To(Equal([]string{opCreate, opBlock}))
			Expect(k8sClient.Get(ctx, nn("kd-"+name), &appsv1.Deployment{})).To(Succeed())
			release(hold)
			reconcileVersion()
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn(name), &corev1alpha1.AppVersion{}))).To(BeTrue())
		})

		It("quarantines the versions of an App: blocks the key and keeps the pods, then revokes after the release", func() {
			reconcileVersion()
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetApp, Namespace: ns, Name: appName},
				corev1alpha1.HoldQuarantine)
			reconcileVersion()
			reconcileVersion()
			Expect(gw.order).To(Equal([]string{opCreate, opBlock}))
			Expect(k8sClient.Get(ctx, nn("kd-"+name), &appsv1.Deployment{})).To(Succeed())
			v := &corev1alpha1.AppVersion{}
			Expect(k8sClient.Get(ctx, nn(name), v)).To(Succeed())
			Expect(v.Finalizers).To(ContainElement(quarantineFinalizer))
			Expect(appVersionQuarantined(v)).To(BeTrue())
			Expect(meta.FindStatusCondition(v.Status.Conditions, corev1alpha1.AppVersionRevoking)).To(BeNil())
			var e string
			Eventually(rec.Events).Should(Receive(&e))
			Expect(e).To(HavePrefix("Warning Quarantined "))
			Expect(held(hold).Reason).To(Equal(reasonQuarantined))

			release(hold)
			reconcileVersion()
			Expect(k8sClient.Get(ctx, nn(name), v)).To(Succeed())
			Expect(v.Finalizers).To(BeEmpty())
			Expect(meta.FindStatusCondition(v.Status.Conditions, corev1alpha1.AppVersionRevoking).Reason).
				To(Equal(reasonStopped))
			Expect(meta.IsStatusConditionTrue(v.Status.Conditions, corev1alpha1.AppVersionRevoked)).To(BeTrue())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+name), &appsv1.Deployment{}))).To(BeTrue())
		})

		It("blocks the key before it deletes the objects, with no drain", func() {
			reconcileVersion()
			Expect(k8sClient.Get(ctx, nn("kd-"+name), &appsv1.Deployment{})).To(Succeed())
			atBlock := 0
			gw.atBlock = func() {
				if k8sClient.Get(ctx, nn("kd-"+name), &appsv1.Deployment{}) == nil {
					atBlock++
				}
			}
			hold := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetApp, Namespace: ns, Name: appName},
				corev1alpha1.HoldStop)
			Expect(holdRequests(ctx, k8sClient, &corev1alpha1.Hold{Spec: corev1alpha1.HoldSpec{
				Target: corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetApp, Namespace: ns, Name: appName}}},
				corev1alpha1.HoldTargetAppVersion, nil)).To(ConsistOf(reconcile.Request{NamespacedName: nn(name)}))

			reconcileVersion()
			Expect(atBlock).To(Equal(1))
			Expect(gw.order).To(Equal([]string{opCreate, opBlock}))
			v := &corev1alpha1.AppVersion{}
			Expect(k8sClient.Get(ctx, nn(name), v)).To(Succeed())
			Expect(meta.FindStatusCondition(v.Status.Conditions, corev1alpha1.AppVersionRevoking).Reason).
				To(Equal(reasonStopped))
			Expect(meta.IsStatusConditionTrue(v.Status.Conditions, corev1alpha1.AppVersionRevoked)).To(BeTrue())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+name), &appsv1.Deployment{}))).To(BeTrue())
			stopEvent(hold)
			Expect(held(hold).Status).To(Equal(metav1.ConditionTrue))

			// The App controllers do not run: the Hold says so.
			r := holds()
			r.AppsEnabled = false
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: hold}})
			Expect(err).NotTo(HaveOccurred())
			h := &corev1alpha1.Hold{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: hold}, h)).To(Succeed())
			Expect(meta.FindStatusCondition(h.Status.Conditions, corev1alpha1.HoldHeld).Reason).To(Equal(reasonAppsDisabled))
		})
	})

	// Two installs in one cluster: each manager watches its own namespaces, and a Profile can bind
	// namespaces of both installs.
	Context("a Profile across two installs", func() {
		var nsA, nsB, profile string
		newAgent := func(ns string) *corev1alpha1.Agent {
			a := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "installs-run", Namespace: ns},
				Spec: corev1alpha1.AgentSpec{Image: "example.com/installs:test", Command: []string{"/installs"},
					Model: testGatewayModel, Budget: resource.MustParse("1"), DeadlineSeconds: 600}}
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, a))).To(Succeed()) })
			return a
		}
		// condition reconciles the Hold with r and gives its Held condition, or nil.
		condition := func(r *HoldReconciler, name string) *metav1.Condition {
			GinkgoHelper()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
			Expect(err).NotTo(HaveOccurred())
			h := &corev1alpha1.Hold{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, h)).To(Succeed())
			return meta.FindStatusCondition(h.Status.Conditions, corev1alpha1.HoldHeld)
		}

		BeforeEach(func() {
			n++
			profile = fmt.Sprintf("installs-%d", n)
			nsA, nsB = profile+"-a", profile+"-b"
			for _, ns := range []string{nsA, nsB} {
				o := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns,
					Labels: map[string]string{corev1alpha1.ProfileLabel: profile}}}
				Expect(k8sClient.Create(ctx, o)).To(Succeed())
				DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed()) })
			}
			p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: profile},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{nsA, nsB}}}
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, p))).To(Succeed()) })
		})

		It("maps the Hold only to the watched namespaces, and each install writes the same status", func() {
			a, b := newAgent(nsA), newAgent(nsB)
			name := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetProfile, Name: profile},
				corev1alpha1.HoldStop)
			h := &corev1alpha1.Hold{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, h)).To(Succeed())
			// The cache of the install of nsA cannot list nsB.
			rd := failReader{Reader: k8sClient, ns: nsB}
			onlyA := []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(a)}}
			Expect(holdRequests(ctx, rd, h, corev1alpha1.HoldTargetAgent, []string{nsA})).To(Equal(onlyA))
			// A failed list of one namespace does not remove the requests of the other namespaces.
			Expect(holdRequests(ctx, rd, h, corev1alpha1.HoldTargetAgent, nil)).To(Equal(onlyA))
			p := &corev1alpha1.Profile{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: profile}, p)).To(Succeed())
			Expect(profileHoldMapper(rd, corev1alpha1.HoldTargetAgent, []string{nsA})(ctx, p)).To(Equal(onlyA))
			Expect(profileHoldMapper(rd, corev1alpha1.HoldTargetAgent, []string{nsA})(ctx,
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsB}})).To(BeEmpty())

			// Each install counts the workloads of the two namespaces. Thus the two managers do not
			// change the status of the other one in a loop.
			for _, scope := range [][]string{{nsA}, {nsB}} {
				c := condition(&HoldReconciler{Client: k8sClient, APIReader: k8sClient, AppsEnabled: true,
					Namespaces: scope}, name)
				Expect(c).NotTo(BeNil())
				Expect(c.Message).To(Equal("2 of the 2 workloads of the target wait for the stop."))
			}

			// Only the install of nsB writes the status of a Hold on an Agent in nsB.
			direct := newHold(corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetAgent, Namespace: nsB,
				Name: b.Name}, corev1alpha1.HoldStop)
			Expect(condition(&HoldReconciler{Client: k8sClient, APIReader: k8sClient, AppsEnabled: true,
				Namespaces: []string{nsA}}, direct)).To(BeNil())
		})

		It("shows AppsDisabled for a Profile with an AppVersion when the App controllers do not run", func() {
			newAgent(nsA)
			av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: "installs-v1", Namespace: nsB},
				Spec: corev1alpha1.AppVersionSpec{
					AppRef: corev1alpha1.AppReference{Name: "installs"}, BudgetPerDay: new(resource.MustParse("5")),
					Template: corev1alpha1.AppVersionTemplate{Model: testGatewayModel,
						PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name: corev1alpha1.AppContainerName, Image: "registry.example.com/installs:v1",
							Ports: []corev1.ContainerPort{{Name: "installs-http", ContainerPort: 8080}},
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/installs-ready", Port: intstr.FromString("installs-http")}}},
						}}}}}}}
			Expect(k8sClient.Create(ctx, av)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, av))).To(Succeed()) })
			r := &HoldReconciler{Client: k8sClient, APIReader: k8sClient, EgressProxy: true}
			target := corev1alpha1.HoldTarget{Kind: corev1alpha1.HoldTargetProfile, Name: profile}
			names := []string{newHold(target, corev1alpha1.HoldStop), newHold(target, corev1alpha1.HoldQuarantine)}
			for _, name := range names {
				res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(appsDisabledRecheck))
				c := condition(r, name)
				Expect(c.Status).To(Equal(metav1.ConditionFalse))
				Expect(c.Reason).To(Equal(reasonAppsDisabled))
				Expect(c.Message).To(Equal(msgVersionsLeft))
			}

			// With no AppVersion, the Hold counts the other workloads.
			Expect(k8sClient.Delete(ctx, av)).To(Succeed())
			for name, reason := range map[string]string{names[0]: reasonStopping, names[1]: reasonQuarantining} {
				res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(BeZero())
				Expect(condition(r, name).Reason).To(Equal(reason))
			}
		})
	})

	It("cuts a long event note at a rune boundary", func() {
		note := eventNote(strings.Repeat("é", maxEventNote))
		Expect(len(note)).To(BeNumerically("<=", maxEventNote))
		Expect(strings.ToValidUTF8(note, "?")).To(Equal(note))
	})
})

// failReader fails the List of one namespace, like the cache of an install that does not watch the
// namespace.
type failReader struct {
	client.Reader
	ns string
}

func (f failReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	o := &client.ListOptions{}
	o.ApplyOptions(opts)
	if o.Namespace == f.ns {
		return fmt.Errorf("unable to list: %s because of unknown namespace for the cache", f.ns)
	}
	return f.Reader.List(ctx, list, opts...)
}
