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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const appName = "shop"

// The CRD validation of the App and the AppVersion. These tests do not run a controller. They
// only send objects to the API server. The budgets are small numbers and the objects hold no key
// value.
var _ = Describe("App and AppVersion CRDs", func() {
	ctx := context.Background()
	ns := metav1.NamespaceDefault
	// These are the schema field names and the fixture values that the tests use more than one time.
	const (
		specField     = "spec"
		otherAppName  = "other"
		appImage      = "registry.example.com/app:v1"
		httpPort      = "http"
		appModel      = "test-model"
		budgetOneUnit = "1"
		sidecarName   = "sidecar"
	)

	create := func(obj client.Object) error {
		err := k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })
		}
		return err
	}

	expectInvalid := func(err error, message string) {
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(message))
	}

	appContainer := func() corev1.Container {
		return corev1.Container{
			Name:  corev1alpha1.AppContainerName,
			Image: appImage,
			Ports: []corev1.ContainerPort{{Name: httpPort, ContainerPort: 8080}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString(httpPort)},
			}},
		}
	}

	newVersion := func(name string, containers ...corev1.Container) *corev1alpha1.AppVersion {
		if len(containers) == 0 {
			containers = []corev1.Container{appContainer()}
		}
		return &corev1alpha1.AppVersion{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef:       corev1alpha1.AppReference{Name: appName},
				BudgetPerDay: new(resource.MustParse(budgetOneUnit)),
				Template: corev1alpha1.AppVersionTemplate{
					Model:       appModel,
					PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: containers}},
				},
			},
		}
	}

	update := func(v *corev1alpha1.AppVersion, change func(*corev1alpha1.AppVersion)) error {
		current := &corev1alpha1.AppVersion{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: v.Name, Namespace: ns}, current)).To(Succeed())
		change(current)
		return k8sClient.Update(ctx, current)
	}

	It("defaults maxLiveVersions of an App to 2", func() {
		app := &corev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: "app-default", Namespace: ns}}
		Expect(create(app)).To(Succeed())
		Expect(app.Spec.MaxLiveVersions).To(Equal(int32(2)))
	})

	It("refuses an App with maxLiveVersions 0", func() {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": corev1alpha1.GroupVersion.String(),
			"kind":       "App",
			specField:    map[string]any{"maxLiveVersions": int64(0)},
		}}
		u.SetName("app-zero")
		u.SetNamespace(ns)
		expectInvalid(create(u), "maxLiveVersions")
	})

	It("accepts a valid AppVersion and defaults the lifecycle", func() {
		v := newVersion("v-valid")
		Expect(create(v)).To(Succeed())
		// replicas has no default, because a version with a scale must not have replicas. The
		// controller gives a version with no replicas and no scale 1 pod.
		Expect(v.Spec.Replicas).To(BeNil())
		Expect(v.Spec.Lifecycle).To(Equal(corev1alpha1.AppVersionActive))
	})

	It("accepts a scale, defaults its minimum to 1, and refuses a scale that is not valid", func() {
		pods := corev1alpha1.AppVersionScaleMetric{Pods: &corev1alpha1.AppVersionPodsMetric{
			Name: "requests_in_flight", AverageValue: resource.MustParse("10")}}
		v := newVersion("v-scale")
		v.Spec.Scale = &corev1alpha1.AppVersionScale{MaxReplicas: 4, Metric: pods}
		Expect(create(v)).To(Succeed())
		Expect(v.Spec.Scale.MinReplicas).To(Equal(new(int32(1))))
		Expect(update(v, func(c *corev1alpha1.AppVersion) {
			c.Spec.Scale.Metric = corev1alpha1.AppVersionScaleMetric{
				CPU: &corev1alpha1.AppVersionCPUMetric{AverageUtilization: 70}}
		})).To(Succeed())
		expectInvalid(update(v, func(c *corev1alpha1.AppVersion) { c.Spec.Replicas = new(int32(2)) }),
			"Set spec.replicas or spec.scale, not the two.")

		for _, tc := range []struct {
			scale   corev1alpha1.AppVersionScale
			message string
		}{
			{corev1alpha1.AppVersionScale{MinReplicas: new(int32(3)), MaxReplicas: 2, Metric: pods},
				"scale.maxReplicas must be equal to or more than scale.minReplicas."},
			{corev1alpha1.AppVersionScale{MinReplicas: new(int32(0)), MaxReplicas: 2, Metric: pods}, "minReplicas"},
			{corev1alpha1.AppVersionScale{MaxReplicas: 2}, "Set metric.cpu or metric.pods, not the two."},
			{corev1alpha1.AppVersionScale{MaxReplicas: 2, Metric: corev1alpha1.AppVersionScaleMetric{
				CPU: &corev1alpha1.AppVersionCPUMetric{AverageUtilization: 70}, Pods: pods.Pods}},
				"Set metric.cpu or metric.pods, not the two."},
			{corev1alpha1.AppVersionScale{MaxReplicas: 2, Metric: corev1alpha1.AppVersionScaleMetric{
				Pods: &corev1alpha1.AppVersionPodsMetric{Name: "a/b", AverageValue: resource.MustParse("1")}}}, "spec.scale.metric.pods.name"},
			{corev1alpha1.AppVersionScale{MaxReplicas: 2, Metric: corev1alpha1.AppVersionScaleMetric{
				Pods: &corev1alpha1.AppVersionPodsMetric{Name: "q", AverageValue: resource.MustParse("0")}}},
				"averageValue must be more than zero"},
		} {
			bad := newVersion("v-bad-scale")
			bad.Spec.Scale = &tc.scale
			expectInvalid(create(bad), tc.message)
		}
	})

	It("accepts an AppVersion with no App, because no controller binds it yet", func() {
		v := newVersion("v-no-app")
		v.Spec.AppRef.Name = "missing"
		Expect(create(v)).To(Succeed())
	})

	It("refuses an AppVersion with no required field", func() {
		for _, field := range []string{"appRef", "template", "budgetPerDay"} {
			v := newVersion("v-missing")
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
			Expect(err).NotTo(HaveOccurred())
			obj := &unstructured.Unstructured{Object: m}
			obj.SetGroupVersionKind(corev1alpha1.GroupVersion.WithKind("AppVersion"))
			unstructured.RemoveNestedField(obj.Object, specField, field)
			expectInvalid(create(obj), field)
		}
	})

	It("refuses a daily budget that is zero or less", func() {
		for _, budget := range []string{"0", "-1"} {
			v := newVersion("v-budget")
			v.Spec.BudgetPerDay = new(resource.MustParse(budget))
			expectInvalid(create(v), "budgetPerDay must be more than zero")
		}
	})

	It("refuses an appRef change", func() {
		v := newVersion("v-appref")
		Expect(create(v)).To(Succeed())
		expectInvalid(update(v, func(c *corev1alpha1.AppVersion) { c.Spec.AppRef.Name = otherAppName }),
			"spec.appRef is immutable.")
	})

	It("accepts a change of the daily budget and of the replicas", func() {
		v := newVersion("v-mutable")
		Expect(create(v)).To(Succeed())
		Expect(update(v, func(c *corev1alpha1.AppVersion) {
			c.Spec.BudgetPerDay = new(resource.MustParse("2.5"))
			c.Spec.Replicas = new(int32(3))
		})).To(Succeed())
	})

	It("refuses negative replicas", func() {
		v := newVersion("v-replicas")
		v.Spec.Replicas = new(int32(-1))
		expectInvalid(create(v), "replicas")
	})

	It("accepts Retiring and refuses a change back to Active", func() {
		v := newVersion("v-retire")
		Expect(create(v)).To(Succeed())
		Expect(update(v, func(c *corev1alpha1.AppVersion) { c.Spec.Lifecycle = corev1alpha1.AppVersionRetiring })).To(Succeed())
		const terminal = "spec.lifecycle Retiring is terminal."
		expectInvalid(update(v, func(c *corev1alpha1.AppVersion) { c.Spec.Lifecycle = corev1alpha1.AppVersionActive }), terminal)
		// If the field is removed, the default sets Active again, and the API server refuses it.
		expectInvalid(update(v, func(c *corev1alpha1.AppVersion) { c.Spec.Lifecycle = "" }), terminal)
	})

	It("refuses a pod template with no app container or with 2 app containers", func() {
		const message = "The pod template must have 1 container with the name app."
		sidecar := appContainer()
		sidecar.Name = sidecarName
		expectInvalid(create(newVersion("v-no-app-container", sidecar)), message)
		expectInvalid(create(newVersion("v-two-app-containers", appContainer(), appContainer())), message)
	})

	It("refuses an app container with no image, no readiness probe or no port", func() {
		const message = "The app container must have an image, a readiness probe and 1 or more ports."
		changes := map[string]func(*corev1.Container){
			"no-image": func(c *corev1.Container) { c.Image = "" },
			"no-probe": func(c *corev1.Container) { c.ReadinessProbe = nil },
			"no-ports": func(c *corev1.Container) { c.Ports = nil },
		}
		for name, change := range changes {
			c := appContainer()
			change(&c)
			err := create(newVersion("v-"+name, c))
			Expect(err).To(HaveOccurred(), name)
			expectInvalid(err, message)
		}
	})

	It("accepts a sidecar with no probe and no port", func() {
		c := appContainer()
		sidecar := corev1.Container{Name: sidecarName, Image: appImage}
		Expect(create(newVersion("v-sidecar", c, sidecar))).To(Succeed())
	})

	It("applies the shared PodTemplate rules", func() {
		hostNetwork := newVersion("v-host-network")
		hostNetwork.Spec.Template.PodTemplate.Spec.HostNetwork = true
		expectInvalid(create(hostNetwork), "host network")

		account := newVersion("v-account")
		account.Spec.Template.PodTemplate.Spec.ServiceAccountName = "default"
		expectInvalid(create(account), "Koodam owns serviceAccountName")

		label := newVersion("v-label")
		label.Spec.Template.PodTemplate.Metadata.Labels = map[string]string{"koodam.dev/agent": "x"}
		expectInvalid(create(label), "reserved")
	})
})
