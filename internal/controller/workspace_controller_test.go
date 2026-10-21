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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("Workspace Controller", func() {
	const ns = "default"
	var (
		ctx  context.Context
		logs *bytes.Buffer
		name string
		n    int
	)
	// The keys are public key lines with fake key data. They are not real keys.
	key1 := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnlyWorkspace0001 dev@laptop"
	key2 := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnlyWorkspace0002 dev@desktop"

	nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
	r := func() *WorkspaceReconciler {
		return &WorkspaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
	}
	reconcileOnce := func() {
		_, err := r().Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
		Expect(err).NotTo(HaveOccurred())
	}
	workspace := func() *corev1alpha1.Workspace {
		ws := &corev1alpha1.Workspace{}
		Expect(k8sClient.Get(ctx, nn(name), ws)).To(Succeed())
		return ws
	}
	get := func(o client.Object, s string) {
		Expect(k8sClient.Get(ctx, nn(s), o)).To(Succeed())
	}
	// cleanup deletes o at the end of the test. envtest has no garbage collector.
	cleanup := func(o client.Object) {
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(context.Background(), o))).To(Succeed()) })
	}

	BeforeEach(func() {
		n++
		name = fmt.Sprintf("ws-%d", n)
		logs = &bytes.Buffer{}
		// Verbosity 1 keeps the request bodies of the test client (level 8) out of the log.
		ctx = logr.NewContext(context.Background(), funcr.New(func(p, a string) {
			logs.WriteString(p + " " + a + "\n")
		}, funcr.Options{Verbosity: 1}))
		ws := &corev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       corev1alpha1.WorkspaceSpec{SSHPublicKeys: []string{key1}},
		}
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		cleanup(ws)
		cleanup(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-ssh-host", Namespace: ns}})
		cleanup(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-ssh", Namespace: ns}})
	})

	It("makes the host key Secret and the authorized_keys ConfigMap", func() {
		reconcileOnce()
		ws := workspace()

		s := &corev1.Secret{}
		get(s, name+"-ssh-host")
		Expect(string(s.Data[hostKeyField])).To(HavePrefix("-----BEGIN OPENSSH PRIVATE KEY-----"))
		Expect(ws.Status.HostKey).To(HavePrefix("ssh-ed25519 "))
		Expect(ws.Status.HostKey).To(Equal(string(s.Data[hostPubField])))
		Expect(s.Immutable).To(HaveValue(BeTrue()))

		cm := &corev1.ConfigMap{}
		get(cm, name+"-ssh")
		Expect(cm.Data).To(Equal(map[string]string{authorizedKeysField: key1 + "\n"}))

		for _, o := range []metav1.Object{s, cm} {
			Expect(metav1.IsControlledBy(o, ws)).To(BeTrue(), o.GetName())
			Expect(o.GetLabels()).To(HaveKeyWithValue(WorkspaceLabel, name))
		}

		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspacePending))
		Expect(meta.IsStatusConditionFalse(ws.Status.Conditions, condReady)).To(BeTrue())
	})

	It("does not change the host key on a second reconcile", func() {
		reconcileOnce()
		before := &corev1.Secret{}
		get(before, name+"-ssh-host")
		hostKey := workspace().Status.HostKey

		reconcileOnce()
		after := &corev1.Secret{}
		get(after, name+"-ssh-host")
		Expect(after.UID).To(Equal(before.UID))
		Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))
		Expect(after.Data).To(Equal(before.Data))
		Expect(workspace().Status.HostKey).To(Equal(hostKey))
	})

	It("updates the ConfigMap when the keys change", func() {
		reconcileOnce()
		ws := workspace()
		ws.Spec.SSHPublicKeys = []string{key1, key2}
		Expect(k8sClient.Update(ctx, ws)).To(Succeed())
		reconcileOnce()

		cm := &corev1.ConfigMap{}
		get(cm, name+"-ssh")
		Expect(cm.Data[authorizedKeysField]).To(Equal(key1 + "\n" + key2 + "\n"))
	})

	It("is Suspended when spec.suspend is true", func() {
		reconcileOnce()
		ws := workspace()
		ws.Spec.Suspend = true
		Expect(k8sClient.Update(ctx, ws)).To(Succeed())
		reconcileOnce()
		ws = workspace()
		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspaceSuspended))
		Expect(meta.IsStatusConditionFalse(ws.Status.Conditions, condReady)).To(BeTrue())
	})

	It("fails and keeps an object that is not from the Workspace", func() {
		other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-ssh", Namespace: ns},
			Data: map[string]string{"x": "y"}}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		reconcileOnce()

		ws := workspace()
		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspaceFailed))
		Expect(meta.FindStatusCondition(ws.Status.Conditions, condReady).Reason).To(Equal("NameConflict"))
		get(other, name+"-ssh")
		Expect(other.Data).To(Equal(map[string]string{"x": "y"}))
	})

	It("writes no private key text to the status, the events or the log", func() {
		reconcileOnce()
		reconcileOnce()
		s := &corev1.Secret{}
		get(s, name+"-ssh-host")
		private := string(s.Data[hostKeyField])
		// The base64 body of the PEM block. A part of it is enough to find a leak.
		body := strings.Split(private, "\n")[1]
		Expect(body).NotTo(BeEmpty())

		status, err := json.Marshal(workspace().Status)
		Expect(err).NotTo(HaveOccurred())
		events := &corev1.EventList{}
		Expect(k8sClient.List(ctx, events, client.InNamespace(ns))).To(Succeed())
		ev, err := json.Marshal(events)
		Expect(err).NotTo(HaveOccurred())
		Expect(logs.String()).To(ContainSubstring("Made the SSH host key"))
		for _, text := range []string{string(status), string(ev), logs.String()} {
			Expect(text).NotTo(ContainSubstring("PRIVATE KEY"))
			Expect(text).NotTo(ContainSubstring(body))
		}
	})
})

var _ = Describe("newHostKey", func() {
	It("makes a key that ssh-keygen reads", func() {
		private, public, err := newHostKey()
		Expect(err).NotTo(HaveOccurred())
		Expect(public).To(MatchRegexp(`^ssh-ed25519 [A-Za-z0-9+/]+=*$`))
		keygen, err := exec.LookPath("ssh-keygen")
		if err != nil {
			Skip("ssh-keygen is not installed")
		}
		file := filepath.Join(GinkgoT().TempDir(), "key")
		Expect(os.WriteFile(file, private, 0o600)).To(Succeed())
		out, err := exec.Command(keygen, "-y", "-f", file).Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(out))).To(Equal(public))
	})
})
