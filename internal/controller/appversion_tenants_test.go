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
	"slices"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/appclaim"
)

const (
	limitsApp      = "storefront"
	limitsNS       = "tenants-a"
	limitsIssuer   = "https://issuer.example.com"
	limitsTenantA  = "tenant-a"
	limitsTenantB  = "tenant-b"
	limitsClaimKey = "tenant"
)

func limitsQuota(name, ns, tenant string, change func(*corev1alpha1.TenantQuota)) corev1alpha1.TenantQuota {
	q := corev1alpha1.TenantQuota{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ns + "/" + name)},
		Spec: corev1alpha1.TenantQuotaSpec{Tenant: tenant}}
	if change != nil {
		change(&q)
	}
	return q
}

func limitsRate(perSecond string, burst *int32) func(*corev1alpha1.TenantQuota) {
	return func(q *corev1alpha1.TenantQuota) {
		q.Spec.RateLimit = &corev1alpha1.TenantRateLimit{RequestsPerSecond: resource.MustParse(perSecond), Burst: burst}
	}
}

func TestPodLimit(t *testing.T) {
	for _, tc := range []struct {
		rate            float64
		burst, replicas int32
		want            appclaim.TenantLimit
	}{
		{rate: 0, burst: 5, replicas: 2, want: appclaim.TenantLimit{}},
		{rate: 6, burst: 0, replicas: 1, want: appclaim.TenantLimit{Rate: 6, Burst: 6}},
		{rate: 6, burst: 9, replicas: 3, want: appclaim.TenantLimit{Rate: 2, Burst: 3}},
		// The burst of a pod is 1 or more, and it does not go below the part of the pod.
		{rate: 0.5, burst: 0, replicas: 4, want: appclaim.TenantLimit{Rate: 0.125, Burst: 1}},
		{rate: 5, burst: 7, replicas: 2, want: appclaim.TenantLimit{Rate: 2.5, Burst: 4}},
		// A version with 0 replicas has the limit of 1 pod.
		{rate: 3, burst: 2, replicas: 0, want: appclaim.TenantLimit{Rate: 3, Burst: 2}},
	} {
		if got := podLimit(tc.rate, tc.burst, tc.replicas); got != tc.want {
			t.Errorf("podLimit(%v, %d, %d) = %+v, want %+v", tc.rate, tc.burst, tc.replicas, got, tc.want)
		}
	}
}

