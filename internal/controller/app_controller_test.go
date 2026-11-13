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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// noRouteReader is an API reader of a cluster that does not have the Gateway API.
type noRouteReader struct{ client.Reader }

func (r noRouteReader) Get(ctx context.Context, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
	if o.GetObjectKind().GroupVersionKind() == httpRouteGVK {
		return &meta.NoKindMatchError{GroupKind: httpRouteGVK.GroupKind()}
	}
	return r.Reader.Get(ctx, key, o, opts...)
}

func (r noRouteReader) List(ctx context.Context, l client.ObjectList, opts ...client.ListOption) error {
	if l.GetObjectKind().GroupVersionKind().Group == gatewayAPIGroup {
		return &meta.NoKindMatchError{GroupKind: httpRouteGVK.GroupKind()}
	}
	return r.Reader.List(ctx, l, opts...)
}

var _ = Describe("App controller", func() {
	const ns = "default"
	const port = "http"
	var (
		ctx     = context.Background()
		rec     *AppReconciler
		appName string
		n       int
	)
	nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
	weight := func(v string, w int32) corev1alpha1.AppEndpointVersion {
		return corev1alpha1.AppEndpointVersion{Name: v, Weight: &w}
	}
	endpoint := func(name string, versions ...corev1alpha1.AppEndpointVersion) corev1alpha1.AppEndpoint {
		return corev1alpha1.AppEndpoint{Name: name, Port: port, Hostnames: []string{"shop.example.com"},
			Gateway: corev1alpha1.AppGatewayReference{Name: "public", Namespace: "gateways"}, Versions: versions}
	}
	newApp := func(endpoints ...corev1alpha1.AppEndpoint) *corev1alpha1.App {
		app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
			Spec: corev1alpha1.AppSpec{Endpoints: endpoints}}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, app))).To(Succeed()) })
		return app
	}
	// newVersion makes an AppVersion in the state that the AppVersion controller gives it: bound
	// to the App, with a key hash and a Service. The hash is a fixture, not the hash of a key.
	newVersion := func(app *corev1alpha1.App, s string, containerPort int32) *corev1alpha1.AppVersion {
		av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: s, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef: corev1alpha1.AppReference{Name: app.Name}, BudgetPerDay: resource.MustParse("5"),
				Template: corev1alpha1.AppVersionTemplate{Model: testGatewayModel,
					PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: corev1alpha1.AppContainerName, Image: "registry.example.com/app:v1",
						Ports: []corev1.ContainerPort{{Name: port, ContainerPort: containerPort}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString(port)}}},
					}}}}},
			}}
		Expect(k8sClient.Create(ctx, av)).To(Succeed())
		av.Status.BoundAppUID, av.Status.KeyHash = app.UID, "fixture-hash"
		Expect(k8sClient.Status().Update(ctx, av)).To(Succeed())
		svc := &corev1.Service{ObjectMeta: appVersionMeta(av), Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: port, Port: containerPort}}}}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, svc))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, av))).To(Succeed())
		})
		return av
	}
	mustReconcile := func() reconcile.Result {
		res, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: nn(appName)})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	route := func(ep string) *unstructured.Unstructured {
		u := newRoute()
		Expect(k8sClient.Get(ctx, nn("kd-"+appName+"."+ep), u)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, u))).To(Succeed()) })
		return u
	}
	// backend is the backendRef of the Service of a version.
	backend := func(suffix string, number, w int32) map[string]any {
		return backendRef("kd-"+appName+suffix, number, w)
	}
	backends := func(u *unstructured.Unstructured) []any {
		rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
		Expect(rules).NotTo(BeEmpty())
		// The weight rule is the last rule. It has no match.
		last := rules[len(rules)-1].(map[string]any)
		Expect(last).NotTo(HaveKey("matches"))
		refs, _, _ := unstructured.NestedSlice(last, "backendRefs")
		return refs
	}
	// pinned gives back the Service names of the session rules, in the order of the route.
	pinned := func(u *unstructured.Unstructured) []string {
		rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
		names := make([]string, 0, len(rules))
		for _, r := range rules[:len(rules)-1] {
			rule := r.(map[string]any)
			matches, _, _ := unstructured.NestedSlice(rule, "matches")
			Expect(matches).To(HaveLen(1))
			headers, _, _ := unstructured.NestedSlice(matches[0].(map[string]any), "headers")
			Expect(headers).To(HaveLen(1))
			refs, _, _ := unstructured.NestedSlice(rule, "backendRefs")
			Expect(refs).To(HaveLen(1))
			svc := refs[0].(map[string]any)[metaNameKey].(string)
			// The header value is the version of the backend, so a session stays on its version.
			Expect(headers[0]).To(Equal(map[string]any{metaNameKey: corev1alpha1.AppVersionHeader,
				"value": svc[len("kd-"):]}))
			names = append(names, svc)
		}
		return names
	}
	status := func() []corev1alpha1.AppEndpointStatus {
		app := &corev1alpha1.App{}
		Expect(k8sClient.Get(ctx, nn(appName), app)).To(Succeed())
		return app.Status.Endpoints
	}
	// report writes the route status of a Gateway controller for the current generation.
	report := func(u *unstructured.Unstructured, typ, st, reason string) {
		cond := map[string]any{"type": typ, "status": st, "reason": reason, "observedGeneration": u.GetGeneration()}
		Expect(unstructured.SetNestedSlice(u.Object, []any{map[string]any{"conditions": []any{cond}}},
			"status", "parents")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, u)).To(Succeed())
	}

	BeforeEach(func() {
		n++
		appName = fmt.Sprintf("route-%d", n)
		rec = &AppReconciler{Client: k8sClient, APIReader: k8sClient}
	})

	It("makes a route with the weight of each version and reports the route state", func() {
		app := newApp(endpoint("web", weight(appName+"-v1", 90), weight(appName+"-v2", 10)))
		newVersion(app, appName+"-v1", 8080)
		newVersion(app, appName+"-v2", 9090)
		Expect(mustReconcile().RequeueAfter).To(Equal(appRoutePoll))

		u := route("web")
		Expect(metav1.IsControlledBy(u, app)).To(BeTrue())
		Expect(u.GetLabels()).To(HaveKeyWithValue(appLabel, appName))
		Expect(u.Object["spec"]).To(HaveKeyWithValue("hostnames", []any{"shop.example.com"}))
		parents, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
		Expect(parents).To(HaveLen(1))
		Expect(parents[0]).To(And(HaveKeyWithValue("group", gatewayAPIGroup), HaveKeyWithValue("namespace", "gateways"),
			HaveLen(4)))
		// Each backend is the Service of a version, with the number of the named port.
		Expect(backends(u)).To(Equal([]any{
			backend("-v1", 8080, 90),
			backend("-v2", 9090, 10),
		}))
		Expect(pinned(u)).To(Equal([]string{"kd-" + appName + "-v1", "kd-" + appName + "-v2"}))
		Expect(status()).To(HaveLen(1))
		Expect(status()[0].RouteState).To(Equal(corev1alpha1.AppRoutePending))
		Expect(status()[0].Weights).To(Equal([]corev1alpha1.AppEndpointVersion{
			weight(appName+"-v1", 90), weight(appName+"-v2", 10)}))

		report(u, "Accepted", "True", "Accepted")
		mustReconcile()
		Expect(status()[0].RouteState).To(Equal(corev1alpha1.AppRouteAccepted))

		report(route("web"), "ResolvedRefs", "False", "BackendNotFound")
		mustReconcile()
		Expect(status()[0].RouteState).To(Equal(corev1alpha1.AppRouteNotAccepted))
		Expect(status()[0].Message).To(ContainSubstring("BackendNotFound"))
	})

	It("gives no traffic to a version that cannot serve", func() {
		app := newApp(endpoint("web", weight(appName+"-v1", 50), weight(appName+"-v2", 50),
			weight(appName+"-v3", 50), weight(appName+"-v4", 0)))
		newVersion(app, appName+"-v1", 8080)
		retiring := newVersion(app, appName+"-v2", 8080)
		retiring.Spec.Lifecycle = corev1alpha1.AppVersionRetiring
		Expect(k8sClient.Update(ctx, retiring)).To(Succeed())
		// v3 does not exist. v4 has the weight 0.
		newVersion(app, appName+"-v4", 8080)
		mustReconcile()

		Expect(backends(route("web"))).To(Equal([]any{
			backend("-v1", 8080, 50)}))
		// The retiring version v2 drains: it has a session rule and no weight. The version v4
		// with the weight 0 gets a session only from a client that sends the header.
		Expect(pinned(route("web"))).To(Equal([]string{"kd-" + appName + "-v1", "kd-" + appName + "-v2",
			"kd-" + appName + "-v4"}))
		st := status()[0]
		Expect(st.Weights).To(Equal([]corev1alpha1.AppEndpointVersion{weight(appName+"-v1", 50),
			weight(appName+"-v2", 0), weight(appName+"-v3", 0), weight(appName+"-v4", 0)}))
		Expect(st.Message).To(ContainSubstring(appName + "-v2, " + appName + "-v3."))
	})

	It("does not send traffic to a version of a different App or to a Service that is not from the version", func() {
		app := newApp(endpoint("web", weight(appName+"-v1", 1), weight(appName+"-v2", 1)))
		other := newVersion(app, appName+"-v1", 8080)
		other.Status.BoundAppUID = "other-uid"
		Expect(k8sClient.Status().Update(ctx, other)).To(Succeed())
		// v2 has a Service with its name, but the version is not the owner.
		v2 := newVersion(app, appName+"-v2", 8080)
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, nn("kd-"+v2.Name), svc)).To(Succeed())
		svc.OwnerReferences = nil
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())
		mustReconcile()
		Expect(backends(route("web"))).To(BeEmpty())
		Expect(pinned(route("web"))).To(BeEmpty())
	})

	It("updates the weights and deletes the route of an endpoint that is gone", func() {
		app := newApp(endpoint("web", weight(appName+"-v1", 100)), endpoint("admin", weight(appName+"-v1", 1)))
		newVersion(app, appName+"-v1", 8080)
		mustReconcile()
		route("admin")

		Expect(k8sClient.Get(ctx, nn(appName), app)).To(Succeed())
		app.Spec.Endpoints = []corev1alpha1.AppEndpoint{endpoint("web", weight(appName+"-v1", 20))}
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		mustReconcile()
		Expect(backends(route("web"))[0]).To(HaveKeyWithValue("weight", int64(20)))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+appName+".admin"), newRoute()))).To(BeTrue())
		Expect(status()).To(HaveLen(1))

		// A second reconcile with no change does not write the route.
		before := route("web").GetResourceVersion()
		mustReconcile()
		Expect(route("web").GetResourceVersion()).To(Equal(before))
	})

	It("does not change a route that is not from the App", func() {
		foreign := newRoute()
		foreign.SetName("kd-" + appName + ".web")
		foreign.SetNamespace(ns)
		foreign.SetLabels(map[string]string{appLabel: appName})
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		app := newApp(endpoint("web", weight(appName+"-v1", 1)))
		newVersion(app, appName+"-v1", 8080)
		mustReconcile()
		Expect(status()[0].RouteState).To(Equal(corev1alpha1.AppRouteNameConflict))
		Expect(route("web").Object).NotTo(HaveKey("spec"))

		// The App has no endpoint now. The route of a different owner stays.
		Expect(k8sClient.Get(ctx, nn(appName), app)).To(Succeed())
		app.Spec.Endpoints = nil
		Expect(k8sClient.Update(ctx, app)).To(Succeed())
		Expect(mustReconcile().RequeueAfter).To(BeZero())
		route("web")
		Expect(status()).To(BeEmpty())
	})

	It("reports a cluster with no Gateway API and makes no route", func() {
		rec.APIReader = noRouteReader{k8sClient}
		newApp(endpoint("web", weight(appName+"-v1", 1)))
		mustReconcile()
		Expect(status()[0].RouteState).To(Equal(corev1alpha1.AppRouteNoGatewayAPI))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, nn("kd-"+appName+".web"), newRoute()))).To(BeTrue())
	})

	It("refuses an endpoint that is not valid", func() {
		bad := func(change func(*corev1alpha1.AppEndpoint)) error {
			ep := endpoint("web", weight("v1", 1))
			change(&ep)
			return k8sClient.Create(ctx, &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{GenerateName: "bad-", Namespace: ns},
				Spec: corev1alpha1.AppSpec{Endpoints: []corev1alpha1.AppEndpoint{ep}}})
		}
		Expect(apierrors.IsInvalid(bad(func(e *corev1alpha1.AppEndpoint) { e.Name = "a.b" }))).To(BeTrue())
		Expect(apierrors.IsInvalid(bad(func(e *corev1alpha1.AppEndpoint) { e.Versions = nil }))).To(BeTrue())
		Expect(apierrors.IsInvalid(bad(func(e *corev1alpha1.AppEndpoint) { e.Versions[0] = weight("v1", -1) }))).To(BeTrue())
		Expect(apierrors.IsInvalid(bad(func(e *corev1alpha1.AppEndpoint) { e.Gateway.Name = "" }))).To(BeTrue())
	})
})
