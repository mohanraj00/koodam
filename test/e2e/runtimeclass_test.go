//go:build e2e
// +build e2e

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

package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// rcProfile binds rcNS. The admission policy applies only to rcNS.
	rcProfile = "koodam-e2e-rc"
	rcNS      = "koodam-e2e-rc"
	// rcClass uses the runc handler of kind. kind has no gVisor, but the path from the pod
	// template to the kubelet is the same.
	rcClass = "e2e-runc"
	// rcPolicy requires rcClass on each Workspace pod of rcNS.
	rcPolicy = "koodam-e2e-runtime-class"
)

var _ = Describe("Runtime class", Ordered, Label("workspace"), func() {
	workspace := func(name, class string) *corev1alpha1.Workspace {
		ws := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: rcNS},
			Spec: corev1alpha1.WorkspaceSpec{Image: workspaceImage, Storage: resource.MustParse("1Gi")}}
		if class != "" {
			ws.Spec.PodTemplate = &corev1alpha1.PodTemplate{Spec: corev1.PodSpec{RuntimeClassName: &class}}
		}
		return ws
	}
	// expectRefused waits for the FailedCreate event of the StatefulSet of ws. Then it checks
	// that the Workspace is Pending and has no pod.
	expectRefused := func(ws *corev1alpha1.Workspace, text string) {
		Eventually(func() []string {
			l := &corev1.EventList{}
			_ = k8s.List(ctx, l, client.InNamespace(rcNS))
			var msgs []string
			for _, e := range l.Items {
				if e.InvolvedObject.Kind == "StatefulSet" && e.InvolvedObject.Name == ws.Name && e.Reason == "FailedCreate" {
					msgs = append(msgs, e.Message)
				}
			}
			return msgs
		}, 2*time.Minute, 2*time.Second).Should(ContainElement(ContainSubstring(text)))
		waitPhase(ws, corev1alpha1.WorkspacePending)
		err := k8s.Get(ctx, client.ObjectKey{Namespace: rcNS, Name: ws.Name + "-0"}, &corev1.Pod{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "The API server made a refused pod.")
	}

	BeforeAll(func() {
		policy := &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: rcPolicy},
			Spec: admissionv1.ValidatingAdmissionPolicySpec{
				FailurePolicy: new(admissionv1.Fail),
				MatchConstraints: &admissionv1.MatchResources{ResourceRules: []admissionv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionv1.RuleWithOperations{
						Operations: []admissionv1.OperationType{admissionv1.Create},
						Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"},
							Resources: []string{"pods"}}}}}},
				MatchConditions: []admissionv1.MatchCondition{{Name: "workspace-pod",
					Expression: "has(object.metadata.labels) && '" + wsLabel + "' in object.metadata.labels"}},
				Validations: []admissionv1.Validation{{
					Expression: "has(object.spec.runtimeClassName) && object.spec.runtimeClassName == '" + rcClass + "'",
					Message:    "A Koodam pod must use the runtime class " + rcClass + "."}},
			}}
		binding := &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: rcPolicy},
			Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: rcPolicy,
				ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
				MatchResources: &admissionv1.MatchResources{NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{corev1alpha1.ProfileLabel: rcProfile}}}}}
		objects := []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: rcNS,
				Labels: map[string]string{corev1alpha1.ProfileLabel: rcProfile}}},
			&corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: rcProfile},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{rcNS}}},
			&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: rcClass}, Handler: "runc"},
			policy, binding,
		}
		DeferCleanup(func() {
			_ = k8s.DeleteAllOf(ctx, &corev1alpha1.Workspace{}, client.InNamespace(rcNS))
			Eventually(func() int {
				l := &corev1alpha1.WorkspaceList{}
				if err := k8s.List(ctx, l, client.InNamespace(rcNS)); err != nil {
					return -1
				}
				return len(l.Items)
			}, 2*time.Minute, time.Second).Should(BeZero(), "The finalizer kept a Workspace.")
			for _, o := range objects {
				_ = k8s.Delete(ctx, o)
			}
			Eventually(func() bool {
				return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKey{Name: rcNS}, &corev1.Namespace{}))
			}, 5*time.Minute, 2*time.Second).Should(BeTrue(), "The namespace "+rcNS+" was not deleted.")
		})
		for _, o := range objects {
			Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, o))).To(Succeed())
		}
		// The API server loads a new policy after some seconds. A dry run of a pod without the
		// class shows when the policy refuses pods.
		probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: rcNS,
			Labels: map[string]string{wsLabel: "probe"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "probe", Image: workspaceImage}}}}
		Eventually(func() error { return k8s.Create(ctx, probe.DeepCopy(), client.DryRunAll) },
			time.Minute, time.Second).Should(MatchError(ContainSubstring("must use the runtime class")))
	})

	AfterEach(func() { dumpWorkspaces() })

	It("runs the Workspace pod with the runtime class of the pod template", func() {
		ws := workspace("isolated", rcClass)
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		waitPhase(ws, corev1alpha1.WorkspaceRunning)
		pod := &corev1.Pod{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: rcNS, Name: ws.Name + "-0"}, pod)).To(Succeed())
		Expect(pod.Spec.RuntimeClassName).To(HaveValue(Equal(rcClass)))
		// The next specs do not need this pod. Delete it to release the node memory.
		Expect(k8s.Delete(ctx, ws)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKey{Namespace: rcNS, Name: ws.Name + "-0"}, &corev1.Pod{}))
		}, 2*time.Minute, time.Second).Should(BeTrue(), "The pod was not deleted.")
	})

	It("makes no pod for a runtime class that does not exist", func() {
		ws := workspace("missing", "e2e-missing")
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		// The RuntimeClass plugin of the API server refuses the pod before the policy does.
		expectRefused(ws, `RuntimeClass "e2e-missing" not found`)
	})

	It("makes no pod without the runtime class that the admission policy requires", func() {
		ws := workspace("plain", "")
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		expectRefused(ws, "must use the runtime class "+rcClass)
	})
})
