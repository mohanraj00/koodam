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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

// The CRD validation of the Workspace. There is no Workspace controller yet, so these tests only
// send objects to the API server.
var _ = Describe("Workspace CRD", func() {
	ctx := context.Background()

	// testKey is a public key line with a fake key data. It is not a real key.
	testKey := func(i int) string {
		return fmt.Sprintf("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnlyTestOnlyTestOnly%04d dev%d@laptop", i, i)
	}

	// newWorkspace returns a valid Workspace. The caller changes it before the create.
	newWorkspace := func(name string) *corev1alpha1.Workspace {
		return &corev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: corev1alpha1.WorkspaceSpec{
				SSHPublicKeys: []string{
					testKey(1),
					"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY=",
					"sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29t yubikey",
				},
				Harnesses: []corev1alpha1.HarnessSpec{{Name: "claude-code"}, {Name: "codex"}},
				Gateway: &corev1alpha1.WorkspaceGateway{
					Model:        "claude-haiku-5-5",
					BudgetPerDay: resource.MustParse("5"),
				},
			},
		}
	}

	// create makes the Workspace and deletes it at the end of the test.
	create := func(ws *corev1alpha1.Workspace) error {
		err := k8sClient.Create(ctx, ws)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, ws))).To(Succeed()) })
		}
		return err
	}

	// expectInvalid checks that the API server refused the object.
	expectInvalid := func(err error) {
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "the error is not Invalid: %v", err)
	}

	It("accepts a valid Workspace and sets the default storage", func() {
		Expect(create(newWorkspace("ws-valid"))).To(Succeed())

		got := &corev1alpha1.Workspace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ws-valid", Namespace: "default"}, got)).To(Succeed())
		Expect(got.Spec.Storage.Cmp(resource.MustParse("20Gi"))).To(Equal(0))
		Expect(got.Spec.Harnesses[0].Libc).To(Equal("glibc"))
	})

	It("refuses a private key in sshPublicKeys", func() {
		ws := newWorkspace("ws-private-key")
		ws.Spec.SSHPublicKeys = []string{"-----BEGIN OPENSSH PRIVATE KEY-----"}
		expectInvalid(create(ws))
	})

	It("refuses a public key with a newline", func() {
		ws := newWorkspace("ws-newline")
		ws.Spec.SSHPublicKeys = []string{testKey(1) + "\n" + testKey(2)}
		expectInvalid(create(ws))
	})

	It("refuses more than 16 public keys", func() {
		ws := newWorkspace("ws-many-keys")
		ws.Spec.SSHPublicKeys = nil
		for i := range 17 {
			ws.Spec.SSHPublicKeys = append(ws.Spec.SSHPublicKeys, testKey(i))
		}
		expectInvalid(create(ws))
	})

	It("refuses a duplicate harness name", func() {
		ws := newWorkspace("ws-dup-harness")
		ws.Spec.Harnesses = []corev1alpha1.HarnessSpec{{Name: "codex"}, {Name: "codex", Version: "1.2.3"}}
		expectInvalid(create(ws))
	})

	It("refuses a phase that is not in the enum", func() {
		ws := newWorkspace("ws-bad-phase")
		Expect(create(ws)).To(Succeed())

		ws.Status.Phase = "Sleeping"
		expectInvalid(k8sClient.Status().Update(ctx, ws))
	})
})
