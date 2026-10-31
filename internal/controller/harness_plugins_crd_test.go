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

// These tests are for spec.harness.plugins and spec.harnesses[].plugins of #83. This step adds
// API validation only. No controller reads the plugins, and Koodam installs and mounts nothing
// for them. The tests only send objects to the API server. The API server does not look up the
// PVC of an Agent. It only checks the volume declaration in the pod template.

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("Harness plugins CRD", func() {
	ctx := context.Background()
	const (
		testsVolume  = "nookku-tests"
		testsClaim   = "hp-user-tests"
		pluginsPath  = ".plugins"
		pvcMessage   = "The nookku plugin needs the volume nookku-tests in spec.podTemplate.spec.volumes, with a persistentVolumeClaim and a claimName."
		agentImage   = "hp-img"
		agentCommand = "hp-run"
		agentModel   = "hp-model"
	)
	// A plugin name outside the nookku enum.
	const unsupportedPlugin corev1alpha1.HarnessPlugin = "hp-unsupported-plugin"
	nookku := []corev1alpha1.HarnessPlugin{corev1alpha1.HarnessPluginNookku}
	harnessNames := []string{claudeName, codexName}

	create := func(o client.Object) error {
		err := k8sClient.Create(ctx, o)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed()) })
		}
		return err
	}
	expectInvalid := func(err error, text string) {
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(text))
	}
	pvcVolume := func(name, claim string) corev1.Volume {
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}
	}
	volumes := func(v ...corev1.Volume) *corev1alpha1.PodTemplate {
		return &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{Volumes: v}}
	}
	agent := func(name, harness string, plugins []corev1alpha1.HarnessPlugin,
		t *corev1alpha1.PodTemplate) *corev1alpha1.Agent {
		return &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: corev1alpha1.AgentSpec{Image: agentImage, Command: []string{agentCommand}, Model: agentModel,
				Budget: resource.MustParse("1"), PodTemplate: t,
				Harness: &corev1alpha1.HarnessSpec{Name: harness, Plugins: plugins}}}
	}
	workspace := func(name string, h ...corev1alpha1.HarnessSpec) *corev1alpha1.Workspace {
		return &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: corev1alpha1.WorkspaceSpec{Harnesses: h}}
	}
	getAgent := func(name string) *corev1alpha1.Agent {
		a := &corev1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: name}, a)).To(Succeed())
		return a
	}
	mergePatch := func(o client.Object, body string) error {
		return k8sClient.Patch(ctx, o, client.RawPatch(types.MergePatchType, []byte(body)))
	}

	// envtest installs the generated CRDs with the default CEL cost limits of the API server. The
	// API server refuses a CRD with a rule over these limits, so the suite does not start. This
	// test shows that the installed Agent CRD has the rule.
	It("has the volume rule in the Agent CRD that the API server installed", func() {
		crd := &unstructured.Unstructured{}
		crd.SetAPIVersion("apiextensions.k8s.io/v1")
		crd.SetKind("CustomResourceDefinition")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "agents.core.koodam.dev"}, crd)).To(Succeed())
		versions, _, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(HaveLen(1))
		rules, _, err := unstructured.NestedSlice(versions[0].(map[string]any),
			"schema", "openAPIV3Schema", "properties", "spec", "x-kubernetes-validations")
		Expect(err).NotTo(HaveOccurred())
		Expect(rules).To(ContainElement(HaveKeyWithValue("message", pvcMessage)))
	})

	It("accepts a legacy Agent with no plugins and no PVC, and an empty plugins list", func() {
		Expect(create(&corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "hp-no-harness",
			Namespace: metav1.NamespaceDefault}, Spec: corev1alpha1.AgentSpec{Image: agentImage,
			Command: []string{agentCommand}, Model: agentModel, Budget: resource.MustParse("1")}})).To(Succeed())
		for _, h := range harnessNames {
			a := agent("hp-legacy-"+h, h, nil, nil)
			Expect(create(a)).To(Succeed())
			Expect(mergePatch(a, `{"spec":{"harness":{"plugins":[]}}}`)).To(Succeed())
			Expect(getAgent(a.Name).Spec.Harness.Plugins).To(BeEmpty())
		}
	})

	It("accepts nookku with a user PVC declaration for each harness name", func() {
		for _, h := range harnessNames {
			a := agent("hp-valid-"+h, h, nookku, volumes(pvcVolume("other", "x"), pvcVolume(testsVolume, testsClaim)))
			Expect(create(a)).To(Succeed())
			Expect(getAgent(a.Name).Spec.Harness.Plugins).To(Equal(nookku))
		}
	})

	DescribeTable("refuses nookku on an Agent without a valid test volume",
		func(name string, t *corev1alpha1.PodTemplate) {
			for _, h := range harnessNames {
				expectInvalid(create(agent(name+"-"+h, h, nookku, t)), pvcMessage)
			}
		},
		Entry("no pod template", "hp-no-template", nil),
		Entry("no volumes", "hp-no-volumes", &corev1alpha1.PodTemplate{}),
		Entry("a wrong volume name", "hp-wrong-name", volumes(pvcVolume("tests", testsClaim))),
		Entry("an emptyDir source", "hp-empty-dir", volumes(corev1.Volume{Name: testsVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})),
		Entry("a configMap source", "hp-config-map", volumes(corev1.Volume{Name: testsVolume,
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "hp-tests"}}}})),
		Entry("an empty claimName", "hp-empty-claim", volumes(pvcVolume(testsVolume, ""))),
	)

	It("refuses an unknown or a duplicate plugin on an Agent", func() {
		t := volumes(pvcVolume(testsVolume, testsClaim))
		expectInvalid(create(agent("hp-unknown", codexName,
			[]corev1alpha1.HarnessPlugin{unsupportedPlugin}, t)), pluginsPath)
		expectInvalid(create(agent("hp-duplicate", codexName,
			[]corev1alpha1.HarnessPlugin{corev1alpha1.HarnessPluginNookku, corev1alpha1.HarnessPluginNookku}, t)),
			pluginsPath)
	})

	It("refuses an update that enables nookku without the PVC, or removes the PVC while nookku is on", func() {
		Expect(create(agent("hp-enable", codexName, nil, nil))).To(Succeed())
		a := getAgent("hp-enable")
		a.Spec.Harness.Plugins = nookku
		expectInvalid(k8sClient.Update(ctx, a), pvcMessage)
		Expect(getAgent("hp-enable").Spec.Harness.Plugins).To(BeEmpty())

		Expect(create(agent("hp-remove", claudeName, nookku, volumes(pvcVolume(testsVolume, testsClaim))))).To(Succeed())
		a = getAgent("hp-remove")
		a.Spec.PodTemplate.Spec.Volumes = nil
		expectInvalid(k8sClient.Update(ctx, a), pvcMessage)
		a = getAgent("hp-remove")
		a.Spec.PodTemplate.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = ""
		expectInvalid(k8sClient.Update(ctx, a), pvcMessage)

		// The removal of the plugin and the volume together is valid.
		a = getAgent("hp-remove")
		a.Spec.Harness.Plugins = nil
		a.Spec.PodTemplate.Spec.Volumes = nil
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
	})

	It("accepts omitted, empty and nookku plugins on each Workspace harness, with no test volume", func() {
		Expect(create(workspace("hp-ws-none", corev1alpha1.HarnessSpec{Name: claudeName},
			corev1alpha1.HarnessSpec{Name: codexName}))).To(Succeed())
		Expect(create(workspace("hp-ws-codex", corev1alpha1.HarnessSpec{Name: claudeName},
			corev1alpha1.HarnessSpec{Name: codexName, Plugins: nookku}))).To(Succeed())
		ws := workspace("hp-ws-both", corev1alpha1.HarnessSpec{Name: claudeName, Plugins: nookku},
			corev1alpha1.HarnessSpec{Name: codexName, Plugins: nookku})
		Expect(create(ws)).To(Succeed())
		Expect(ws.Spec.PodTemplate).To(BeNil())
		Expect(mergePatch(ws, `{"spec":{"harnesses":[{"name":"claude-code","plugins":[]}]}}`)).To(Succeed())
	})

	It("refuses an unknown or a duplicate plugin on each Workspace harness", func() {
		expectInvalid(create(workspace("hp-ws-unknown", corev1alpha1.HarnessSpec{Name: claudeName, Plugins: nookku},
			corev1alpha1.HarnessSpec{Name: codexName, Plugins: []corev1alpha1.HarnessPlugin{unsupportedPlugin}})), pluginsPath)
		expectInvalid(create(workspace("hp-ws-duplicate", corev1alpha1.HarnessSpec{Name: claudeName,
			Plugins: []corev1alpha1.HarnessPlugin{corev1alpha1.HarnessPluginNookku, corev1alpha1.HarnessPluginNookku}})),
			pluginsPath)
	})
})
