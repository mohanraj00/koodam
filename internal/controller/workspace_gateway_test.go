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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// The calls that wsGateway records.
const (
	opCreate = "create"
	opRenew  = "renew"
	opBlock  = "block"
	opUsage  = "usage"
)

// wsGateway is a fake gateway for the Workspace tests. atBlock runs at each BlockKey, before
// the block, so a test can see the objects at the time of the block.
type wsGateway struct {
	mu       sync.Mutex
	keys     map[string]bool
	specs    []gateway.KeySpec
	order    []string
	blockErr error
	atBlock  func()
}

func (g *wsGateway) CreateKey(_ context.Context, s gateway.KeySpec) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.keys[gateway.HashKey(s.Value)] = true
	g.specs = append(g.specs, s)
	g.order = append(g.order, opCreate)
	return nil
}

func (g *wsGateway) RenewKey(_ context.Context, s gateway.KeySpec) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.keys[gateway.HashKey(s.Value)] {
		return gateway.ErrKeyNotFound
	}
	g.specs = append(g.specs, s)
	g.order = append(g.order, opRenew)
	return nil
}

func (g *wsGateway) BlockKey(context.Context, string) error {
	if g.atBlock != nil {
		g.atBlock()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.order = append(g.order, opBlock)
	return g.blockErr
}

func (g *wsGateway) Usage(context.Context, string) (gateway.Usage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.order = append(g.order, opUsage)
	return gateway.Usage{Spend: 0.5, Calls: 2, InputTokens: 10, OutputTokens: 5}, nil
}

var _ = Describe("Workspace gateway key", func() {
	const ns = "default"
	const ttl = 2 * time.Hour
	var (
		ctx  context.Context
		logs *bytes.Buffer
		gw   *wsGateway
		name string
		n    int
	)
	nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
	reconcileOnce := func() (ctrl.Result, error) {
		r := &WorkspaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Image: "img:test", Gateway: gw, GatewayURL: "http://gw.test:4000", KeyTTL: ttl}
		return r.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
	}
	mustReconcile := func() ctrl.Result {
		res, err := reconcileOnce()
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	workspace := func() *corev1alpha1.Workspace {
		ws := &corev1alpha1.Workspace{}
		Expect(k8sClient.Get(ctx, nn(name), ws)).To(Succeed())
		return ws
	}
	edit := func(f func(*corev1alpha1.Workspace)) {
		ws := workspace()
		f(ws)
		Expect(k8sClient.Update(ctx, ws)).To(Succeed())
	}
	secretKey := func() string {
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, nn(name+"-gateway"), s)).To(Succeed())
		return string(s.Data[keyField])
	}
	secretGone := func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, nn(name+"-gateway"), &corev1.Secret{}))
	}
	replicas := func() int32 {
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn(name), sts)).To(Succeed())
		return *sts.Spec.Replicas
	}

	BeforeEach(func() {
		n++
		name = fmt.Sprintf("wsgw-%d", n)
		logs = &bytes.Buffer{}
		ctx = logr.NewContext(context.Background(), funcr.New(func(p, a string) {
			logs.WriteString(p + " " + a + "\n")
		}, funcr.Options{Verbosity: 1}))
		gw = &wsGateway{keys: map[string]bool{}}
		ws := &corev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.WorkspaceSpec{Storage: resource.MustParse("1Gi"),
				Gateway: &corev1alpha1.WorkspaceGateway{Model: "local-model", BudgetPerDay: resource.MustParse("5")}},
		}
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		DeferCleanup(func() {
			bg := context.Background()
			w := &corev1alpha1.Workspace{}
			if k8sClient.Get(bg, nn(name), w) == nil && controllerutil.RemoveFinalizer(w, workspaceFinalizer) {
				Expect(k8sClient.Update(bg, w)).To(Succeed())
			}
			om := func(s string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: s, Namespace: ns} }
			for _, o := range []client.Object{&corev1alpha1.Workspace{ObjectMeta: om(name)},
				&corev1.Secret{ObjectMeta: om(name + "-gateway")}, &corev1.Secret{ObjectMeta: om(name + "-ssh-host")},
				&corev1.ConfigMap{ObjectMeta: om(name + "-ssh")}, &appsv1.StatefulSet{ObjectMeta: om(name)}} {
				Expect(client.IgnoreNotFound(k8sClient.Delete(bg, o))).To(Succeed())
			}
		})
	})

	It("makes the key, mounts its Secret and writes only the alias and the hash", func() {
		res := mustReconcile()
		Expect(res.RequeueAfter).To(Equal(ttl / 2))
		ws := workspace()
		key := secretKey()
		Expect(key).To(HavePrefix("sk-kd-"))
		Expect(controllerutil.ContainsFinalizer(ws, workspaceFinalizer)).To(BeTrue())
		Expect(ws.Status.KeyHash).To(Equal(gateway.HashKey(key)))
		Expect(ws.Status.KeyAlias).To(Equal("ws-default-" + name + "-" + ws.Status.KeyHash[:8]))
		Expect(meta.IsStatusConditionTrue(ws.Status.Conditions, condGatewayKey)).To(BeTrue())
		Expect(ws.Status.HostKey).To(HavePrefix("ssh-ed25519 "))

		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, nn(name+"-gateway"), s)).To(Succeed())
		Expect(metav1.IsControlledBy(s, ws)).To(BeTrue())
		Expect(s.Immutable).To(HaveValue(BeTrue()))
		Expect(string(s.Data["ANTHROPIC_BASE_URL"])).To(Equal("http://gw.test:4000"))
		Expect(string(s.Data[modelField])).To(Equal("local-model"))

		Expect(gw.order).To(Equal([]string{opCreate}))
		sp := gw.specs[0]
		Expect(sp.Alias).To(Equal(ws.Status.KeyAlias))
		Expect(sp.Budget).To(Equal(5.0))
		Expect(sp.BudgetPeriod).To(Equal(24 * time.Hour))
		Expect(sp.TTL).To(Equal(ttl))

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn(name), sts)).To(Succeed())
		Expect(sts.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(
			corev1.VolumeMount{Name: "gateway", MountPath: "/etc/koodam/gateway", ReadOnly: true}))
		Expect(sts.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("Secret.SecretName", name+"-gateway")))

		status, err := json.Marshal(ws.Status)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(status)).NotTo(ContainSubstring(key))
		Expect(logs.String()).NotTo(ContainSubstring(key))
	})

	It("renews the key at each reconcile and does not make a new key", func() {
		mustReconcile()
		key := secretKey()
		res := mustReconcile()
		Expect(res.RequeueAfter).To(Equal(ttl / 2))
		Expect(gw.order).To(Equal([]string{opCreate, opRenew}))
		Expect(gw.specs[1].Value).To(Equal(key))
		Expect(gw.specs[1].TTL).To(Equal(ttl))
	})

	It("blocks the key before it scales to 0 on suspend, and makes a new key on resume", func() {
		mustReconcile()
		old := secretKey()
		var replicasAtBlock int32
		var secretAtBlock bool
		gw.atBlock = func() { replicasAtBlock, secretAtBlock = replicas(), !secretGone() }
		edit(func(ws *corev1alpha1.Workspace) { ws.Spec.Suspend = true })
		res := mustReconcile()

		Expect(res.RequeueAfter).To(BeZero())
		Expect(replicasAtBlock).To(Equal(int32(1)))
		Expect(secretAtBlock).To(BeTrue())
		Expect(replicas()).To(Equal(int32(0)))
		Expect(secretGone()).To(BeTrue())
		Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage}))
		ws := workspace()
		Expect(ws.Status.KeyHash).To(BeEmpty())
		Expect(ws.Status.KeyAlias).To(BeEmpty())
		Expect(ws.Status.Usage).To(Equal(&corev1alpha1.WorkspaceUsage{Spend: "0.5", Calls: 2, InputTokens: 10,
			OutputTokens: 5, Source: corev1alpha1.UsageFromGateway}))
		Expect(meta.FindStatusCondition(ws.Status.Conditions, condGatewayKey).Reason).To(Equal("Revoked"))
		Expect(controllerutil.ContainsFinalizer(ws, workspaceFinalizer)).To(BeTrue())

		edit(func(ws *corev1alpha1.Workspace) { ws.Spec.Suspend = false })
		mustReconcile()
		Expect(secretKey()).NotTo(Equal(old))
		Expect(workspace().Status.KeyHash).To(Equal(gateway.HashKey(secretKey())))
		Expect(replicas()).To(Equal(int32(1)))
	})

	It("keeps the finalizer on a gateway error at delete, and blocks before the Secret goes", func() {
		mustReconcile()
		gw.blockErr = errors.New("gateway down")
		Expect(k8sClient.Delete(ctx, workspace())).To(Succeed())
		_, err := reconcileOnce()
		Expect(err).To(HaveOccurred())
		ws := workspace()
		Expect(controllerutil.ContainsFinalizer(ws, workspaceFinalizer)).To(BeTrue())
		Expect(meta.FindStatusCondition(ws.Status.Conditions, condGatewayKey).Reason).To(Equal(reasonRevoking))
		Expect(secretGone()).To(BeFalse())

		gw.blockErr = nil
		var secretAtBlock bool
		gw.atBlock = func() { secretAtBlock = !secretGone() }
		mustReconcile()
		Expect(secretAtBlock).To(BeTrue())
		Expect(secretGone()).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn(name), &corev1alpha1.Workspace{}))).To(BeTrue())
	})

	It("blocks the key, deletes the Secret and removes the mount when spec.gateway is removed", func() {
		mustReconcile()
		edit(func(ws *corev1alpha1.Workspace) { ws.Spec.Gateway = nil })
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage}))
		Expect(secretGone()).To(BeTrue())
		ws := workspace()
		Expect(controllerutil.ContainsFinalizer(ws, workspaceFinalizer)).To(BeFalse())
		Expect(ws.Status.KeyHash).To(BeEmpty())
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn(name), sts)).To(Succeed())
		Expect(sts.Spec.Template.Spec.Volumes).NotTo(ContainElement(HaveField("Name", "gateway")))
	})

	It("makes a new key when the model changes", func() {
		mustReconcile()
		old := secretKey()
		edit(func(ws *corev1alpha1.Workspace) { ws.Spec.Gateway.Model = "other-model" })
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage, opCreate}))
		Expect(secretKey()).NotTo(Equal(old))
		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, nn(name+"-gateway"), s)).To(Succeed())
		Expect(string(s.Data[modelField])).To(Equal("other-model"))
	})
})