func TestTenantLimitsOfVersion(t *testing.T) {
	av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: "v1", Namespace: limitsNS},
		Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: limitsApp}, Replicas: new(int32(2))}}
	older := metav1.NewTime(metav1.Now().Add(-3600e9))
	quotas := []corev1alpha1.TenantQuota{
		// A quota of the namespace with no App, at its limit.
		limitsQuota("a", limitsNS, limitsTenantA, func(q *corev1alpha1.TenantQuota) {
			limitsRate("4", new(int32(6)))(q)
			q.Status.Conditions = []metav1.Condition{{Type: corev1alpha1.TenantQuotaExceeded, Status: metav1.ConditionTrue}}
		}),
		// A quota for this App, with no limit: the tenant is known.
		limitsQuota("b", limitsNS, limitsTenantB, func(q *corev1alpha1.TenantQuota) {
			q.Spec.AppRef = &corev1alpha1.AppReference{Name: limitsApp}
		}),
		// These have no effect on this version: a different App, a different namespace, a quota in
		// delete, and a tenant label that an older quota of a different namespace has.
		limitsQuota("c", limitsNS, "other-app", func(q *corev1alpha1.TenantQuota) {
			q.Spec.AppRef = &corev1alpha1.AppReference{Name: "search"}
		}),
		limitsQuota("d", "tenants-z", "other-namespace", limitsRate("1", nil)),
		limitsQuota("e", limitsNS, "in-delete", func(q *corev1alpha1.TenantQuota) { q.DeletionTimestamp = &older }),
		limitsQuota("f", limitsNS, "taken", func(q *corev1alpha1.TenantQuota) {
			limitsRate("1", nil)(q)
			q.CreationTimestamp = metav1.Now()
		}),
		limitsQuota("g", "tenants-z", "taken", func(q *corev1alpha1.TenantQuota) { q.CreationTimestamp = older }),
		limitsQuota("h", limitsNS, "taken", func(q *corev1alpha1.TenantQuota) { q.CreationTimestamp = metav1.Now() }),
	}
	claim := AppClaimCheck{Issuer: limitsIssuer, TenantClaim: limitsClaimKey,
		Tenants: AppTenantLimits{DefaultRate: 1, RefuseUnknown: true}}
	got := claim.limits(av, 2, quotas)
	want := appclaim.TenantLimits{RefuseUnknown: true, Default: &appclaim.TenantLimit{Rate: 0.5, Burst: 1},
		Tenants: map[string]appclaim.TenantLimit{
			limitsTenantA: {Rate: 2, Burst: 3, Exceeded: true},
			limitsTenantB: {},
		}}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Errorf("limits %s, want %s", a, b)
	}
	// The owner of each tenant label is the same when the list is read one time.
	owners := tenantOwners(quotas)
	for i := range quotas {
		if label := quotas[i].Spec.Tenant; owners[label] != tenantOwner(quotas, label) {
			t.Errorf("tenantOwners gives a different owner for the tenant label %q.", label)
		}
	}
	if owners["taken"] == nil || owners["taken"].Name != "g" || owners["in-delete"] != nil {
		t.Errorf("The owner of a label is not correct: %v, %v.", owners["taken"], owners["in-delete"])
	}
	// The check container accepts the file.
	if _, err := appclaim.ParseTenantLimits(a); err != nil {
		t.Errorf("the check container refuses the file: %v", err)
	}
	// With no default and no TenantQuota, the file has no tenant and no default.
	empty, _ := json.Marshal(AppClaimCheck{}.limits(av, 1, nil))
	if string(empty) != `{}` {
		t.Errorf("empty limits: %s", empty)
	}
}

