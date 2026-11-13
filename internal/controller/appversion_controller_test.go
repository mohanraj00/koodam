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
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

var _ = Describe("AppVersion controller", func() {
	const ns = "default"
	const ttl = 2 * time.Hour
	const port = "http"
	var (
		ctx     context.Context
		logs    *bytes.Buffer
		gw      *wsGateway
		rec     *AppVersionReconciler
		now     time.Time
		appName string
		name    string
		n       int
	)
	nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
	reconcileName := func(s string) (ctrl.Result, error) {
		return rec.Reconcile(ctx, reconcile.Request{NamespacedName: nn(s)})
	}
	mustReconcile := func() ctrl.Result {
		res, err := reconcileName(name)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	version := func(s string) *corev1alpha1.AppVersion {
		av := &corev1alpha1.AppVersion{}
		Expect(k8sClient.Get(ctx, nn(s), av)).To(Succeed())
		return av
	}
	cond := func(s, t string) *metav1.Condition {
		return meta.FindStatusCondition(version(s).Status.Conditions, t)
	}
	gone := func(o client.Object, s string) bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+s), o))
	}
	newApp := func(maxLive int32) {
		app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
			Spec: corev1alpha1.AppSpec{MaxLiveVersions: maxLive}}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
	}
	newVersion := func(s string, rules ...rbacv1.PolicyRule) {
		av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: s, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef: corev1alpha1.AppReference{Name: appName}, BudgetPerDay: resource.MustParse("5"),
				Template: corev1alpha1.AppVersionTemplate{Model: testGatewayModel, Rules: rules,
					PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: corev1alpha1.AppContainerName, Image: "registry.example.com/app:v1",
						Ports: []corev1.ContainerPort{{Name: port, ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString(port)}}},
					}}}}},
			}}
		Expect(k8sClient.Create(ctx, av)).To(Succeed())
	}
	cleanup := func(s string) {
		bg := context.Background()
		av := &corev1alpha1.AppVersion{}
		if k8sClient.Get(bg, nn(s), av) == nil && controllerutil.RemoveFinalizer(av, appFinalizer) {
			Expect(k8sClient.Update(bg, av)).To(Succeed())
		}
		om := metav1.ObjectMeta{Name: "kd-" + s, Namespace: ns}
		for _, o := range []client.Object{&corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: s, Namespace: ns}},
			&corev1.Secret{ObjectMeta: om}, &appsv1.Deployment{ObjectMeta: om}, &corev1.Service{ObjectMeta: om},
			&rbacv1.RoleBinding{ObjectMeta: om}, &rbacv1.Role{ObjectMeta: om}, &corev1.ServiceAccount{ObjectMeta: om}} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(bg, o))).To(Succeed())
		}
	}

	BeforeEach(func() {
		n++
		appName, name = fmt.Sprintf("shop-%d", n), fmt.Sprintf("shop-%d-v1", n)
		logs = &bytes.Buffer{}
		ctx = logr.NewContext(context.Background(), funcr.New(func(p, a string) {
			logs.WriteString(p + " " + a + "\n")
		}, funcr.Options{Verbosity: 1}))
		gw = &wsGateway{keys: map[string]bool{}}
		now = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
		rec = &AppVersionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Gateway: gw, GatewayURL: testGatewayURL, KeyTTL: ttl, Now: func() time.Time { return now },
			Egress: EgressProxy{IP: "10.96.0.50", Port: 8080, ClusterDomain: testEgress.ClusterDomain}}
		DeferCleanup(func() {
			cleanup(name)
			cleanup(name + "-b")
			app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns}}
			Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), app))).To(Succeed())
		})
	})

	It("makes the key, the Role, the Deployment and the Service, and writes only the alias and the hash", func() {
		newApp(2)
		newVersion(name, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}})
		res := mustReconcile()
		Expect(res.RequeueAfter).To(Equal(ttl / 4))

		s := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), s)).To(Succeed())
		Expect(s.Data).To(HaveLen(1))
		key := string(s.Data[appKeyField])
		Expect(key).To(HavePrefix("sk-kd-"))
		Expect(*s.Immutable).To(BeTrue())

		av := version(name)
		Expect(av.Finalizers).To(ContainElement(appFinalizer))
		Expect(av.Status.KeyHash).To(Equal(gateway.HashKey(key)))
		Expect(av.Status.KeyAlias).To(Equal("kdapp-" + string(av.UID)))
		Expect(av.Status.BoundAppUID).NotTo(BeEmpty())
		Expect(fmt.Sprintf("%+v", av.Status)).NotTo(ContainSubstring(key))
		Expect(logs.String()).NotTo(ContainSubstring(key))
		for _, t := range []string{condProvisioned, condGatewayKey, corev1alpha1.AppVersionTracedEgress} {
			Expect(meta.IsStatusConditionTrue(av.Status.Conditions, t)).To(BeTrue(), t)
		}

		Expect(gw.order).To(Equal([]string{opCreate}))
		spec := gw.specs[0]
		Expect(spec.Value).To(Equal(key))
		Expect(spec.Model).To(Equal(testGatewayModel))
		Expect(spec.Budget).To(Equal(5.0))
		Expect(spec.BudgetPeriod).To(Equal(24 * time.Hour))
		Expect(spec.TTL).To(Equal(ttl))

		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		Expect(metav1.IsControlledBy(d, av)).To(BeTrue())
		Expect(*d.Spec.Replicas).To(Equal(int32(1)))
		pod := d.Spec.Template
		// The egress proxy accepts the App identity only with no Agent, Workspace or run label.
		want := map[string]string{appLabel: appName, appVersionLabel: name}
		Expect(pod.Labels).To(And(HaveKeyWithValue(appLabel, appName), HaveKeyWithValue(appVersionLabel, name)))
		Expect(pod.Labels).NotTo(Or(HaveKey(agentLabel), HaveKey(WorkspaceLabel), HaveKey(RunLabel)))
		Expect(d.Spec.Selector.MatchLabels).To(Equal(want))
		Expect(*pod.Spec.AutomountServiceAccountToken).To(BeTrue())
		Expect(pod.Spec.DNSPolicy).To(Equal(corev1.DNSNone))
		Expect(pod.Spec.DNSConfig.Nameservers).To(Equal([]string{"10.96.0.50"}))
		c := pod.Spec.Containers[0]
		Expect(c.Ports[0].Name).To(Equal(port))
		Expect(c.ReadinessProbe).NotTo(BeNil())
		Expect(c.Env).To(ContainElements(
			corev1.EnvVar{Name: appGatewayURLEnv, Value: testGatewayURL},
			corev1.EnvVar{Name: appModelEnv, Value: testGatewayModel},
			corev1.EnvVar{Name: appKeyFileEnv, Value: "/etc/koodam/gateway/key"}))
		for _, e := range c.Env {
			Expect(e.Value).NotTo(ContainSubstring(key))
		}
		Expect(c.VolumeMounts[0].ReadOnly).To(BeTrue())

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), svc)).To(Succeed())
		Expect(svc.Spec.Selector).To(Equal(want))
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8080)))
		Expect(d.Spec.Template.Spec.SecurityContext.FSGroup).To(Equal(new(appKeyGroup)))
		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), role)).To(Succeed())
		Expect(role.Rules).To(HaveLen(1))

		// A second reconcile makes no gateway call and no change.
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate}))
	})

	It("gives the pod no ServiceAccount token if the version has no rules", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		Expect(*d.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())
	})

	It("makes no key until the App exists", func() {
		newVersion(name)
		mustReconcile()
		Expect(cond(name, condProvisioned).Reason).To(Equal("AppNotFound"))
		Expect(gw.order).To(BeEmpty())
		Expect(gone(&corev1.Secret{}, name)).To(BeTrue())
		Expect(version(name).Finalizers).To(BeEmpty())
		newApp(2)
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate}))
	})

	It("makes no key for a rule that is not in the ceiling", func() {
		newApp(2)
		newVersion(name, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}})
		mustReconcile()
		Expect(cond(name, condProvisioned).Reason).To(Equal("Invalid"))
		Expect(gw.order).To(BeEmpty())
		Expect(gone(&corev1.Secret{}, name)).To(BeTrue())
	})

	It("keeps a new version in wait while the App has maxLiveVersions versions with a key", func() {
		newApp(1)
		newVersion(name)
		mustReconcile()
		newVersion(name + "-b")
		res, err := reconcileName(name + "-b")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(appLiveWait))
		Expect(cond(name+"-b", condProvisioned).Reason).To(Equal("LiveVersionsFull"))
		Expect(gw.order).To(Equal([]string{opCreate}))
		Expect(gone(&corev1.Secret{}, name+"-b")).To(BeTrue())

		// The first version retires. Then the slot is free.
		av := version(name)
		av.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		mustReconcile()
		_, err = reconcileName(name + "-b")
		Expect(err).NotTo(HaveOccurred())
		Expect(gw.order).To(Equal([]string{opCreate, opBlock, opCreate}))
	})

	It("makes the Service again when the ports of the template change", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		av := version(name)
		c := &av.Spec.Template.PodTemplate.Spec.Containers[0]
		c.Ports = append(c.Ports, corev1.ContainerPort{Name: "extra", ContainerPort: 9090})
		Expect(k8sClient.Update(ctx, av)).To(Succeed())

		_, err := reconcileName(name)
		Expect(err).To(HaveOccurred())
		mustReconcile()
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), svc)).To(Succeed())
		Expect(svc.Spec.Ports).To(HaveLen(2))
		Expect(svc.Spec.Ports[1].Port).To(Equal(int32(9090)))
		Expect(cond(name, condProvisioned).Status).To(Equal(metav1.ConditionTrue))
	})

	It("sends the new budget if the spec changed before the status had the key", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		// This is the state after a crash between CreateKey and the status write.
		av := version(name)
		meta.SetStatusCondition(&av.Status.Conditions, metav1.Condition{Type: condGatewayKey,
			Status: metav1.ConditionFalse, Reason: reasonIssuing, ObservedGeneration: av.Generation})
		Expect(k8sClient.Status().Update(ctx, av)).To(Succeed())
		av.Spec.BudgetPerDay = resource.MustParse("2")
		Expect(k8sClient.Update(ctx, av)).To(Succeed())

		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opCreate, opRenew}))
		Expect(gw.specs[2].Budget).To(Equal(2.0))
		c := cond(name, condGatewayKey)
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.ObservedGeneration).To(Equal(version(name).Generation))
	})

	It("sends a new budget to the same key, and applies a replicas change", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		av := version(name)
		av.Spec.BudgetPerDay = resource.MustParse("9")
		av.Spec.Replicas = new(int32(3))
		Expect(k8sClient.Update(ctx, av)).To(Succeed())

		gw.renewErr = errors.New("gateway down")
		_, err := reconcileName(name)
		Expect(err).To(MatchError(errRenewFailed))
		c := cond(name, condGatewayKey)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.ObservedGeneration).To(Equal(int64(1)))

		gw.renewErr = nil
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opRenew}))
		Expect(gw.specs[1].Budget).To(Equal(9.0))
		Expect(gw.specs[1].Value).To(Equal(gw.specs[0].Value))
		c = cond(name, condGatewayKey)
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.ObservedGeneration).To(Equal(version(name).Generation))
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		Expect(*d.Spec.Replicas).To(Equal(int32(3)))
	})

	It("renews the key after TTL/4", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		now = now.Add(ttl/4 + time.Second)
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opRenew}))
	})

	It("blocks the key before it deletes an object, at a delete", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		atBlock := 0
		gw.atBlock = func() {
			for _, o := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &rbacv1.RoleBinding{}, &corev1.Secret{}} {
				if !gone(o, name) {
					atBlock++
				}
			}
		}
		Expect(k8sClient.Delete(ctx, version(name))).To(Succeed())
		mustReconcile()
		Expect(atBlock).To(Equal(4))
		Expect(gw.order).To(Equal([]string{opCreate, opBlock}))
		for _, o := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &rbacv1.RoleBinding{},
			&rbacv1.Role{}, &corev1.ServiceAccount{}, &corev1.Secret{}} {
			Expect(gone(o, name)).To(BeTrue(), "%T", o)
		}
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn(name), &corev1alpha1.AppVersion{}))).To(BeTrue())
	})

	It("keeps the Secret, the objects and the finalizer while the gateway does not confirm the block", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		gw.blockErr = errors.New("gateway down")
		Expect(k8sClient.Delete(ctx, version(name))).To(Succeed())
		_, err := reconcileName(name)
		Expect(err).To(HaveOccurred())
		Expect(version(name).Finalizers).To(ContainElement(appFinalizer))
		Expect(cond(name, corev1alpha1.AppVersionRevoked).Status).To(Equal(metav1.ConditionUnknown))
		Expect(cond(name, corev1alpha1.AppVersionRevoking).Status).To(Equal(metav1.ConditionTrue))
		Expect(gone(&corev1.Secret{}, name)).To(BeFalse())
		Expect(gone(&appsv1.Deployment{}, name)).To(BeFalse())

		gw.blockErr = nil
		mustReconcile()
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn(name), &corev1alpha1.AppVersion{}))).To(BeTrue())
	})

	It("revokes a retired version and keeps the AppVersion", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		av := version(name)
		av.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opBlock}))
		c := cond(name, corev1alpha1.AppVersionRevoked)
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.Reason).To(Equal("Retired"))
		Expect(version(name).Finalizers).To(BeEmpty())
		Expect(gone(&appsv1.Deployment{}, name)).To(BeTrue())
		Expect(gone(&corev1.Secret{}, name)).To(BeTrue())
		// The revoke is terminal.
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opBlock}))
	})

	It("revokes and makes no second key when the gateway lost the key", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		gw.keys = map[string]bool{}
		now = now.Add(ttl)
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opBlock}))
		Expect(cond(name, corev1alpha1.AppVersionRevoked).Reason).To(Equal("KeyLost"))
	})

	It("revokes when the Secret is gone, and when a different App has the name", func() {
		newApp(2)
		newVersion(name)
		newVersion(name + "-b")
		mustReconcile()
		_, err := reconcileName(name + "-b")
		Expect(err).NotTo(HaveOccurred())

		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kd-" + name, Namespace: ns}}
		Expect(k8sClient.Delete(ctx, s)).To(Succeed())
		mustReconcile()
		Expect(cond(name, corev1alpha1.AppVersionRevoked).Reason).To(Equal("SecretLost"))

		app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns}}
		Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		newApp(2)
		_, err = reconcileName(name + "-b")
		Expect(err).NotTo(HaveOccurred())
		Expect(cond(name+"-b", corev1alpha1.AppVersionRevoked).Reason).To(Equal("AppGone"))
		Expect(gw.order).To(Equal([]string{opCreate, opCreate, opBlock, opBlock}))
	})
})
