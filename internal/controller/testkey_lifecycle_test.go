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
	"encoding/json"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
	"github.com/mohanraj00/koodam/internal/podtemplate"
)

// markBound sets the condition ProfileBound of ws, as the Workspace controller does.
func markBound(ctx context.Context, ws *corev1alpha1.Workspace, status metav1.ConditionStatus) {
	GinkgoHelper()
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ws), ws)).To(Succeed())
	meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{Type: condProfileBound,
		Status: status, Reason: "Test", Message: "Set by the test."})
	Expect(k8sClient.Status().Update(ctx, ws)).To(Succeed())
}

// tkGateway records the order of the gateway calls. If missing is true, the gateway has no
// row for any key, as after a block that arrived before the create. If late is true, the next
// Usage shows a row that is not blocked, as after a create that landed after the block.
type tkGateway struct {
	mu      sync.Mutex
	order   []string
	specs   []gateway.KeySpec
	missing bool
	late    bool
}

func (g *tkGateway) add(op string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.order = append(g.order, op)
}

func (g *tkGateway) CreateKey(_ context.Context, s gateway.KeySpec) error {
	g.add(opCreate)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.specs = append(g.specs, s)
	return nil
}

func (g *tkGateway) RenewKey(context.Context, gateway.KeySpec) error { g.add(opRenew); return nil }
func (g *tkGateway) BlockKey(context.Context, string) error          { g.add(opBlock); return nil }

func (g *tkGateway) Usage(context.Context, string) (gateway.Usage, error) {
	g.add(opUsage)
	if g.missing {
		return gateway.Usage{}, gateway.ErrKeyNotFound
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	blocked := !g.late
	g.late = false
	return gateway.Usage{Spend: 0.25, Calls: 3, InputTokens: 12, OutputTokens: 4, Blocked: blocked}, nil
}

func (g *tkGateway) ops() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.order...)
}

