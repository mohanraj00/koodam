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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// The CRD validation of spec.gate and status.evidence of an AppVersion. These tests do not run a
// controller. They only send objects to the API server. The objects hold no key value.
var _ = Describe("AppVersion gate CRD", func() {
	ctx := context.Background()
	ns := metav1.NamespaceDefault
	const (
		gateImage = "registry.example.com/shop-gate@sha256:" +
			"1111111111111111111111111111111111111111111111111111111111111111"
		gateRoot = "/project"
		gateFile = "gates/release.json"
		digest   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
		gateID   = "gate-20261119-030000-0a1b2c3d"
	)

	newVersion := func(name string, gate *corev1alpha1.AppVersionGate) *corev1alpha1.AppVersion {
		return &corev1alpha1.AppVersion{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.AppVersionSpec{
				AppRef: corev1alpha1.AppReference{Name: "shop"},
				Gate:   gate,
				Template: corev1alpha1.AppVersionTemplate{PodTemplate: corev1alpha1.PodTemplate{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:  corev1alpha1.AppContainerName,
						Image: "registry.example.com/app@" + digest,
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("http")},
						}},
					}}},
				}},
			},
		}
	}
	newGate := func() *corev1alpha1.AppVersionGate {
		return &corev1alpha1.AppVersionGate{Nookku: corev1alpha1.NookkuGate{
			Image: gateImage, Root: gateRoot, GateFile: gateFile,
		}}
	}
	create := func(obj *corev1alpha1.AppVersion) error {
		err := k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })
		}
		return err
	}
	expectInvalid := func(err error, message string) {
		GinkgoHelper()
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(message))
	}
	newRecord := func() *corev1alpha1.EvidenceRecord {
		return &corev1alpha1.EvidenceRecord{
			Source:        corev1alpha1.EvidenceSourceNookkuGate,
			Subject:       "koodam.dev/appversion/v1:default/v:uid:" + digest,
			Verdict:       corev1alpha1.EvidenceFail,
			ID:            gateID,
			Digest:        digest,
			Integrity:     corev1alpha1.EvidenceSeal,
			NookkuVersion: "0.5.9",
			CheckedAt:     metav1.Now().Rfc3339Copy(),
		}
	}

	It("accepts a gate and defaults the deadline", func() {
		v := newVersion("gate-valid", newGate())
		Expect(create(v)).To(Succeed())
		Expect(v.Spec.Gate.Nookku.DeadlineSeconds).To(HaveValue(Equal(int32(1800))))
	})

	It("accepts a version with no gate", func() {
		v := newVersion("gate-none", nil)
		Expect(create(v)).To(Succeed())
		Expect(v.Spec.Gate).To(BeNil())
	})

	It("refuses a gate with a path that is not safe", func() {
		for name, change := range map[string]func(*corev1alpha1.NookkuGate){
			"a relative root":       func(g *corev1alpha1.NookkuGate) { g.Root = "project" },
			"a root with ..":        func(g *corev1alpha1.NookkuGate) { g.Root = "/project/../etc" },
			"a root with a space":   func(g *corev1alpha1.NookkuGate) { g.Root = "/my project" },
			"an absolute gate file": func(g *corev1alpha1.NookkuGate) { g.GateFile = "/gates/release.json" },
			"a gate file with ..":   func(g *corev1alpha1.NookkuGate) { g.GateFile = "gates/../../x.json" },
			"a gate file flag":      func(g *corev1alpha1.NookkuGate) { g.GateFile = "--root=/x" },
			"no image":              func(g *corev1alpha1.NookkuGate) { g.Image = "" },
			"a deadline of 0":       func(g *corev1alpha1.NookkuGate) { g.DeadlineSeconds = new(int32(0)) },
		} {
			gate := newGate()
			change(&gate.Nookku)
			err := create(newVersion("gate-bad", gate))
			Expect(err).To(HaveOccurred(), name)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s: the error is not Invalid: %v", name, err)
		}
	})

	It("refuses a change, a removal and a late add of the gate", func() {
		v := newVersion("gate-fixed", newGate())
		Expect(create(v)).To(Succeed())
		v.Spec.Gate.Nookku.GateFile = "gates/other.json"
		expectInvalid(k8sClient.Update(ctx, v), "spec.gate is immutable.")

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())
		v.Spec.Gate = nil
		expectInvalid(k8sClient.Update(ctx, v), "spec.gate is immutable.")

		plain := newVersion("gate-late", nil)
		Expect(create(plain)).To(Succeed())
		plain.Spec.Gate = newGate()
		expectInvalid(k8sClient.Update(ctx, plain), "spec.gate is immutable.")
	})

	It("writes status.evidence one time", func() {
		v := newVersion("gate-record", newGate())
		Expect(create(v)).To(Succeed())
		v.Status.Evidence = newRecord()
		Expect(k8sClient.Status().Update(ctx, v)).To(Succeed())

		// A status update that keeps the record passes.
		v.Status.KeyAlias = "alias"
		Expect(k8sClient.Status().Update(ctx, v)).To(Succeed())

		v.Status.Evidence.Verdict = corev1alpha1.EvidencePass
		expectInvalid(k8sClient.Status().Update(ctx, v), "status.evidence is written one time.")

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())
		v.Status.Evidence = nil
		expectInvalid(k8sClient.Status().Update(ctx, v), "status.evidence is written one time.")
	})

	It("refuses a record with a field that is not valid", func() {
		for name, change := range map[string]func(*corev1alpha1.EvidenceRecord){
			"a source":    func(r *corev1alpha1.EvidenceRecord) { r.Source = "user" },
			"a verdict":   func(r *corev1alpha1.EvidenceRecord) { r.Verdict = "maybe" },
			"a digest":    func(r *corev1alpha1.EvidenceRecord) { r.Digest = "sha256:abc" },
			"a subject":   func(r *corev1alpha1.EvidenceRecord) { r.Subject = strings.Repeat("s", 513) },
			"an id":       func(r *corev1alpha1.EvidenceRecord) { r.ID = "" },
			"integrity":   func(r *corev1alpha1.EvidenceRecord) { r.Integrity = "none" },
			"the version": func(r *corev1alpha1.EvidenceRecord) { r.NookkuVersion = strings.Repeat("9", 65) },
		} {
			v := newVersion("gate-bad-record", newGate())
			Expect(create(v)).To(Succeed())
			v.Status.Evidence = newRecord()
			change(v.Status.Evidence)
			err := k8sClient.Status().Update(ctx, v)
			Expect(err).To(HaveOccurred(), name)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s: the error is not Invalid: %v", name, err)
			Expect(k8sClient.Delete(ctx, v)).To(Succeed())
		}
	})
})