// The check container gets the limits file only with a tenant claim.
// A caller can select each version with a weight above 0. Thus the pods of all these versions
// share the rate of a tenant, and 2 versions do not give the tenant 2 times its rate.
func TestTenantRateIsForAllTheVersionsOfTheApp(t *testing.T) {
	version := func(name, app string, replicas int32) *corev1alpha1.AppVersion {
		return &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: limitsNS, UID: types.UID(name)},
			Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: app}, Replicas: &replicas}}
	}
	weights := func(pairs map[string]int32) []corev1alpha1.AppEndpointVersion {
		out := make([]corev1alpha1.AppEndpointVersion, 0, len(pairs))
		for name, w := range pairs {
			out = append(out, corev1alpha1.AppEndpointVersion{Name: name, Weight: &w})
		}
		return out
	}
	app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: limitsApp, Namespace: limitsNS},
		Spec: corev1alpha1.AppSpec{Endpoints: []corev1alpha1.AppEndpoint{
			{Name: "front", Versions: weights(map[string]int32{"v1": 90, "v2": 0, "v3": 0})},
			// A weight in one endpoint is sufficient.
			{Name: "back", Versions: weights(map[string]int32{"v2": 1})},
		}}}
	v1, v2 := version("v1", limitsApp, 2), version("v2", limitsApp, 3)
	// These versions get no request of the App: no weight, not in the spec, and a different App.
	v3, v4, other := version("v3", limitsApp, 5), version("v4", limitsApp, 7), version("o1", "search", 11)
	alone := version("alone", "no-app", 4)
	quota := limitsQuota("a", limitsNS, limitsTenantA, limitsRate("10", new(int32(20))))
	c := fake.NewClientBuilder().WithScheme(ingWorld(t).Scheme()).
		WithObjects(app, v1, v2, v3, v4, other, alone, &quota).Build()
	claim := AppClaimCheck{Issuer: limitsIssuer, TenantClaim: limitsClaimKey}
	pod := func(av *corev1alpha1.AppVersion) appclaim.TenantLimit {
		t.Helper()
		if err := applyTenantLimits(t.Context(), c, c, claim, av); err != nil {
			t.Fatal(err)
		}
		cm := &corev1.ConfigMap{}
		if err := c.Get(t.Context(), types.NamespacedName{Namespace: limitsNS, Name: tenantLimitsName(av)}, cm); err != nil {
			t.Fatal(err)
		}
		limits, err := appclaim.ParseTenantLimits([]byte(cm.Data[tenantsFile]))
		if err != nil {
			t.Fatal(err)
		}
		return limits.Tenants[limitsTenantA]
	}
	// The 2 versions with a weight have 5 pods together.
	want := appclaim.TenantLimit{Rate: 2, Burst: 4}
	if a, b := pod(v1), pod(v2); a != want || b != want {
		t.Errorf("The limit of a pod is %+v for v1 and %+v for v2, want %+v.", a, b, want)
	}
	if sum := 2*pod(v1).Rate + 3*pod(v2).Rate; sum != 10 {
		t.Errorf("The pods of the 2 versions permit %v requests for each second, want 10.", sum)
	}
	// A version with no weight has its pods and the pods of the versions with a weight in the sum.
	if got := pod(v3); got != (appclaim.TenantLimit{Rate: 1, Burst: 2}) {
		t.Errorf("The limit of a pod of v3 is %+v.", got)
	}
	// A version with no App has only its pods in the sum.
	if n, err := appReplicas(t.Context(), c, alone); err != nil || n != 4 {
		t.Errorf("The replicas of a version with no App are %d, %v.", n, err)
	}

	// A change of the App or of one version builds the file of each version of the App again.
	r := &TenantLimitsReconciler{Client: c}
	names := func(reqs []reconcile.Request) []string {
		out := make([]string, 0, len(reqs))
		for _, req := range reqs {
			out = append(out, req.Name)
		}
		slices.Sort(out)
		return out
	}
	siblings := []string{"v1", "v2", "v3", "v4"}
	if got := names(r.appVersions(t.Context(), app)); !slices.Equal(got, siblings) {
		t.Errorf("A change of the App gives the versions %v.", got)
	}
	if got := names(r.appVersions(t.Context(), v2)); !slices.Equal(got, siblings) {
		t.Errorf("A change of a version gives the versions %v.", got)
	}
	if got := names(r.allVersions(t.Context(), nil)); len(got) != 6 {
		t.Errorf("A change of a TenantQuota gives the versions %v.", got)
	}
}

func TestClaimTenantsVolume(t *testing.T) {
	av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: "v1"},
		Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: limitsApp}}}
	ports := []corev1.ContainerPort{{ContainerPort: 8080}}
	for _, tenantClaim := range []string{"", limitsClaimKey} {
		claim := AppClaimCheck{Issuer: limitsIssuer, TenantClaim: tenantClaim, Image: "registry.example.com/koodam:tenants"}
		tmpl := &corev1.PodTemplateSpec{}
		if err := claim.add(tmpl, av, ports); err != nil {
			t.Fatal(err)
		}
		check := tmpl.Spec.InitContainers[0]
		hasArg := slices.Contains(check.Args, "--tenants-file")
		hasVolume := slices.ContainsFunc(tmpl.Spec.Volumes, func(v corev1.Volume) bool {
			return v.Name == tenantsVolume && v.ConfigMap != nil && v.ConfigMap.Name == "kd-v1-tenants"
		})
		hasMount := slices.ContainsFunc(check.VolumeMounts, func(m corev1.VolumeMount) bool {
			return m.Name == tenantsVolume && m.MountPath == tenantsDir && m.ReadOnly && m.SubPath == ""
		})
		if on := tenantClaim != ""; hasArg != on || hasVolume != on || hasMount != on {
			t.Errorf("tenant claim %q: arg %v, volume %v, mount %v", tenantClaim, hasArg, hasVolume, hasMount)
		}
		// A volume of the user with the reserved name is refused only if the check uses the name.
		taken := &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: tenantsVolume}}}}
		if err := claim.add(taken, av, ports); (err != nil) != (tenantClaim != "") {
			t.Errorf("tenant claim %q: a user volume with the reserved name gives %v", tenantClaim, err)
		}
	}
}

