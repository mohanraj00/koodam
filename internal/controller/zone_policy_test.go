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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// These specs render the zone policies of the chart with helm and apply them to the envtest API
// server. They run no controller. The policies are for the namespace zone-in only.
var _ = Describe("Zone placement admission policy", Ordered, func() {
	const (
		inNS, outNS       = "zone-in", "zone-out"
		here, far         = "zone-a", "zone-b"
		nearClass         = "near-disk"
		farClass          = "far-disk"
		zoneKey           = "topology.kubernetes.io/zone"
		regionKey         = "topology.kubernetes.io/region"
		oldZoneKey        = "failure-domain.beta.kubernetes.io/zone"
		reason            = "ZonePlacement"
		before, beforeOut = "placed-before", "placed-out"
	)
	ctx := context.Background()
	newVersion := func(ns, name string, change func(*corev1.PodSpec)) *corev1alpha1.AppVersion {
		av := &corev1alpha1.AppVersion{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef: corev1alpha1.AppReference{Name: "zone-app"}, BudgetPerDay: new(resource.MustParse("5")),
				Template: corev1alpha1.AppVersionTemplate{Model: testGatewayModel,
					PodTemplate: corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: corev1alpha1.AppContainerName, Image: "registry.example.com/zone:v1",
						Ports: []corev1.ContainerPort{{Name: "zoned", ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/ok", Port: intstr.FromString("zoned")}}},
					}}}}},
			}}
		if change != nil {
			change(&av.Spec.Template.PodTemplate.Spec)
		}
		return av
	}
	selector := func(key, value string) func(*corev1.PodSpec) {
		return func(p *corev1.PodSpec) { p.NodeSelector = map[string]string{key: value} }
	}
	term := func(key string, op corev1.NodeSelectorOperator, values ...string) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
			{Key: key, Operator: op, Values: values}}}
	}
	required := func(terms ...corev1.NodeSelectorTerm) func(*corev1.PodSpec) {
		return func(p *corev1.PodSpec) {
			p.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms}}}
		}
	}
	preferred := func(t corev1.NodeSelectorTerm) func(*corev1.PodSpec) {
		return func(p *corev1.PodSpec) {
			p.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1, Preference: t}}}}
		}
	}
	claimSpec := func(class *string) corev1.PersistentVolumeClaimSpec {
		return corev1.PersistentVolumeClaimSpec{StorageClassName: class,
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi")}}}
	}
	scratch := func(class *string) func(*corev1.PodSpec) {
		return func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{
				Ephemeral: &corev1.EphemeralVolumeSource{VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
					Spec: claimSpec(class)}}}}}
		}
	}
	create := func(obj client.Object) error {
		err := k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })
		}
		return err
	}
	refused := func(err error) {
		GinkgoHelper()
		Expect(apierrors.IsForbidden(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring(reason + ": "))
		Expect(err.Error()).To(ContainSubstring("the zone " + here))
	}

	BeforeAll(func() {
		helm, err := exec.LookPath("helm")
		if err != nil {
			if os.Getenv("CI") != "" {
				Fail("helm is not on the PATH")
			}
			Skip("helm is not on the PATH")
		}
		for _, ns := range []string{inNS, outNS} {
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx,
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))).To(Succeed())
		}
		// These versions are from before the policy. They have a placement in a different zone.
		for _, ns := range []string{inNS, outNS} {
			name := before
			if ns == outNS {
				name = beforeOut
			}
			Expect(create(newVersion(ns, name, selector(zoneKey, far)))).To(Succeed())
		}
		chart := filepath.Join("..", "..", "charts", "koodam")
		out, err := exec.Command(helm, "template", "zone-test", chart, "--namespace", "zone-test-system", //nolint:gosec
			"-f", filepath.Join(chart, "values-bundled.yaml"), "--set", "apps.enabled=true",
			"--set", "zone.name="+here, "--set", "zone.namespaces={"+inNS+"}",
			"--set", "zone.storageClasses={"+nearClass+"}").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		applied := 0
		for doc := range bytes.SplitSeq(out, []byte("\n---\n")) {
			u := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(doc, &u.Object)).To(Succeed())
			if !strings.HasPrefix(u.GetKind(), "ValidatingAdmissionPolicy") || !strings.Contains(u.GetName(), "-zone-") {
				continue
			}
			Expect(k8sClient.Create(ctx, u)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, u))).To(Succeed()) })
			applied++
		}
		Expect(applied).To(Equal(4), "the chart has 2 policies and 2 bindings for the zone")
		// The API server starts to enforce a new policy after a short time.
		probe := func(obj client.Object) func() error {
			return func() error {
				err := k8sClient.Create(ctx, obj)
				if err == nil {
					_ = k8sClient.Delete(ctx, obj)
				}
				return err
			}
		}
		Eventually(probe(newVersion(inNS, "placed-probe", selector(zoneKey, far))), time.Minute, time.Second).
			ShouldNot(Succeed())
		Eventually(probe(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim-probe", Namespace: inNS},
			Spec: claimSpec(new(farClass))}), time.Minute, time.Second).ShouldNot(Succeed())
	})

	It("accepts a pod template with no placement or with a placement in the zone", func() {
		Expect(create(newVersion(inNS, "placed-none", nil))).To(Succeed())
		Expect(create(newVersion(inNS, "placed-zone", selector(zoneKey, here)))).To(Succeed())
		Expect(create(newVersion(inNS, "placed-other-key", selector("disk", far)))).To(Succeed())
		Expect(create(newVersion(inNS, "placed-required", required(term(zoneKey, corev1.NodeSelectorOpIn, here),
			term("disk", corev1.NodeSelectorOpNotIn, far))))).To(Succeed())
		Expect(create(newVersion(inNS, "placed-preferred", preferred(term(regionKey, corev1.NodeSelectorOpIn, here))))).
			To(Succeed())
	})

	It("refuses a node selector for a different zone or region", func() {
		for _, key := range []string{zoneKey, regionKey, oldZoneKey} {
			refused(create(newVersion(inNS, "placed-far", selector(key, far))))
		}
	})

	It("refuses a node affinity for a different zone", func() {
		for name, change := range map[string]func(*corev1.PodSpec){
			"a different zone":         required(term(zoneKey, corev1.NodeSelectorOpIn, far)),
			"the zone and a different": required(term(zoneKey, corev1.NodeSelectorOpIn, here, far)),
			"a second term":            required(term(zoneKey, corev1.NodeSelectorOpIn, here), term(regionKey, corev1.NodeSelectorOpIn, far)),
			"not in":                   required(term(zoneKey, corev1.NodeSelectorOpNotIn, far)),
			"exists":                   required(term(zoneKey, corev1.NodeSelectorOpExists)),
			"preferred":                preferred(term(zoneKey, corev1.NodeSelectorOpIn, far)),
			"preferred not in":         preferred(term(oldZoneKey, corev1.NodeSelectorOpNotIn, here)),
		} {
			By(name)
			refused(create(newVersion(inNS, "placed-far", change)))
		}
	})

	It("refuses a volume claim with a storage class that is not of the zone", func() {
		Expect(create(newVersion(inNS, "claim-near", scratch(new(nearClass))))).To(Succeed())
		refused(create(newVersion(inNS, "claim-far", scratch(new(farClass)))))
		refused(create(newVersion(inNS, "claim-none", scratch(nil))))
		claim := func(name string, class *string) *corev1.PersistentVolumeClaim {
			return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: inNS},
				Spec: claimSpec(class)}
		}
		Expect(create(claim("data-near", new(nearClass)))).To(Succeed())
		refused(create(claim("data-far", new(farClass))))
		refused(create(claim("data-none", nil)))
		refused(create(claim("data-empty", new(""))))
		// The home claim of a Workspace has the storage class that the controller gets from the chart.
		home := func(name, class string) *corev1.PersistentVolumeClaim {
			r := &WorkspaceReconciler{Image: testWorkspaceImage, StorageClass: class}
			ws := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: inNS}}
			c := r.statefulSet(ws, testProfile).Spec.VolumeClaimTemplates[0]
			c.Name, c.Namespace = name, inNS
			return &c
		}
		Expect(create(home("home-near", nearClass))).To(Succeed())
		refused(create(home("home-none", "")))
	})

	It("does not apply to a namespace of a different install", func() {
		Expect(create(newVersion(outNS, "placed-far", selector(zoneKey, far)))).To(Succeed())
		Expect(create(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-far", Namespace: outNS},
			Spec: claimSpec(new(farClass))})).To(Succeed())
	})

	It("lets a version from before the policy change only if its template stays the same", func() {
		av := &corev1alpha1.AppVersion{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: inNS, Name: before}, av)).To(Succeed())
		av.Labels = map[string]string{"example.com/team": "ledger"}
		Expect(k8sClient.Update(ctx, av)).To(Succeed())
		av.Spec.Template.PodTemplate.Spec.Containers[0].Image = "registry.example.com/zone:v2"
		refused(k8sClient.Update(ctx, av))
	})
})
