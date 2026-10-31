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

// These tests are for spec.ingressPort of #147. This part adds only the API and the two generated
// CRD variants. The suite installs the default Agent CRD, which refuses each Agent that sets
// spec.ingressPort. No controller runs, and no test uses a gateway key.
//
// No supported command installs the enabled variant. The proof tests install a copy of it with a
// different kind, AgentIngressProof. This copy is a schema proof only. It is not the real Agent and
// it has no admission webhook, so these tests make no claim about the reserved port name http. A
// later part of #147 adds the webhook and its tests.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("Agent ingress port", func() {
	ctx := context.Background()
	const (
		disabledMsg = "spec.ingressPort is disabled"
		immutable   = "spec.ingressPort is immutable"
		portField   = "ingressPort"
		specKey     = "spec"
		imageKey    = "image"
		image       = "img"
		runCommand  = "ingress-run"
		noteKey     = "ingress-note"
		otherImage  = "ingress-other"
		modeKey     = "koodam.dev/agent-ingress-mode"
		agentKind   = "Agent"
		proofKind   = "AgentIngressProof"
		labelPatch  = `{"metadata":{"labels":{"team":"a"}}}`
		addPort     = `{"spec":{"ingressPort":8080}}`
		offName     = "ing-off"
		addName     = "ing-add"
		removeName  = "ing-remove"
		sameName    = "ing-same"
		absentName  = "ing-same-absent"
	)

	// obj returns an object with the required spec fields. A nil port leaves out the field.
	obj := func(kind, name string, port *int64) *unstructured.Unstructured {
		spec := map[string]any{imageKey: image, "command": []any{runCommand}, "model": "m", "budget": "1"}
		if port != nil {
			spec[portField] = *port
		}
		u := &unstructured.Unstructured{Object: map[string]any{specKey: spec}}
		u.SetGroupVersionKind(corev1alpha1.GroupVersion.WithKind(kind))
		u.SetName(name)
		u.SetNamespace(metav1.NamespaceDefault)
		return u
	}
	create := func(c client.Client, o client.Object) error {
		err := c.Create(ctx, o)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(c.Delete(ctx, o))).To(Succeed()) })
		}
		return err
	}
	expectInvalid := func(err error, text string) {
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(text))
	}
	// get reads the stored object as JSON fields, so a test can see if a field is absent.
	get := func(c client.Client, kind, name string) *unstructured.Unstructured {
		u := obj(kind, name, nil)
		Expect(c.Get(ctx, client.ObjectKeyFromObject(u), u)).To(Succeed())
		return u
	}
	spec := func(c client.Client, kind, name string) map[string]any {
		return get(c, kind, name).Object[specKey].(map[string]any)
	}
	patch := func(c client.Client, kind, name, body string) error {
		return c.Patch(ctx, obj(kind, name, nil), client.RawPatch(types.MergePatchType, []byte(body)))
	}

	Context("the default Agent CRD", func() {
		typedGet := func(name string) *corev1alpha1.Agent {
			a := &corev1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: name}, a)).
				To(Succeed())
			return a
		}

		It("is the disabled variant with the gate", func() {
			crd := &unstructured.Unstructured{}
			crd.SetAPIVersion("apiextensions.k8s.io/v1")
			crd.SetKind("CustomResourceDefinition")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "agents.core.koodam.dev"}, crd)).To(Succeed())
			Expect(crd.GetAnnotations()).To(HaveKeyWithValue(modeKey, "disabled"))
			versions, _, err := unstructured.NestedSlice(crd.Object, specKey, "versions")
			Expect(err).NotTo(HaveOccurred())
			Expect(versions).To(HaveLen(1))
			rules, _, err := unstructured.NestedSlice(versions[0].(map[string]any),
				"schema", "openAPIV3Schema", "properties", specKey, "x-kubernetes-validations")
			Expect(err).NotTo(HaveOccurred())
			Expect(rules).To(ContainElement(HaveKeyWithValue("rule", "!has(self.ingressPort)")))
		})

		It("accepts an Agent with no port and its metadata updates, and adds no default", func() {
			Expect(create(k8sClient, obj(agentKind, offName, nil))).To(Succeed())
			Expect(spec(k8sClient, agentKind, offName)).NotTo(HaveKey(portField))
			Expect(patch(k8sClient, agentKind, offName, labelPatch)).To(Succeed())

			a := typedGet(offName)
			Expect(a.Spec.IngressPort).To(BeNil())
			a.Annotations = map[string]string{noteKey: "x"}
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			Expect(spec(k8sClient, agentKind, offName)).NotTo(HaveKey(portField))
		})

		It("refuses each Agent that sets the port, and the addition of a port", func() {
			for _, p := range []int64{1, 8080, 65535} {
				expectInvalid(create(k8sClient, obj(agentKind, fmt.Sprintf("ing-on-%d", p), &p)), disabledMsg)
			}
			for _, p := range []int64{0, 65536} {
				expectInvalid(create(k8sClient, obj(agentKind, fmt.Sprintf("ing-bad-%d", p), &p)), "spec.ingressPort")
			}
			typed := &corev1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "ing-typed", Namespace: metav1.NamespaceDefault},
				Spec: corev1alpha1.AgentSpec{Image: image, Command: []string{runCommand}, Model: "m",
					Budget: resource.MustParse("1"), IngressPort: new(int32(8080))}}
			expectInvalid(create(k8sClient, typed), disabledMsg)

			Expect(create(k8sClient, obj(agentKind, addName, nil))).To(Succeed())
			expectInvalid(patch(k8sClient, agentKind, addName, addPort), disabledMsg)
			a := typedGet(addName)
			a.Spec.IngressPort = new(int32(8080))
			expectInvalid(k8sClient.Update(ctx, a), disabledMsg)
			Expect(spec(k8sClient, agentKind, addName)).NotTo(HaveKey(portField))
		})
	})

	// This context is a schema proof only. See the comment at the top of the file.
	Context("the enabled schema, in a proof copy", Ordered, func() {
		var c client.Client

		BeforeAll(func() {
			b, err := os.ReadFile(filepath.Join("..", "..", "config", "agent-ingress", "crd", "bases",
				"core.koodam.dev_agents.yaml"))
			Expect(err).NotTo(HaveOccurred())
			crd := &apiextensionsv1.CustomResourceDefinition{}
			Expect(yaml.UnmarshalStrict(b, crd)).To(Succeed())
			Expect(crd.Annotations).To(HaveKeyWithValue(modeKey, "enabled"))
			crd.Name = "agentingressproofs.core.koodam.dev"
			crd.Spec.Names = apiextensionsv1.CustomResourceDefinitionNames{Kind: proofKind, ListKind: proofKind + "List",
				Plural: "agentingressproofs", Singular: "agentingressproof"}

			// The API server checks the CEL cost of the copy with the default limits.
			opts := envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{crd}}
			_, err = envtest.InstallCRDs(cfg, opts)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { Expect(envtest.UninstallCRDs(cfg, opts)).To(Succeed()) })
			// A new client discovers the new kind.
			c, err = client.New(cfg, client.Options{})
			Expect(err).NotTo(HaveOccurred())
		})

		It("accepts only the port range 1 to 65535 or an absent port, and adds no default", func() {
			Expect(create(c, obj(proofKind, "min", new(int64(1))))).To(Succeed())
			Expect(create(c, obj(proofKind, "max", new(int64(65535))))).To(Succeed())
			Expect(create(c, obj(proofKind, "absent", nil))).To(Succeed())
			Expect(spec(c, proofKind, "max")).To(HaveKeyWithValue(portField, BeEquivalentTo(65535)))
			Expect(spec(c, proofKind, "absent")).NotTo(HaveKey(portField))
			for i, p := range []int64{0, -1, 65536} {
				expectInvalid(create(c, obj(proofKind, fmt.Sprintf("bad-%d", i), &p)), "spec.ingressPort")
			}
		})

		It("accepts a harmless update when the port does not change", func() {
			Expect(create(c, obj(proofKind, sameName, new(int64(8080))))).To(Succeed())
			Expect(create(c, obj(proofKind, absentName, nil))).To(Succeed())
			for _, name := range []string{sameName, absentName} {
				Expect(patch(c, proofKind, name, labelPatch)).To(Succeed())
				Expect(patch(c, proofKind, name, `{"spec":{"user":"team-a"}}`)).To(Succeed())
				u := get(c, proofKind, name)
				u.SetAnnotations(map[string]string{noteKey: "x"})
				Expect(c.Update(ctx, u)).To(Succeed())
			}
			Expect(spec(c, proofKind, sameName)).To(HaveKeyWithValue(portField, BeEquivalentTo(8080)))
			Expect(spec(c, proofKind, absentName)).NotTo(HaveKey(portField))
		})

		It("refuses an addition, a removal and a change of the port after create", func() {
			Expect(create(c, obj(proofKind, addName, nil))).To(Succeed())
			expectInvalid(patch(c, proofKind, addName, addPort), immutable)
			Expect(spec(c, proofKind, addName)).NotTo(HaveKey(portField))

			Expect(create(c, obj(proofKind, removeName, new(int64(8080))))).To(Succeed())
			expectInvalid(patch(c, proofKind, removeName, `{"spec":{"ingressPort":null}}`), immutable)
			expectInvalid(patch(c, proofKind, removeName, `{"spec":{"ingressPort":9090}}`), immutable)
			u := get(c, proofKind, removeName)
			unstructured.RemoveNestedField(u.Object, specKey, portField)
			expectInvalid(c.Update(ctx, u), immutable)
			Expect(spec(c, proofKind, removeName)).To(HaveKeyWithValue(portField, BeEquivalentTo(8080)))
		})

		It("keeps the shared pod template rules when the port is set", func() {
			withTemplate := func(name string, tmpl map[string]any) *unstructured.Unstructured {
				u := obj(proofKind, name, new(int64(8080)))
				Expect(unstructured.SetNestedField(u.Object, map[string]any{specKey: tmpl}, specKey, "podTemplate")).
					To(Succeed())
				return u
			}
			ephemeral := map[string]any{"ephemeralContainers": []any{map[string]any{"name": "debug", imageKey: image}}}
			expectInvalid(create(c, withTemplate("ing-ephemeral", ephemeral)), "ephemeralContainers")
			mainImage := map[string]any{"containers": []any{map[string]any{"name": agentContainer, imageKey: otherImage}}}
			expectInvalid(create(c, withTemplate("ing-main-image", mainImage)), "Set the image and the command")
		})
	})
})
