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
	"errors"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/gateway"
)

// versionCostGroupGateway checks the saved status before a key request. It keeps no key value.
type versionCostGroupGateway struct {
	*countGateway
	reader   client.Reader
	version  types.NamespacedName
	group    string
	calls    int
	renews   int
	saved    bool
	metadata map[string]string
	err      error
}

func (g *versionCostGroupGateway) CreateKey(ctx context.Context, s gateway.KeySpec) error {
	av := &corev1alpha1.AppVersion{}
	g.saved = g.reader.Get(ctx, g.version, av) == nil && av.Status.CostGroup == g.group &&
		av.Status.KeyHash == gateway.HashKey(s.Value) && av.Status.KeyAlias == s.Alias
	g.calls++
	g.metadata = s.Metadata
	return g.err
}

func (g *versionCostGroupGateway) RenewKey(_ context.Context, s gateway.KeySpec) error {
	g.renews++
	g.metadata = s.Metadata
	return nil
}

func versionCostGroupObjects(namespace string, label *string) (
	*corev1alpha1.App, *corev1alpha1.AppVersion, *corev1alpha1.Profile) {
	app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: "cost-app", Namespace: namespace},
		Spec: corev1alpha1.AppSpec{MaxLiveVersions: 2}}
	if label != nil {
		app.Labels = map[string]string{corev1alpha1.CostGroupLabel: *label}
	}
	av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: "cost-version", Namespace: namespace,
		// A label on the version cannot set the cost group of the key.
		Labels: map[string]string{corev1alpha1.CostGroupLabel: testCostGroupDenied}},
		Spec: corev1alpha1.AppVersionSpec{AppRef: corev1alpha1.AppReference{Name: app.Name},
			BudgetPerDay: new(resource.MustParse("5")),
			Template: corev1alpha1.AppVersionTemplate{Model: testGatewayModel,
				PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: corev1alpha1.AppContainerName, Image: testAgentImage,
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("http")}}},
				}}}}}}}
	p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: namespace},
		Spec: corev1alpha1.ProfileSpec{Namespaces: []string{namespace}, CostGroups: []string{testCostGroupAllowed}}}
	return app, av, p
}

var _ = Describe("AppVersion cost group", func() {
	DescribeTable("checks the App label before it makes a key", func(label *string, bound, denied, fixLabel bool) {
		ctx := context.Background()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "cost-version-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ns)).To(Succeed()) })
		app, av, p := versionCostGroupObjects(ns.Name, label)
		if bound {
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, p)).To(Succeed()) })
			ns.Labels = map[string]string{corev1alpha1.ProfileLabel: p.Name}
			Expect(k8sClient.Update(ctx, ns)).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		Expect(k8sClient.Create(ctx, av)).To(Succeed())
		key := client.ObjectKeyFromObject(av)
		gw := &versionCostGroupGateway{countGateway: &countGateway{}, reader: k8sClient, version: key}
		if label != nil {
			gw.group = *label
		}
		r := &AppVersionReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Gateway: gw, GatewayURL: testGatewayURL}
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, av)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		})
		res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, av)).To(Succeed())
		if denied {
			Expect(res.RequeueAfter).To(Equal(appLiveWait))
			c := meta.FindStatusCondition(av.Status.Conditions, condGatewayKey)
			Expect(c).NotTo(BeNil())
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(reasonCostGroupNotAllowed))
			message := fmt.Sprintf("the label koodam.dev/cost-group needs a Profile bound to namespace %s, with the value in spec.costGroups", ns.Name)
			if bound {
				message = fmt.Sprintf("the label koodam.dev/cost-group has the value %q, and Profile %s does not permit it in spec.costGroups", *label, p.Name)
			}
			Expect(c.Message).To(Equal(message))
			Expect(gw.calls).To(BeZero())
			Expect(av.Status.KeyHash).To(BeEmpty())
			Expect(av.Status.CostGroup).To(BeEmpty())
			Expect(av.Finalizers).NotTo(ContainElement(appFinalizer))
			Expect(meta.FindStatusCondition(av.Status.Conditions, corev1alpha1.AppVersionRevoking)).To(BeNil())
			for _, obj := range []client.Object{&corev1.Secret{}, &appsv1.Deployment{}} {
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx,
					types.NamespacedName{Namespace: ns.Name, Name: appVersionName(av)}, obj))).To(BeTrue())
			}
			if !bound {
				return
			}
			if fixLabel {
				app.Labels[corev1alpha1.CostGroupLabel] = testCostGroupAllowed
				Expect(k8sClient.Update(ctx, app)).To(Succeed())
				gw.group = testCostGroupAllowed
			} else {
				p.Spec.CostGroups = append(p.Spec.CostGroups, *label)
				Expect(k8sClient.Update(ctx, p)).To(Succeed())
			}
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, key, av)).To(Succeed())
		}
		Expect(av.Status.CostGroup).To(Equal(gw.group))
		Expect(gw.calls).To(Equal(1))
		Expect(gw.saved).To(BeTrue(), "the status must have the cost group before CreateKey")
		Expect(meta.IsStatusConditionTrue(av.Status.Conditions, condGatewayKey)).To(BeTrue())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: appVersionName(av)}, &appsv1.Deployment{})).To(Succeed())
		if gw.group == "" {
			Expect(gw.metadata).NotTo(HaveKey("costGroup"))
		} else {
			Expect(gw.metadata).To(HaveKeyWithValue("costGroup", gw.group))
		}
	},
		Entry("no label", (*string)(nil), false, false, false),
		Entry("empty label", new(""), false, false, false),
		Entry("allowed App label", new(testCostGroupAllowed), true, false, false),
		Entry("Profile fix permits the key", new(testCostGroupDenied), true, true, false),
		Entry("App label fix permits the key", new(testCostGroupDenied), true, true, true),
		Entry("no bound Profile", new(testCostGroupDenied), false, true, false),
	)
})

