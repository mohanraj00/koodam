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
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

// These specs apply the Hold CRD and config/hold/admission/policy.yaml to the envtest API
// server. They run no controller. A second admin user shows that the policy compares the user
// fields with the user of the request.
const (
	holdOtherAdmin = "hold-other-admin"
	holdDone       = "The investigation is complete."
)

var _ = Describe("Hold CRD and admission policy", Ordered, func() {
	ctx := context.Background()
	var admin string
	var other client.Client

	newHold := func(name string, change func(*corev1beta1.Hold)) *corev1beta1.Hold {
		h := &corev1beta1.Hold{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1beta1.HoldSpec{
				Target: corev1beta1.HoldTarget{Kind: corev1beta1.HoldTargetAgent, Namespace: "team-a", Name: "job-7"},
				Action: corev1beta1.HoldStop, Reason: "The run sends data to an unknown host.", User: admin,
			}}
		if change != nil {
			change(h)
		}
		return h
	}
	// release ends the Hold as the admin, so that the delete of the cleanup passes the policy.
	release := func(name string) {
		h := &corev1beta1.Hold{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, h); apierrors.IsNotFound(err) {
			return
		}
		if h.Spec.Release == nil {
			h.Spec.Release = &corev1beta1.HoldRelease{User: admin, Reason: "test cleanup"}
			Expect(k8sClient.Update(ctx, h)).To(Succeed())
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, h))).To(Succeed())
	}
	create := func(c client.Client, h *corev1beta1.Hold) error {
		err := c.Create(ctx, h)
		if err == nil {
			DeferCleanup(release, h.Name)
		}
		return err
	}
	refused := func(err error, text string) {
		GinkgoHelper()
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err) || apierrors.IsForbidden(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring(text))
	}

	BeforeAll(func() {
		review := &authenticationv1.SelfSubjectReview{}
		Expect(k8sClient.Create(ctx, review)).To(Succeed())
		admin = review.Status.UserInfo.Username
		Expect(admin).NotTo(BeEmpty())

		user, err := testEnv.AddUser(envtest.User{Name: holdOtherAdmin, Groups: []string{"system:masters"}}, nil)
		Expect(err).NotTo(HaveOccurred())
		other, err = client.New(user.Config(), client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		src, err := os.ReadFile(filepath.Join("..", "..", "config", "hold", "admission", "policy.yaml"))
		Expect(err).NotTo(HaveOccurred())
		for doc := range bytes.SplitSeq(src, []byte("\n---\n")) {
			u := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(doc, &u.Object)).To(Succeed())
			Expect(k8sClient.Create(ctx, u)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, u))).To(Succeed()) })
		}
		// The API server starts to enforce a new policy after a short time.
		Eventually(func() error {
			probe := newHold("hold-probe", func(h *corev1beta1.Hold) { h.Spec.User = "someone-else" })
			err := k8sClient.Create(ctx, probe)
			if err == nil {
				h := &corev1beta1.Hold{}
				Expect(k8sClient.Get(ctx, client.ObjectKey{Name: probe.Name}, h)).To(Succeed())
				h.Spec.Release = &corev1beta1.HoldRelease{User: admin, Reason: "probe"}
				Expect(k8sClient.Update(ctx, h)).To(Succeed())
				Expect(k8sClient.Delete(ctx, h)).To(Succeed())
			}
			return err
		}, time.Minute, time.Second).ShouldNot(Succeed())
	})

	It("needs a namespace for each target kind other than Profile", func() {
		const text = "target.namespace is necessary"
		refused(create(k8sClient, newHold("hold-ns-a", func(h *corev1beta1.Hold) { h.Spec.Target.Namespace = "" })), text)
		refused(create(k8sClient, newHold("hold-ns-b", func(h *corev1beta1.Hold) {
			h.Spec.Target = corev1beta1.HoldTarget{Kind: corev1beta1.HoldTargetProfile, Namespace: "team-a", Name: "team-profile"}
		})), text)
		Expect(create(k8sClient, newHold("hold-ns-c", func(h *corev1beta1.Hold) {
			h.Spec.Target = corev1beta1.HoldTarget{Kind: corev1beta1.HoldTargetProfile, Name: "team-profile"}
			h.Spec.Action = corev1beta1.HoldQuarantine
		}))).To(Succeed())
		for _, kind := range []corev1beta1.HoldTargetKind{corev1beta1.HoldTargetWorkspace,
			corev1beta1.HoldTargetApp, corev1beta1.HoldTargetAppVersion} {
			Expect(create(k8sClient, newHold("hold-ns-"+string(bytes.ToLower([]byte(kind))), func(h *corev1beta1.Hold) {
				h.Spec.Target.Kind = kind
			}))).To(Succeed())
		}
	})

	It("refuses an unknown action, an unknown kind and an empty reason", func() {
		refused(create(k8sClient, newHold("hold-bad-a", func(h *corev1beta1.Hold) { h.Spec.Action = "Pause" })), "spec.action")
		refused(create(k8sClient, newHold("hold-bad-b", func(h *corev1beta1.Hold) { h.Spec.Target.Kind = "Pod" })), "spec.target.kind")
		refused(create(k8sClient, newHold("hold-bad-c", func(h *corev1beta1.Hold) { h.Spec.Reason = "" })), "spec.reason")
	})

	It("keeps the target, the action, the reason and the user immutable", func() {
		h := newHold("hold-immutable", nil)
		Expect(create(k8sClient, h)).To(Succeed())
		for text, change := range map[string]func(*corev1beta1.Hold){
			"spec.target is immutable": func(h *corev1beta1.Hold) { h.Spec.Target.Name = "job-8" },
			"spec.action is immutable": func(h *corev1beta1.Hold) { h.Spec.Action = corev1beta1.HoldQuarantine },
			"spec.reason is immutable": func(h *corev1beta1.Hold) { h.Spec.Reason = "A different reason." },
			"spec.user is immutable":   func(h *corev1beta1.Hold) { h.Spec.User = "someone-else" },
		} {
			got := &corev1beta1.Hold{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(h), got)).To(Succeed())
			change(got)
			refused(k8sClient.Update(ctx, got), text)
		}
	})

	It("refuses a user that is not the user of the request", func() {
		refused(create(other, newHold("hold-user-a", nil)), "spec.user must be the user of the request")
		Expect(create(other, newHold("hold-user-b", func(h *corev1beta1.Hold) { h.Spec.User = holdOtherAdmin }))).To(Succeed())
	})

	It("refuses a release on a create and a release with a different user", func() {
		refused(create(k8sClient, newHold("hold-release-a", func(h *corev1beta1.Hold) {
			h.Spec.Release = &corev1beta1.HoldRelease{User: admin, Reason: "early"}
		})), "A new Hold cannot have spec.release")

		h := newHold("hold-release-b", nil)
		Expect(create(k8sClient, h)).To(Succeed())
		got := &corev1beta1.Hold{}
		Expect(other.Get(ctx, client.ObjectKeyFromObject(h), got)).To(Succeed())
		got.Spec.Release = &corev1beta1.HoldRelease{User: admin, Reason: holdDone}
		refused(other.Update(ctx, got), "spec.release.user must be the user of the request")

		// Another admin can release the Hold with the own name.
		got.Spec.Release.User = holdOtherAdmin
		Expect(other.Update(ctx, got)).To(Succeed())
	})

	It("keeps the release immutable after it is set", func() {
		h := newHold("hold-release-c", nil)
		Expect(create(k8sClient, h)).To(Succeed())
		got := &corev1beta1.Hold{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(h), got)).To(Succeed())
		got.Spec.Release = &corev1beta1.HoldRelease{User: admin, Reason: holdDone}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		got.Spec.Release.Reason = "A different reason."
		refused(k8sClient.Update(ctx, got), "spec.release is immutable")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(h), got)).To(Succeed())
		got.Spec.Release = nil
		refused(k8sClient.Update(ctx, got), "spec.release is immutable")
	})

	It("refuses the delete of a Hold with no release", func() {
		h := newHold("hold-delete", nil)
		Expect(create(k8sClient, h)).To(Succeed())
		refused(k8sClient.Delete(ctx, h), "Release the Hold before the delete")

		got := &corev1beta1.Hold{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(h), got)).To(Succeed())
		got.Spec.Release = &corev1beta1.HoldRelease{User: admin, Reason: holdDone}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
	})
})