var _ = Describe("TestKey lifecycle", func() {
	const ttl = 30 * time.Minute
	var (
		ctx       context.Context
		gw        *tkGateway
		rec       *TestKeyReconciler
		now       time.Time
		workspace *corev1alpha1.Workspace
	)

	newKey := func(testID string) *corev1alpha1.TestKey {
		k := &corev1alpha1.TestKey{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "tk-life-", Namespace: workspace.Namespace},
			Spec: corev1alpha1.TestKeySpec{
				WorkspaceRef: corev1alpha1.TestKeyWorkspaceRef{Name: workspace.Name, UID: workspace.UID},
				TestID:       testID, Budget: resource.MustParse("0.5"), TTL: metav1.Duration{Duration: ttl},
			},
		}
		Expect(k8sClient.Create(ctx, k)).To(Succeed())
		DeferCleanup(func() {
			got := &corev1alpha1.TestKey{}
			if k8sClient.Get(ctx, client.ObjectKeyFromObject(k), got) == nil {
				controllerutil.RemoveFinalizer(got, testKeyFinalizer)
				_ = k8sClient.Update(ctx, got)
				_ = k8sClient.Delete(ctx, got)
			}
		})
		return k
	}
	reconcileKey := func(k *corev1alpha1.TestKey) reconcile.Result {
		res, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(k)})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	get := func(k *corev1alpha1.TestKey) *corev1alpha1.TestKey {
		got := &corev1alpha1.TestKey{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(k), got)).To(Succeed())
		return got
	}
	slot := func() (*corev1.Secret, error) {
		s := &corev1.Secret{}
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: workspace.Namespace,
			Name: testKeySecretName(workspace.Name)}, s)
		return s, err
	}
	reason := func(k *corev1alpha1.TestKey) string {
		c := meta.FindStatusCondition(get(k).Status.Conditions, condActive)
		Expect(c).NotTo(BeNil())
		return c.Reason
	}

	BeforeEach(func() {
		ctx = context.Background()
		now = time.Now().Truncate(time.Second)
		gw = &tkGateway{}
		rec = &TestKeyReconciler{Client: k8sClient, APIReader: k8sClient, Gateway: gw,
			GatewayURL: "http://gateway.test:4000", SingleInstanceGateway: true,
			RevokeWait: time.Minute, Now: func() time.Time { return now }}
		workspace = &corev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "tk-life-ws-", Namespace: metav1.NamespaceDefault},
			Spec: corev1alpha1.WorkspaceSpec{
				Gateway: &corev1alpha1.WorkspaceGateway{Model: testGatewayModel, BudgetPerDay: resource.MustParse("5")},
				TestKeys: &corev1alpha1.WorkspaceTestKeys{
					MaxBudget: resource.MustParse("1"), MaxTTL: metav1.Duration{Duration: time.Hour}},
			},
		}
		Expect(k8sClient.Create(ctx, workspace)).To(Succeed())
		DeferCleanup(k8sClient.Delete, ctx, workspace)
		markBound(ctx, workspace, metav1.ConditionTrue)
		DeferCleanup(func() {
			if s, err := slot(); err == nil {
				_ = k8sClient.Delete(ctx, s)
			}
		})
	})

	It("refuses each key if the gateway is not single-instance", func() {
		rec.SingleInstanceGateway = false
		k := newKey("shape")
		reconcileKey(k)
		Expect(reason(k)).To(Equal(reasonShape))
		Expect(gw.ops()).To(BeEmpty())
		_, err := slot()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("issues a key in the approved order and keeps it on a retry", func() {
		k := newKey(testKeyTestID)
		res := reconcileKey(k)
		Expect(res.RequeueAfter).To(Equal(ttl))

		got := get(k)
		Expect(controllerutil.ContainsFinalizer(got, testKeyFinalizer)).To(BeTrue())
		s, err := slot()
		Expect(err).NotTo(HaveOccurred())
		Expect(metav1.IsControlledBy(s, got)).To(BeTrue())
		Expect(*s.Immutable).To(BeTrue())
		value := string(s.Data[testKeyValueField])
		Expect(value).To(HavePrefix("sk-kd-"))
		Expect(string(s.Data[testKeyURLField])).To(Equal("http://gateway.test:4000"))
		Expect(string(s.Data[testKeyModelField])).To(Equal(testGatewayModel))

		Expect(got.Status.KeyHash).To(Equal(gateway.HashKey(value)))
		Expect(got.Status.KeyAlias).To(Equal("tk-login-smoke-" + string(got.UID)[:8]))
		Expect(string(s.Data[testKeyAliasField])).To(Equal(got.Status.KeyAlias))
		Expect(got.Status.ExpiresAt.Time).To(BeTemporally("==", now.Add(ttl)))
		Expect(reason(k)).To(Equal(reasonIssued))
		Expect(gw.specs).To(HaveLen(1))
		Expect(gw.specs[0]).To(Equal(gateway.KeySpec{Value: value, Alias: got.Status.KeyAlias,
			Model: testGatewayModel, Budget: 0.5, TTL: ttl,
			Metadata: map[string]string{"koodam_test_id": testKeyTestID, "koodam_testkey": got.Name}}))

		// No key value in the status.
		status, err := json.Marshal(got.Status)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(status)).NotTo(ContainSubstring(value))

		// A retry after 10 minutes uses the same key and the remaining TTL.
		now = now.Add(10 * time.Minute)
		reconcileKey(k)
		Expect(gw.specs).To(HaveLen(2))
		Expect(gw.specs[1].Value).To(Equal(value))
		Expect(gw.specs[1].TTL).To(Equal(ttl - 10*time.Minute))
		Expect(get(k).Status.ExpiresAt.Time).To(BeTemporally("==", got.Status.ExpiresAt.Time))
	})

	It("lets only one key of a Workspace use the slot", func() {
		first := newKey("first")
		reconcileKey(first)
		before, err := slot()
		Expect(err).NotTo(HaveOccurred())

		second := newKey("second")
		res := reconcileKey(second)
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(reason(second)).To(Equal(reasonSlotBusy))
		Expect(get(second).Status.KeyHash).To(BeEmpty())
		Expect(gw.specs).To(HaveLen(1))
		after, err := slot()
		Expect(err).NotTo(HaveOccurred())
		Expect(after.UID).To(Equal(before.UID))
	})

	It("blocks first, waits, then reads the spend and deletes the slot", func() {
		k := newKey("revoke")
		reconcileKey(k)
		Expect(k8sClient.Delete(ctx, get(k))).To(Succeed())

		res := reconcileKey(k)
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(reason(k)).To(Equal(reasonTKBlocked))
		Expect(gw.ops()).To(Equal([]string{opCreate, opBlock}))
		_, err := slot()
		Expect(err).NotTo(HaveOccurred(), "the slot stays until the wait ends")

		// Before the wait ends: no spend read and no cleanup.
		now = now.Add(30 * time.Second)
		res = reconcileKey(k)
		Expect(res.RequeueAfter).To(Equal(30 * time.Second))
		Expect(gw.ops()).To(Equal([]string{opCreate, opBlock}))

		now = now.Add(31 * time.Second)
		final := get(k)
		reconcileKey(k)
		Expect(gw.ops()).To(Equal([]string{opCreate, opBlock, opUsage}))
		_, err = slot()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		err = k8sClient.Get(ctx, client.ObjectKeyFromObject(k), final)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the finalizer is gone after the revoke")
	})

	It("records the usage and keeps the TestKey after an expiry", func() {
		k := newKey("expiry")
		reconcileKey(k)
		now = now.Add(ttl)
		reconcileKey(k)
		Expect(reason(k)).To(Equal(reasonTKBlocked))
		now = now.Add(time.Minute)
		reconcileKey(k)

		got := get(k)
		Expect(reason(k)).To(Equal(reasonTKRevoked))
		Expect(got.Status.RevokedAt).NotTo(BeNil())
		Expect(got.Status.Usage).To(Equal(&corev1alpha1.AgentUsage{Spend: "0.25", Calls: 3,
			InputTokens: 12, OutputTokens: 4}))
		Expect(controllerutil.ContainsFinalizer(got, testKeyFinalizer)).To(BeFalse())

		// A Revoked TestKey gets no second key.
		reconcileKey(k)
		Expect(gw.specs).To(HaveLen(1))
	})

	It("keeps the slot and blocks again while a late create can land", func() {
		k := newKey("missing")
		reconcileKey(k)
		gw.missing = true
		ws := &corev1alpha1.Workspace{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workspace), ws)).To(Succeed())
		ws.Spec.Suspend = true
		Expect(k8sClient.Update(ctx, ws)).To(Succeed())

		reconcileKey(k)
		now = now.Add(time.Minute)
		res := reconcileKey(k)
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(gw.ops()).To(Equal([]string{opCreate, opBlock, opUsage, opBlock}))
		Expect(reason(k)).To(Equal(reasonTKBlocked))
		_, err := slot()
		Expect(err).NotTo(HaveOccurred(), "the slot stays while the result is not known")

		// After the expiry plus the wait, a late key cannot be valid.
		now = get(k).Status.ExpiresAt.Add(time.Minute)
		reconcileKey(k)
		got := get(k)
		Expect(reason(k)).To(Equal(reasonTKRevoked))
		Expect(got.Status.Usage).To(BeNil())
		_, err = slot()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("blocks again and waits again if a late create made a row after the block", func() {
		k := newKey("late")
		reconcileKey(k)
		Expect(k8sClient.Delete(ctx, get(k))).To(Succeed())
		reconcileKey(k)
		gw.late = true

		now = now.Add(time.Minute)
		res := reconcileKey(k)
		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(gw.ops()).To(Equal([]string{opCreate, opBlock, opUsage, opBlock}))
		Expect(reason(k)).To(Equal(reasonTKBlocked))
		_, err := slot()
		Expect(err).NotTo(HaveOccurred(), "the slot stays until the row is blocked")

		// The wait counts from the second block.
		now = now.Add(30 * time.Second)
		reconcileKey(k)
		Expect(gw.ops()).To(HaveLen(4))

		now = now.Add(31 * time.Second)
		reconcileKey(k)
		Expect(gw.ops()).To(Equal([]string{opCreate, opBlock, opUsage, opBlock, opUsage}))
		_, err = slot()
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("names the slot under the reserved prefix and apart from the gateway Secrets", func() {
		// A pod template cannot refer to a name with the reserved prefix, so an Agent cannot
		// name the slot before the controller makes it.
		Expect(testKeySecretName("dev")).To(HavePrefix(podtemplate.ReservedSecretPrefix))
		for _, name := range []string{"dev", "dev-test", "kd-dev-testkey", "kd-dev"} {
			other := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}}
			Expect(testKeySecretName("dev")).NotTo(Equal(gatewaySecretName(other)))
		}
	})

	It("refuses a key and revokes it when no Profile binds the namespace", func() {
		k := newKey("unbound")
		reconcileKey(k)
		Expect(reason(k)).To(Equal(reasonIssued))

		markBound(ctx, workspace, metav1.ConditionFalse)
		reconcileKey(k)
		Expect(reason(k)).To(Equal(reasonTKBlocked))
		Expect(gw.ops()).To(Equal([]string{opCreate, opBlock}))

		fresh := newKey("unbound-new")
		reconcileKey(fresh)
		Expect(reason(fresh)).To(Equal(reasonUnbound))
		Expect(gw.specs).To(HaveLen(1), "no key for a TestKey in an unbound namespace")
	})
})