func TestAppVersionCostGroupRetry(t *testing.T) {
	for _, group := range []string{"", testCostGroupAllowed} {
		t.Run(group, func(t *testing.T) {
			ctx := t.Context()
			app, av, p := versionCostGroupObjects(testTeam, new(group))
			av.UID, app.UID = "cost-version-uid", "cost-app-uid"
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testTeam,
				Labels: map[string]string{corev1alpha1.ProfileLabel: p.Name}}}
			c := fake.NewClientBuilder().WithScheme(ingWorld(t).Scheme()).WithObjects(ns, app, av, p).
				WithStatusSubresource(&corev1alpha1.AppVersion{}).Build()
			key := client.ObjectKeyFromObject(av)
			gw := &versionCostGroupGateway{countGateway: &countGateway{}, reader: c, version: key, group: group,
				err: errors.New("the test gateway is not ready")}
			r := &AppVersionReconciler{Client: c, Scheme: c.Scheme(), APIReader: c, Gateway: gw, GatewayURL: testGatewayURL}
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err == nil || !gw.saved {
				t.Fatal("the first call did not save the cost group before the gateway error")
			}
			app.Labels[corev1alpha1.CostGroupLabel] = testCostGroupDenied
			if c.Update(ctx, app) != nil || c.Delete(ctx, p) != nil {
				t.Fatal("the test could not change the App label and remove the Profile")
			}
			// The fake API does not copy StringData to Data as the API server does.
			sec := &corev1.Secret{}
			secretKey := types.NamespacedName{Namespace: ns.Name, Name: appVersionName(av)}
			if c.Get(ctx, secretKey, sec) != nil {
				t.Fatal("the test could not read the version Secret")
			}
			sec.Data = map[string][]byte{appKeyField: []byte(sec.StringData[appKeyField])}
			sec.StringData = nil
			if c.Update(ctx, sec) != nil {
				t.Fatal("the test could not update the version Secret")
			}
			gw.err = nil
			// A new reconciler has no renew timer, as after a controller restart.
			r = &AppVersionReconciler{Client: c, Scheme: c.Scheme(), APIReader: c, Gateway: gw, GatewayURL: testGatewayURL}
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
				t.Fatal("the retry failed")
			}
			if c.Get(ctx, key, av) != nil || av.Status.CostGroup != group || gw.calls != 2 || !gw.saved {
				t.Fatal("the retry changed the saved cost group")
			}
			// A renew after another restart also uses the saved group.
			r = &AppVersionReconciler{Client: c, Scheme: c.Scheme(), APIReader: c, Gateway: gw, GatewayURL: testGatewayURL}
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
				t.Fatal("the renew failed")
			}
			if c.Get(ctx, key, av) != nil || av.Status.CostGroup != group || gw.renews != 1 ||
				gw.calls != 2 || gw.metadata["costGroup"] != group {
				t.Fatal("the renew changed the saved cost group")
			}
			if _, present := gw.metadata["costGroup"]; present != (group != "") {
				t.Fatal("the key metadata has the wrong cost group entry")
			}
		})
	}
}