var _ = Describe("Tenant limits file", func() {
	ctx := context.Background()
	claim := AppClaimCheck{Issuer: limitsIssuer, TenantClaim: limitsClaimKey}
	var ns string

	BeforeEach(func() {
		n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "tl-"}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		ns = n.Name
	})

	newVersion := func(name string) *corev1alpha1.AppVersion {
		av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: limitsApp},
				Template: corev1alpha1.AppVersionTemplate{PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: corev1alpha1.AppContainerName, Image: "registry.example.com/app:t",
						Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8080)}}}}}}}}}}
		Expect(k8sClient.Create(ctx, av)).To(Succeed())
		return av
	}
	file := func(av *corev1alpha1.AppVersion) appclaim.TenantLimits {
		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: tenantLimitsName(av)}, cm)).To(Succeed())
		Expect(metav1.IsControlledBy(cm, av)).To(BeTrue())
		limits, err := appclaim.ParseTenantLimits([]byte(cm.Data[tenantsFile]))
		Expect(err).NotTo(HaveOccurred())
		return *limits
	}

	It("writes the file of a version and changes it with the TenantQuota", func() {
		r := &TenantLimitsReconciler{Client: k8sClient, APIReader: k8sClient, Claim: claim}
		av := newVersion("tl-v1")
		req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(av)}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(file(av).Tenants).To(BeEmpty())
		Expect(r.allVersions(ctx, nil)).To(ContainElement(req))

		q := &corev1alpha1.TenantQuota{ObjectMeta: metav1.ObjectMeta{Name: "tl-q", Namespace: ns},
			Spec: corev1alpha1.TenantQuotaSpec{Tenant: "tl-" + ns}}
		limitsRate("2", nil)(q)
		Expect(k8sClient.Create(ctx, q)).To(Succeed())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(file(av).Tenants).To(Equal(map[string]appclaim.TenantLimit{q.Spec.Tenant: {Rate: 2, Burst: 2}}))

		// The mark of the controller goes into the file.
		q.Status.Conditions = []metav1.Condition{{Type: corev1alpha1.TenantQuotaExceeded, Status: metav1.ConditionTrue,
			Reason: corev1alpha1.ReasonTenantQuotaExceeded, LastTransitionTime: metav1.Now()}}
		Expect(k8sClient.Status().Update(ctx, q)).To(Succeed())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(file(av).Tenants[q.Spec.Tenant].Exceeded).To(BeTrue())

		// A reconcile with no change does not write the ConfigMap.
		cm := &corev1.ConfigMap{}
		key := types.NamespacedName{Namespace: ns, Name: tenantLimitsName(av)}
		Expect(k8sClient.Get(ctx, key, cm)).To(Succeed())
		version := cm.ResourceVersion
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, cm)).To(Succeed())
		Expect(cm.ResourceVersion).To(Equal(version))
	})

	It("does not change a ConfigMap that is not from the version, and writes no file with no tenant claim", func() {
		av := newVersion("tl-v2")
		foreign := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: tenantLimitsName(av), Namespace: ns},
			Data: map[string]string{tenantsFile: "user data"}}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		Expect(applyTenantLimits(ctx, k8sClient, k8sClient, claim, av)).To(MatchError(errNotOwned))
		r := &TenantLimitsReconciler{Client: k8sClient, APIReader: k8sClient, Claim: claim}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(av)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foreign), foreign)).To(Succeed())
		Expect(foreign.Data[tenantsFile]).To(Equal("user data"))

		other := newVersion("tl-v3")
		Expect(applyTenantLimits(ctx, k8sClient, k8sClient, AppClaimCheck{Issuer: limitsIssuer}, other)).To(Succeed())
		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: tenantLimitsName(other)}, &corev1.ConfigMap{})
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
	})
})
