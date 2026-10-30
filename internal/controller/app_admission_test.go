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

// These tests prove 2 API server rules for #183. They do not prove the full App or AppVersion
// model, because that model does not exist yet.
//  1. Full-template immutability. The tests copy the shared PodTemplate schema from the generated
//     Workspace CRD in memory, with no field removed, and add the CEL rule "self == oldSelf".
//     The API server accepts this schema. It refuses each template change at admission, with no
//     controller. It accepts a replicas change and an unchanged large template.
//  2. The deletion-time finalizer fence. After deletion starts, the API server refuses a new
//     finalizer from an update, a merge patch or a swap. A stale resourceVersion gets a conflict.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("App admission: full PodTemplate immutability and the deletion-time finalizer fence", Ordered, func() {
	ctx := context.Background()
	// The test group and kind are only in this test. No controller watches them.
	gvk := schema.GroupVersionKind{Group: "admission.test.koodam.dev", Version: "v1alpha1", Kind: "TemplateHolder"}
	const immutableMessage = "spec.template is immutable."
	const fenceMessage = "no new finalizers can be added if the object is being deleted"
	const ownerFinalizer = "admission.test.koodam.dev/owner"
	// These are the schema field names and the fixture values that the tests use more than one time.
	const (
		objectType    = "object"
		specField     = "spec"
		templateField = "template"
		teamLabel     = "team"
		poolLabel     = "pool"
		appsPool      = "apps"
		otherAccount  = "other"
	)
	var c client.Client

	BeforeAll(func() {
		raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", "core.koodam.dev_workspaces.yaml"))
		Expect(err).NotTo(HaveOccurred())
		workspace := &apiextensionsv1.CustomResourceDefinition{}
		Expect(yaml.Unmarshal(raw, workspace)).To(Succeed())
		Expect(workspace.Spec.Versions).To(HaveLen(1))
		generated := workspace.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties[specField].Properties["podTemplate"]
		template := generated.DeepCopy()

		// The copy has each json field of corev1.PodSpec. The test removes no field.
		podSpec := template.Properties[specField].Properties
		podSpecType := reflect.TypeFor[corev1.PodSpec]()
		for field := range podSpecType.Fields() {
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			Expect(podSpec).To(HaveKey(name), "the copied schema has no PodSpec field %q", name)
		}
		// The copy keeps the generated rules, for example the host restrictions.
		Expect(template.XValidations).NotTo(BeEmpty())
		Expect(template.XValidations).To(Equal(generated.XValidations))

		// This is the rule that the approval asks for: one equality check on the full template.
		template.XValidations = append(template.XValidations, apiextensionsv1.ValidationRule{
			Rule: "self == oldSelf", Message: immutableMessage})

		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "templateholders.admission.test.koodam.dev"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: gvk.Group, Scope: apiextensionsv1.NamespaceScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: gvk.Kind, ListKind: gvk.Kind + "List",
					Plural: "templateholders", Singular: "templateholder"},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name: gvk.Version, Served: true, Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						// The root requires spec, and spec requires template. Without these, an update can
						// remove the template, and a later update can add a different one with no oldSelf.
						Type: objectType, Required: []string{specField},
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"apiVersion": {Type: "string"}, "kind": {Type: "string"}, "metadata": {Type: objectType},
							specField: {Type: objectType, Required: []string{templateField},
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									templateField: *template,
									"replicas":    {Type: "integer", Format: "int32"},
								}},
						},
					}},
				}},
			},
		}
		_, err = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
			CRDs: []*apiextensionsv1.CustomResourceDefinition{crd}, MaxTime: 30 * time.Second})
		Expect(err).NotTo(HaveOccurred(), "the API server refused the full-template equality rule")

		// This client has no cache. Each read goes to the API server.
		c, err = client.New(cfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())
	})

	toMap := func(t *corev1alpha1.PodTemplate) map[string]any {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(t)
		Expect(err).NotTo(HaveOccurred())
		return m
	}
	baseTemplate := func() *corev1alpha1.PodTemplate {
		return &corev1alpha1.PodTemplate{
			Metadata: corev1alpha1.PodTemplateMetadata{Labels: map[string]string{teamLabel: "a"}},
			Spec: corev1.PodSpec{
				RuntimeClassName: new("gvisor"),
				NodeSelector:     map[string]string{poolLabel: appsPool},
				Tolerations:      []corev1.Toleration{{Key: "k", Operator: corev1.TolerationOpExists}},
				Volumes: []corev1.Volume{{Name: "data",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				Containers: []corev1.Container{{
					Name: "app", Image: "registry.example/app@sha256:" + strings.Repeat("a", 64),
					Command: []string{"serve"}, Env: []corev1.EnvVar{{Name: "MODE", Value: "prod"}},
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("http")}}},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}},
			},
		}
	}
	newHolder := func(name string, t *corev1alpha1.PodTemplate, finalizers ...string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		u.SetName(name)
		u.SetNamespace(metav1.NamespaceDefault)
		u.SetFinalizers(finalizers)
		u.Object[specField] = map[string]any{templateField: toMap(t), "replicas": int64(1)}
		return u
	}
	get := func(name string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		Expect(c.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: name}, u)).To(Succeed())
		return u
	}
	create := func(u *unstructured.Unstructured) {
		Expect(c.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() {
			cur := &unstructured.Unstructured{}
			cur.SetGroupVersionKind(gvk)
			if err := c.Get(ctx, client.ObjectKeyFromObject(u), cur); apierrors.IsNotFound(err) {
				return
			}
			cur.SetFinalizers(nil)
			Expect(client.IgnoreNotFound(c.Update(ctx, cur))).To(Succeed())
			Expect(client.IgnoreNotFound(c.Delete(ctx, cur))).To(Succeed())
		})
	}
	storedTemplate := func(name string) any {
		return get(name).Object[specField].(map[string]any)[templateField]
	}
	expectImmutable := func(err error) {
		GinkgoWriter.Printf("Template change result: %v\n", err)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
		Expect(err.Error()).To(ContainSubstring(immutableMessage))
	}
	expectFenced := func(step string, err error) {
		GinkgoWriter.Printf("%s: %v\n", step, err)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s: the error is not Invalid: %v", step, err)
		Expect(err.Error()).To(ContainSubstring(fenceMessage))
	}
	expectConflict := func(step string, err error) {
		GinkgoWriter.Printf("%s: %v\n", step, err)
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "%s: the error is not Conflict: %v", step, err)
	}

	Context("full-template immutability", func() {
		DescribeTable("refuses a template change at admission, with no controller",
			func(name string, change func(*corev1alpha1.PodTemplate)) {
				create(newHolder(name, baseTemplate()))
				before := storedTemplate(name)
				u := get(name)
				t := baseTemplate()
				change(t)
				u.Object[specField].(map[string]any)[templateField] = toMap(t)
				expectImmutable(c.Update(ctx, u))
				// The API server keeps the old template. No controller reverts it.
				Expect(storedTemplate(name)).To(Equal(before))
			},
			Entry("container image", "change-image", func(t *corev1alpha1.PodTemplate) {
				t.Spec.Containers[0].Image = "registry.example/app@sha256:" + strings.Repeat("b", 64)
			}),
			Entry("environment value", "change-env", func(t *corev1alpha1.PodTemplate) {
				t.Spec.Containers[0].Env[0].Value = "dev"
			}),
			Entry("readiness probe", "change-probe", func(t *corev1alpha1.PodTemplate) {
				t.Spec.Containers[0].ReadinessProbe.HTTPGet.Path = "/other"
			}),
			Entry("resource limit", "change-limit", func(t *corev1alpha1.PodTemplate) {
				t.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse("4")
			}),
			Entry("template label", "change-label", func(t *corev1alpha1.PodTemplate) {
				t.Metadata.Labels[teamLabel] = "b"
			}),
			Entry("added container", "add-container", func(t *corev1alpha1.PodTemplate) {
				t.Spec.Containers = append(t.Spec.Containers, corev1.Container{Name: "side", Image: "side"})
			}),
			Entry("removed tolerations", "remove-tolerations", func(t *corev1alpha1.PodTemplate) {
				t.Spec.Tolerations = nil
			}),
			Entry("removed runtime class", "remove-runtime-class", func(t *corev1alpha1.PodTemplate) {
				t.Spec.RuntimeClassName = nil
			}),
			Entry("DNS policy", "change-dns", func(t *corev1alpha1.PodTemplate) {
				t.Spec.DNSPolicy = corev1.DNSNone
			}),
			Entry("added scheduling gate", "add-scheduling-gate", func(t *corev1alpha1.PodTemplate) {
				t.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "g"}}
			}),
		)

		It("refuses a template change from a merge patch", func() {
			create(newHolder("patch-node-selector", baseTemplate()))
			before := storedTemplate("patch-node-selector")
			patch := []byte(`{"spec":{"template":{"spec":{"nodeSelector":{"pool":"other"}}}}}`)
			expectImmutable(c.Patch(ctx, get("patch-node-selector"), client.RawPatch(types.MergePatchType, patch)))
			Expect(storedTemplate("patch-node-selector")).To(Equal(before))
		})

		DescribeTable("refuses the removal of spec or the template, and then refuses a different template",
			func(name, field string, remove func(*unstructured.Unstructured) error) {
				create(newHolder(name, baseTemplate()))
				before := storedTemplate(name)
				err := remove(get(name))
				GinkgoWriter.Printf("Removal result: %v\n", err)
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
				Expect(err.Error()).To(ContainSubstring(field + ": Required value"))
				Expect(storedTemplate(name)).To(Equal(before))

				// The template is still there, so oldSelf is present. A different template is refused.
				u := get(name)
				t := baseTemplate()
				t.Spec.Containers[0].Image = "registry.example/app@sha256:" + strings.Repeat("c", 64)
				u.Object[specField].(map[string]any)[templateField] = toMap(t)
				expectImmutable(c.Update(ctx, u))
				Expect(storedTemplate(name)).To(Equal(before))
			},
			Entry("merge patch spec null", "null-spec", specField, func(u *unstructured.Unstructured) error {
				return c.Patch(ctx, u, client.RawPatch(types.MergePatchType, []byte(`{"spec":null}`)))
			}),
			Entry("JSON patch remove spec", "json-remove-spec", specField, func(u *unstructured.Unstructured) error {
				return c.Patch(ctx, u, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/spec"}]`)))
			}),
			Entry("update with no spec", "update-no-spec", specField, func(u *unstructured.Unstructured) error {
				delete(u.Object, specField)
				return c.Update(ctx, u)
			}),
			Entry("merge patch template null", "null-template", specField+"."+templateField, func(u *unstructured.Unstructured) error {
				return c.Patch(ctx, u, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"template":null}}`)))
			}),
		)

		It("accepts a replicas change and a metadata change when the template does not change", func() {
			create(newHolder("change-replicas", baseTemplate()))
			before := storedTemplate("change-replicas")
			u := get("change-replicas")
			u.Object[specField].(map[string]any)["replicas"] = int64(3)
			u.SetLabels(map[string]string{"x": "y"})
			Expect(c.Update(ctx, u)).To(Succeed())
			after := get("change-replicas")
			Expect(after.Object[specField].(map[string]any)["replicas"]).To(BeEquivalentTo(3))
			Expect(after.Object[specField].(map[string]any)[templateField]).To(Equal(before))
		})

		It("accepts a replicas change on a large unchanged template, and refuses one changed value in it", func() {
			t := baseTemplate()
			for i := range 40 {
				ctr := t.Spec.Containers[0].DeepCopy()
				ctr.Name = "c" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
				for j := range 50 {
					ctr.Env = append(ctr.Env, corev1.EnvVar{Name: "E" + string(rune('A'+j%26)) + string(rune('A'+j/26)),
						Value: strings.Repeat("v", 200)})
				}
				t.Spec.Containers = append(t.Spec.Containers, *ctr)
			}
			create(newHolder("large-template", t))
			u := get("large-template")
			u.Object[specField].(map[string]any)["replicas"] = int64(2)
			Expect(c.Update(ctx, u)).To(Succeed(), "an unchanged large template must pass the CEL cost limit")

			u = get("large-template")
			t.Spec.Containers[40].Env[49].Value = "changed"
			u.Object[specField].(map[string]any)[templateField] = toMap(t)
			expectImmutable(c.Update(ctx, u))
		})
	})

	DescribeTable("keeps the generated host restrictions on the copied template",
		func(name, message string, change func(*corev1alpha1.PodTemplate)) {
			t := baseTemplate()
			change(t)
			err := c.Create(ctx, newHolder(name, t))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
			Expect(err.Error()).To(ContainSubstring(message))
		},
		Entry("host network", "host-network", "cannot use the host network",
			func(t *corev1alpha1.PodTemplate) { t.Spec.HostNetwork = true }),
		Entry("host PID", "host-pid", "cannot use the host network",
			func(t *corev1alpha1.PodTemplate) { t.Spec.HostPID = true }),
		Entry("host IPC", "host-ipc", "cannot use the host network",
			func(t *corev1alpha1.PodTemplate) { t.Spec.HostIPC = true }),
		Entry("shared process namespace", "share-process", "cannot use the host network",
			func(t *corev1alpha1.PodTemplate) { t.Spec.ShareProcessNamespace = new(true) }),
		Entry("service account name", "service-account", "Koodam owns serviceAccountName",
			func(t *corev1alpha1.PodTemplate) { t.Spec.ServiceAccountName = otherAccount }),
	)

	Context("the deletion-time finalizer fence", func() {
		It("refuses a new finalizer after deletion starts, and refuses a stale resourceVersion", func() {
			create(newHolder("fence", baseTemplate(), ownerFinalizer))
			enroll := "admission.test.koodam.dev/version-" + string(get("fence").GetUID())

			// Before deletion: a stale resourceVersion gets a conflict.
			stale := get("fence")
			other := get("fence")
			other.SetLabels(map[string]string{"bump": "1"})
			Expect(c.Update(ctx, other)).To(Succeed())
			stale.SetFinalizers(append(stale.GetFinalizers(), enroll))
			expectConflict("Stale enrollment before deletion", c.Update(ctx, stale))

			// Keep a copy from before deletion starts.
			beforeDelete := get("fence")
			Expect(c.Delete(ctx, get("fence"))).To(Succeed())
			deleting := get("fence")
			Expect(deleting.GetDeletionTimestamp()).NotTo(BeNil())
			Expect(deleting.GetFinalizers()).To(Equal([]string{ownerFinalizer}))

			// An update with the current resourceVersion cannot add a different finalizer.
			deleting.SetFinalizers(append(deleting.GetFinalizers(), enroll))
			expectFenced("Enrollment after deletion, current resourceVersion", c.Update(ctx, deleting))

			// An update cannot replace the existing finalizer with a different finalizer.
			deleting = get("fence")
			deleting.SetFinalizers([]string{enroll})
			expectFenced("Finalizer swap after deletion", c.Update(ctx, deleting))

			// An update with a stale resourceVersion from before deletion gets a conflict.
			beforeDelete.SetFinalizers(append(beforeDelete.GetFinalizers(), enroll))
			expectConflict("Enrollment after deletion, stale resourceVersion", c.Update(ctx, beforeDelete))

			// A merge patch has no resourceVersion. It cannot add the finalizer either.
			patch := []byte(`{"metadata":{"finalizers":["` + ownerFinalizer + `","` + enroll + `"]}}`)
			expectFenced("Enrollment after deletion, merge patch",
				c.Patch(ctx, get("fence"), client.RawPatch(types.MergePatchType, patch)))

			Expect(get("fence").GetFinalizers()).To(Equal([]string{ownerFinalizer}))
		})
	})
})
