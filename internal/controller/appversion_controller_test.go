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
	networkingv1 "k8s.io/api/networking/v1"
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
	// outsideCeiling is a rule that the agent-ceiling ClusterRole does not have.
	outsideCeiling := rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}
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
		// The drain time is 0, so that a retiring version revokes its key immediately.
		app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
			Spec: corev1alpha1.AppSpec{MaxLiveVersions: maxLive, DrainSeconds: new(int32(0))}}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
	}
	newVersion := func(s string, rules ...rbacv1.PolicyRule) {
		av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: s, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef: corev1alpha1.AppReference{Name: appName}, BudgetPerDay: new(resource.MustParse("5")),
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
			&rbacv1.RoleBinding{ObjectMeta: om}, &rbacv1.Role{ObjectMeta: om}, &corev1.ServiceAccount{ObjectMeta: om},
			&networkingv1.NetworkPolicy{ObjectMeta: om}} {
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

	It("makes a default-deny NetworkPolicy with egress to the proxy only, and then the egress is traced", func() {
		proxy := map[string]string{"app.kubernetes.io/component": "egress-proxy"}
		rec.Egress.Namespace, rec.Egress.PodLabels = "koodam-system", proxy
		newApp(2)
		newVersion(name)
		mustReconcile()
		// With no ingress namespace, the policy permits no ingress.
		first := &networkingv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), first)).To(Succeed())
		Expect(first.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeIngress))
		Expect(first.Spec.Ingress).To(BeEmpty())
		rec.IngressNamespaces = []string{"gateway-system"}
		mustReconcile()

		np := &networkingv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), np)).To(Succeed())
		Expect(metav1.IsControlledBy(np, version(name))).To(BeTrue())
		Expect(np.Spec.PodSelector.MatchLabels).To(Equal(map[string]string{appLabel: appName, appVersionLabel: name}))
		Expect(np.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
		// The ingress is the port of the app container only, from the pods of the ingress namespace only.
		Expect(np.Spec.Ingress).To(HaveLen(1))
		Expect(np.Spec.Ingress[0].From).To(Equal([]networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: []string{"gateway-system"}}}}}}))
		Expect(np.Spec.Ingress[0].Ports).To(HaveLen(1))
		Expect(np.Spec.Ingress[0].Ports[0].Port.IntValue()).To(Equal(8080))
		// The egress has 1 rule, and its only peer is the proxy pods.
		Expect(np.Spec.Egress).To(HaveLen(1))
		Expect(np.Spec.Egress[0].To).To(Equal([]networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"kubernetes.io/metadata.name": "koodam-system"}},
			PodSelector: &metav1.LabelSelector{MatchLabels: proxy}}}))
		Expect(np.Spec.Egress[0].Ports).To(HaveLen(5))
		egress := cond(name, corev1alpha1.AppVersionTracedEgress)
		Expect(egress.Status).To(Equal(metav1.ConditionTrue))

		// The controller restores a policy that a person made wider.
		np.Spec.Egress = append(np.Spec.Egress, networkingv1.NetworkPolicyEgressRule{})
		Expect(k8sClient.Update(ctx, np)).To(Succeed())
		mustReconcile()
		Expect(k8sClient.Get(ctx, nn("kd-"+name), np)).To(Succeed())
		Expect(np.Spec.Egress).To(HaveLen(1))

		// A policy that is not from the version does not count, and the version makes no pod.
		newVersion(name + "-b")
		foreign := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "kd-" + name + "-b", Namespace: ns}}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		_, err := reconcileName(name + "-b")
		Expect(err).NotTo(HaveOccurred())
		Expect(cond(name+"-b", condProvisioned).Reason).To(Equal(reasonNameConflict))
		Expect(cond(name+"-b", corev1alpha1.AppVersionTracedEgress).Reason).To(Equal(reasonNoEgressPolicy))
		Expect(gone(&appsv1.Deployment{}, name+"-b")).To(BeTrue())
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
		for _, t := range []string{condProvisioned, condGatewayKey} {
			Expect(meta.IsStatusConditionTrue(av.Status.Conditions, t)).To(BeTrue(), t)
		}
		// No NetworkPolicy enforces the egress path of an App pod, so the condition is False.
		egress := meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionTracedEgress)
		Expect(egress.Status).To(Equal(metav1.ConditionFalse))
		Expect(egress.Reason).To(Equal(reasonNoEgressPolicy))

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
		newVersion(name, outsideCeiling)
		mustReconcile()
		Expect(cond(name, condProvisioned).Reason).To(Equal("Invalid"))
		Expect(gw.order).To(BeEmpty())
		Expect(gone(&corev1.Secret{}, name)).To(BeTrue())
	})

	It("makes no key for a template with a Secret name of a Workspace that does not exist", func() {
		newApp(2)
		for i, secret := range []string{"victim" + gatewaySecretSuffix, "victim" + hostKeySecretSuffix, loginSecret} {
			s := fmt.Sprintf("%s-%d", name, i)
			newVersion(s)
			DeferCleanup(cleanup, s)
			av := version(s)
			av.Spec.Template.PodTemplate.Spec.Volumes = []corev1.Volume{{Name: "peer-key", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: secret, Optional: new(true)}}}}
			Expect(k8sClient.Update(ctx, av)).To(Succeed())
			_, err := reconcileName(s)
			Expect(err).NotTo(HaveOccurred())
			Expect(cond(s, condProvisioned).Reason).To(Equal(reasonInvalid), secret)
			Expect(gone(&corev1.Secret{}, s)).To(BeTrue())
		}
		Expect(gw.order).To(BeEmpty())
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
		Expect(cond(name, condStarted).Status).To(Equal(metav1.ConditionTrue))
	})

	It("puts the claim check in front of the app container when the operator configures an issuer", func() {
		rec.Claim = AppClaimCheck{Issuer: "https://issuer.example.com", Audience: "shop", CallerClaim: "sub",
			JWKS: `{"keys":[]}`, Image: "registry.example.com/koodam:test"}
		newApp(2)
		newVersion(name)
		mustReconcile()

		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		pod := d.Spec.Template
		Expect(pod.Spec.InitContainers).To(HaveLen(1))
		check := pod.Spec.InitContainers[0]
		Expect(check.Name).To(Equal(claimContainer))
		Expect(*check.RestartPolicy).To(Equal(corev1.ContainerRestartPolicyAlways))
		Expect(check.Args).To(ContainElements("--issuer", rec.Claim.Issuer, "--route", "15080=8080", "--version", name))
		Expect(*check.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
		Expect(pod.Annotations).To(HaveKeyWithValue(claimJWKSAnnotation, rec.Claim.JWKS))
		// The Service sends the traffic to the check container, not to the app container.
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), svc)).To(Succeed())
		Expect(svc.Spec.Ports).To(HaveLen(1))
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8080)))
		Expect(svc.Spec.Ports[0].TargetPort).To(Equal(intstr.FromInt32(claimPortBase)))
		// With no NetworkPolicy, a pod of the cluster can call the app container directly.
		Expect(cond(name, condClaimCheck).Reason).To(Equal("NoNetworkPolicy"))

		rec.Egress.Namespace, rec.Egress.PodLabels = "proxy-ns", map[string]string{"app": "egress-proxy"}
		rec.IngressNamespaces = []string{"gateway-ns"}
		mustReconcile()
		np := &networkingv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), np)).To(Succeed())
		Expect(np.Spec.Ingress).To(HaveLen(1))
		Expect(np.Spec.Ingress[0].Ports).To(HaveLen(1))
		Expect(np.Spec.Ingress[0].Ports[0].Port.IntValue()).To(Equal(claimPortBase))
		Expect(cond(name, condClaimCheck).Status).To(Equal(metav1.ConditionTrue))

		// With no issuer, the pod has no check container and the status has no condition.
		rec.Claim = AppClaimCheck{}
		_, err := reconcileName(name)
		Expect(err).To(HaveOccurred())
		mustReconcile()
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		Expect(d.Spec.Template.Spec.InitContainers).To(BeEmpty())
		Expect(d.Spec.Template.Annotations).NotTo(HaveKey(claimJWKSAnnotation))
		Expect(cond(name, condClaimCheck)).To(BeNil())
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
		av.Spec.BudgetPerDay = new(resource.MustParse("2"))
		Expect(k8sClient.Update(ctx, av)).To(Succeed())

		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opCreate, opRenew}))
		Expect(gw.specs[2].Budget).To(Equal(2.0))
		c := cond(name, condGatewayKey)
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.ObservedGeneration).To(Equal(version(name).Generation))
	})

	It("sends a new budget and a new model to the same key, and applies the template after the renew", func() {
		const nextModel = "next-model"
		podModel := func() string {
			d := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
			for _, e := range d.Spec.Template.Spec.Containers[0].Env {
				if e.Name == appModelEnv {
					return e.Value
				}
			}
			return ""
		}
		newApp(2)
		newVersion(name)
		mustReconcile()
		av := version(name)
		av.Spec.BudgetPerDay = new(resource.MustParse("9"))
		av.Spec.Replicas = new(int32(3))
		av.Spec.Template.Model = nextModel
		Expect(k8sClient.Update(ctx, av)).To(Succeed())

		gw.renewErr = errors.New("gateway down")
		_, err := reconcileName(name)
		Expect(err).To(MatchError(errRenewFailed))
		// The key refuses the new model, so the pods keep the old model.
		Expect(podModel()).To(Equal(testGatewayModel))
		c := cond(name, condGatewayKey)
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.ObservedGeneration).To(Equal(int64(1)))

		gw.renewErr = nil
		mustReconcile()
		Expect(gw.order).To(Equal([]string{opCreate, opRenew}))
		Expect(gw.specs[1].Budget).To(Equal(9.0))
		Expect(gw.specs[1].Model).To(Equal(nextModel))
		Expect(podModel()).To(Equal(nextModel))
		Expect(gw.specs[1].Value).To(Equal(gw.specs[0].Value))
		c = cond(name, condGatewayKey)
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.ObservedGeneration).To(Equal(version(name).Generation))
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		Expect(*d.Spec.Replicas).To(Equal(int32(3)))
	})

	It("keeps the renew of the key when the template of a live version is not valid", func() {
		newApp(2)
		newVersion(name)
		mustReconcile()
		av := version(name)
		av.Spec.Template.Rules = []rbacv1.PolicyRule{outsideCeiling}
		av.Spec.Template.Model = "model-of-the-refused-template"
		Expect(k8sClient.Update(ctx, av)).To(Succeed())

		now = now.Add(ttl/4 + time.Second)
		res := mustReconcile()
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(gw.order).To(Equal([]string{opCreate, opRenew}))
		// The pods keep the model of the last valid template, so the key keeps it too.
		Expect(gw.specs[1].Model).To(Equal(testGatewayModel))
		Expect(cond(name, condProvisioned).Reason).To(Equal(reasonInvalid))
		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), role)).To(Succeed())
		Expect(role.Rules).To(BeEmpty())
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

	It("makes one key that permits each model of the list", func() {
		const small, large = "model-small", "model-large"
		newApp(2)
		newVersion(name)
		av := version(name)
		av.Spec.Template.Model, av.Spec.Template.Models = "", []string{small, large}
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		mustReconcile()
		Expect(gw.specs).To(HaveLen(1))
		Expect(gw.specs[0].Model).To(Equal(small))
		Expect(gw.specs[0].Models).To(Equal([]string{large}))
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		Expect(d.Spec.Template.Spec.Containers[0].Env).To(ContainElements(
			corev1.EnvVar{Name: appModelEnv, Value: small},
			corev1.EnvVar{Name: appModelsEnv, Value: small + "," + large}))

		// The API server refuses the 2 forms together, a model with no budget and a budget with no model.
		for _, change := range []func(*corev1alpha1.AppVersion){
			func(v *corev1alpha1.AppVersion) { v.Spec.Template.Model = small },
			func(v *corev1alpha1.AppVersion) { v.Spec.BudgetPerDay = nil },
			func(v *corev1alpha1.AppVersion) { v.Spec.Template.Models = nil },
		} {
			bad := version(name)
			change(bad)
			Expect(apierrors.IsInvalid(k8sClient.Update(ctx, bad))).To(BeTrue())
		}
	})

	It("makes no key for a version with no model, and drains it before the delete of its pods", func() {
		newApp(2)
		app := &corev1alpha1.App{}
		Expect(k8sClient.Get(ctx, nn(appName), app)).To(Succeed())
		app.Spec.DrainSeconds = new(int32(600))
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		newVersion(name)
		av := version(name)
		av.Spec.Template.Model, av.Spec.BudgetPerDay = "", nil
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		Expect(mustReconcile().RequeueAfter).To(BeZero())

		// No gateway call, no Secret, no finalizer, no alias and no hash.
		Expect(gw.order).To(BeEmpty())
		Expect(gone(&corev1.Secret{}, name)).To(BeTrue())
		av = version(name)
		Expect(av.Finalizers).To(BeEmpty())
		Expect(av.Status.KeyAlias).To(BeEmpty())
		Expect(av.Status.KeyHash).To(BeEmpty())
		Expect(cond(name, condGatewayKey).Reason).To(Equal(reasonNoModel))
		Expect(cond(name, condProvisioned).Status).To(Equal(metav1.ConditionTrue))
		// The pod has no key file and no gateway variable. The Role and the Service are there.
		d := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, nn("kd-"+name), d)).To(Succeed())
		Expect(d.Spec.Template.Spec.Volumes).To(BeEmpty())
		Expect(d.Spec.Template.Spec.Containers[0].Env).To(Equal([]corev1.EnvVar{{Name: appVersionEnv, Value: name}}))
		Expect(gone(&corev1.Service{}, name)).To(BeFalse())
		Expect(gone(&rbacv1.Role{}, name)).To(BeFalse())

		// A version with no model does not use a live slot.
		newVersion(name + "-b")
		_, err := reconcileName(name + "-b")
		Expect(err).NotTo(HaveOccurred())
		newVersion(name + "-c")
		DeferCleanup(func() { cleanup(name + "-c") })
		_, err = reconcileName(name + "-c")
		Expect(err).NotTo(HaveOccurred())
		Expect(cond(name+"-c", condProvisioned).Status).To(Equal(metav1.ConditionTrue))

		av.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		Expect(mustReconcile().RequeueAfter).To(Equal(10 * time.Minute))
		Expect(gone(&appsv1.Deployment{}, name)).To(BeFalse())
		now = now.Add(10 * time.Minute)
		mustReconcile()
		Expect(cond(name, corev1alpha1.AppVersionRevoked).Status).To(Equal(metav1.ConditionTrue))
		Expect(gone(&appsv1.Deployment{}, name)).To(BeTrue())
		Expect(gone(&corev1.Service{}, name)).To(BeTrue())
		Expect(gw.order).NotTo(ContainElement(opBlock))
	})

	It("drains a retiring version before the revoke", func() {
		newApp(2)
		app := &corev1alpha1.App{}
		Expect(k8sClient.Get(ctx, nn(appName), app)).To(Succeed())
		app.Spec.DrainSeconds = new(int32(600))
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		newVersion(name)
		mustReconcile()
		Expect(cond(name, corev1alpha1.AppVersionTrafficAllowed).Status).To(Equal(metav1.ConditionTrue))

		av := version(name)
		av.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		// In the drain time the version keeps its key, its Secret and its pods.
		Expect(mustReconcile().RequeueAfter).To(Equal(10 * time.Minute))
		Expect(cond(name, corev1alpha1.AppVersionTrafficAllowed).Status).To(Equal(metav1.ConditionFalse))
		Expect(cond(name, corev1alpha1.AppVersionSessionsEnded).Reason).To(Equal("Draining"))
		Expect(cond(name, corev1alpha1.AppVersionRevoking)).To(BeNil())
		Expect(gw.order).NotTo(ContainElement(opBlock))
		Expect(gone(&appsv1.Deployment{}, name)).To(BeFalse())

		// The start of the drain does not move with a later reconcile.
		now = now.Add(9 * time.Minute)
		Expect(mustReconcile().RequeueAfter).To(Equal(time.Minute))
		Expect(gw.order).NotTo(ContainElement(opBlock))

		// At the end of the drain time the key is blocked before the objects go.
		now = now.Add(time.Minute)
		mustReconcile()
		Expect(gw.order[len(gw.order)-1]).To(Equal(opBlock))
		Expect(cond(name, corev1alpha1.AppVersionSessionsEnded).Status).To(Equal(metav1.ConditionTrue))
		Expect(cond(name, corev1alpha1.AppVersionRevoked).Reason).To(Equal("Retired"))
		Expect(gone(&appsv1.Deployment{}, name)).To(BeTrue())
	})

	It("does not wait for the drain time when a retiring version is deleted", func() {
		newApp(2)
		app := &corev1alpha1.App{}
		Expect(k8sClient.Get(ctx, nn(appName), app)).To(Succeed())
		app.Spec.DrainSeconds = new(int32(600))
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		newVersion(name)
		mustReconcile()
		av := version(name)
		av.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		mustReconcile()
		Expect(k8sClient.Delete(ctx, version(name))).To(Succeed())
		mustReconcile()
		Expect(gw.order[len(gw.order)-1]).To(Equal(opBlock))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn(name), &corev1alpha1.AppVersion{}))).To(BeTrue())
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
