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
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

var _ = Describe("Workspace Controller", func() {
	const ns = "default"
	const image = "ghcr.io/mohanraj00/koodam-workspace:test"
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
		return &WorkspaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, Image: image}
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
			Spec:       corev1alpha1.WorkspaceSpec{SSHPublicKeys: []string{key1}, Storage: resource.MustParse("5Gi")},
		}
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		cleanup(ws)
		cleanup(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-ssh-host", Namespace: ns}})
		cleanup(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-ssh", Namespace: ns}})
		cleanup(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
	})

	It("makes the 3 objects of the pod contract", func() {
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

		sts := &appsv1.StatefulSet{}
		get(sts, name)
		for _, o := range []metav1.Object{s, cm, sts} {
			Expect(metav1.IsControlledBy(o, ws)).To(BeTrue(), o.GetName())
			Expect(o.GetLabels()).To(HaveKeyWithValue(WorkspaceLabel, name))
		}
		Expect(sts.Spec.Replicas).To(HaveValue(Equal(int32(1))))
		Expect(sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
		Expect(sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled).To(Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))
		Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
		vct := sts.Spec.VolumeClaimTemplates[0]
		Expect(vct.Name).To(Equal("home"))
		Expect(vct.Spec.Resources.Requests[corev1.ResourceStorage]).To(Equal(resource.MustParse("5Gi")))

		pod := sts.Spec.Template.Spec
		Expect(pod.Containers).To(HaveLen(1))
		c := pod.Containers[0]
		Expect(c.Image).To(Equal(image))
		mounts := map[string]corev1.VolumeMount{}
		for _, m := range c.VolumeMounts {
			mounts[m.MountPath] = m
		}
		Expect(mounts).To(HaveKeyWithValue("/home/dev", corev1.VolumeMount{Name: "home", MountPath: "/home/dev"}))
		for _, p := range []string{"/etc/koodam/ssh", "/etc/koodam/globals", "/etc/koodam/login"} {
			Expect(mounts).To(HaveKey(p))
			Expect(mounts[p].ReadOnly).To(BeTrue(), p)
		}
		vols := map[string]corev1.Volume{}
		for _, v := range pod.Volumes {
			vols[v.Name] = v
		}
		src := vols[mounts["/etc/koodam/ssh"].Name].Projected.Sources
		Expect(src).To(HaveLen(2))
		Expect(src[0].ConfigMap.Name).To(Equal(name + "-ssh"))
		Expect(src[0].ConfigMap.Items).To(Equal([]corev1.KeyToPath{{Key: "authorized_keys", Path: "authorized_keys"}}))
		Expect(src[1].Secret.Name).To(Equal(name + "-ssh-host"))
		Expect(src[1].Secret.Items).To(Equal([]corev1.KeyToPath{{Key: "ssh_host_ed25519_key",
			Path: "host/ssh_host_ed25519_key", Mode: new(int32(0o440))}}))
		// The group of the secret files is the fsGroup, so sshd (uid 1000) can read them.
		Expect(pod.SecurityContext.FSGroup).To(HaveValue(Equal(int64(1000))))
		globals := vols[mounts["/etc/koodam/globals"].Name].ConfigMap
		Expect(globals.Name).To(Equal("koodam-globals"))
		Expect(globals.Optional).To(HaveValue(BeTrue()))
		login := vols[mounts["/etc/koodam/login"].Name].Secret
		Expect(login.SecretName).To(Equal("koodam-login"))
		Expect(login.Optional).To(HaveValue(BeTrue()))
		Expect(login.DefaultMode).To(HaveValue(Equal(int32(0o440))))

		Expect(c.Ports).To(ConsistOf(HaveField("ContainerPort", int32(2222))))
		Expect(c.ReadinessProbe.TCPSocket.Port.IntValue()).To(Equal(2222))

		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspacePending))
		Expect(ws.Status.Pod).To(Equal(name + "-0"))
		Expect(meta.IsStatusConditionFalse(ws.Status.Conditions, condReady)).To(BeTrue())
		Expect(meta.IsStatusConditionTrue(ws.Status.Conditions, condKeysValid)).To(BeTrue())
	})

	It("fails and keeps a host key Secret that is not from the Workspace", func() {
		other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-ssh-host", Namespace: ns},
			Data: map[string][]byte{"x": []byte("y")}}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		get(other, name+"-ssh-host")
		version := other.ResourceVersion
		reconcileOnce()

		ws := workspace()
		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspaceFailed))
		Expect(meta.FindStatusCondition(ws.Status.Conditions, condReady).Reason).To(Equal("NameConflict"))
		Expect(ws.Status.HostKey).To(BeEmpty())
		get(other, name+"-ssh-host")
		Expect(other.ResourceVersion).To(Equal(version))
		Expect(other.Data).To(Equal(map[string][]byte{"x": []byte("y")}))
	})

	It("meets the Pod Security restricted profile", func() {
		reconcileOnce()
		sts := &appsv1.StatefulSet{}
		get(sts, name)
		pod := sts.Spec.Template.Spec
		Expect(pod.AutomountServiceAccountToken).To(HaveValue(BeFalse()))
		psc := pod.SecurityContext
		Expect(psc.RunAsNonRoot).To(HaveValue(BeTrue()))
		Expect(psc.RunAsUser).To(HaveValue(Equal(int64(1000))))
		Expect(psc.FSGroup).To(HaveValue(Equal(int64(1000))))
		Expect(psc.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
		sc := pod.Containers[0].SecurityContext
		Expect(sc.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
		Expect(sc.Capabilities.Drop).To(Equal([]corev1.Capability{"ALL"}))
		Expect(sc.Capabilities.Add).To(BeEmpty())
		Expect(sc.RunAsNonRoot).To(HaveValue(BeTrue()))
		Expect(sc.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
		for _, v := range pod.Volumes {
			Expect(v.HostPath).To(BeNil())
		}
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

	It("updates the ConfigMap and not the pod template when the keys change", func() {
		reconcileOnce()
		before := &appsv1.StatefulSet{}
		get(before, name)

		ws := workspace()
		ws.Spec.SSHPublicKeys = []string{key1, key2}
		Expect(k8sClient.Update(ctx, ws)).To(Succeed())
		reconcileOnce()

		cm := &corev1.ConfigMap{}
		get(cm, name+"-ssh")
		Expect(cm.Data[authorizedKeysField]).To(Equal(key1 + "\n" + key2 + "\n"))
		after := &appsv1.StatefulSet{}
		get(after, name)
		Expect(after.Generation).To(Equal(before.Generation))
		Expect(after.Spec.Template).To(Equal(before.Spec.Template))
	})

	It("scales to 0 on suspend and is Running when the pod is ready", func() {
		reconcileOnce()
		ws := workspace()
		ws.Spec.Suspend = true
		Expect(k8sClient.Update(ctx, ws)).To(Succeed())
		reconcileOnce()

		sts := &appsv1.StatefulSet{}
		get(sts, name)
		Expect(sts.Spec.Replicas).To(HaveValue(Equal(int32(0))))
		Expect(workspace().Status.Phase).To(Equal(corev1alpha1.WorkspaceSuspended))

		ws = workspace()
		ws.Spec.Suspend = false
		Expect(k8sClient.Update(ctx, ws)).To(Succeed())
		reconcileOnce()
		get(sts, name)
		Expect(sts.Spec.Replicas).To(HaveValue(Equal(int32(1))))
		Expect(workspace().Status.Phase).To(Equal(corev1alpha1.WorkspacePending))

		// envtest has no StatefulSet controller, so the test writes the status. A ready replica
		// of an old generation does not give Running.
		sts.Status.Replicas, sts.Status.ReadyReplicas = 1, 1
		sts.Status.ObservedGeneration = sts.Generation - 1
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		reconcileOnce()
		Expect(workspace().Status.Phase).To(Equal(corev1alpha1.WorkspacePending))

		get(sts, name)
		sts.Status.ObservedGeneration = sts.Generation
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		reconcileOnce()
		ws = workspace()
		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspaceRunning))
		Expect(meta.IsStatusConditionTrue(ws.Status.Conditions, condReady)).To(BeTrue())
	})

	It("reverts a manual edit of the pod template", func() {
		reconcileOnce()
		sts := &appsv1.StatefulSet{}
		get(sts, name)
		want := sts.Spec.Template.DeepCopy()
		sts.Spec.Template.Spec.Containers[0].Image = "example.com/other:1"
		sts.Spec.Template.Spec.SecurityContext.RunAsNonRoot = new(false)
		Expect(k8sClient.Update(ctx, sts)).To(Succeed())

		reconcileOnce()
		get(sts, name)
		Expect(sts.Spec.Template).To(Equal(*want))
	})

	It("uses spec.image and the default storage of 20Gi", func() {
		ws := &corev1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-d", Namespace: ns},
			Spec:       corev1alpha1.WorkspaceSpec{Image: "example.com/my-workspace:2"},
		}
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		cleanup(ws)
		cleanup(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-d-ssh-host", Namespace: ns}})
		cleanup(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-d-ssh", Namespace: ns}})
		cleanup(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name + "-d", Namespace: ns}})
		_, err := r().Reconcile(ctx, reconcile.Request{NamespacedName: nn(name + "-d")})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		get(sts, name+"-d")
		Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("example.com/my-workspace:2"))
		Expect(sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]).
			To(Equal(resource.MustParse("20Gi")))
	})

	It("fails and keeps an object that is not from the Workspace", func() {
		other := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-ssh", Namespace: ns},
			Data: map[string]string{"x": "y"}}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		reconcileOnce()

		ws := workspace()
		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspaceFailed))
		Expect(meta.FindStatusCondition(ws.Status.Conditions, condReady).Reason).To(Equal("NameConflict"))
		Expect(ws.Status.Pod).To(BeEmpty())
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

var _ = Describe("validKeys", func() {
	It("skips a key that the CRD rule refuses and returns its index", func() {
		good := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnly dev@laptop"
		valid, skipped := validKeys([]string{
			good,
			`command="/bin/sh" ` + good,
			"ssh-ed25519 AAAA\nssh-rsa BBBB",
			"ssh-dss AAAAB3NzaC1kc3M=",
			"ssh-ed25519 " + strings.Repeat("A", maxKeyLength),
			"ssh-rsa AAAAB3NzaC1yc2E=",
		})
		Expect(valid).To(Equal([]string{good, "ssh-rsa AAAAB3NzaC1yc2E="}))
		Expect(skipped).To(Equal([]int{1, 2, 3, 4}))
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

// testProfile binds the namespace "default" for all the Workspace tests. The binding tests use
// other namespaces.
const testProfile = "ws-default"

var _ = BeforeEach(func() {
	ctx := context.Background()
	p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: testProfile},
		Spec: corev1alpha1.ProfileSpec{Namespaces: []string{metav1.NamespaceDefault}}}
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, p))).To(Succeed())
	ns := &corev1.Namespace{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: metav1.NamespaceDefault}, ns)).To(Succeed())
	if ns.Labels[corev1alpha1.ProfileLabel] != testProfile {
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		ns.Labels[corev1alpha1.ProfileLabel] = testProfile
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())
	}
})

var _ = Describe("Workspace Profile binding", func() {
	const name = "ws"
	var (
		ctx context.Context
		gw  *wsGateway
		rec *WorkspaceReconciler
		ns  string
		n   int
	)
	nn := func(s string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: s} }
	reconcileOnce := func() {
		_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
		Expect(err).NotTo(HaveOccurred())
	}
	workspace := func() *corev1alpha1.Workspace {
		ws := &corev1alpha1.Workspace{}
		Expect(k8sClient.Get(ctx, nn(name), ws)).To(Succeed())
		return ws
	}
	exists := func(o client.Object, s string) bool {
		err := k8sClient.Get(ctx, nn(s), o)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		return err == nil
	}
	replicas := func() int32 {
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn(name), sts)).To(Succeed())
		return *sts.Spec.Replicas
	}
	// setLabel sets the Profile label of the namespace to the Profile of the test, or removes it.
	setLabel := func(on bool) {
		o := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns}, o)).To(Succeed())
		o.Labels = map[string]string{}
		if on {
			o.Labels[corev1alpha1.ProfileLabel] = ns
		}
		Expect(k8sClient.Update(ctx, o)).To(Succeed())
	}
	// expectBound checks the condition ProfileBound, and the phase if the namespace is not bound.
	expectBound := func(want bool) {
		ws := workspace()
		c := meta.FindStatusCondition(ws.Status.Conditions, condProfileBound)
		Expect(c).NotTo(BeNil())
		if want {
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			return
		}
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal(reasonNotBound))
		Expect(c.Message).To(ContainSubstring(ns))
		Expect(c.Message).To(ContainSubstring(corev1alpha1.ProfileLabel))
		Expect(ws.Status.Phase).To(Equal(corev1alpha1.WorkspacePending))
		Expect(ws.Status.Pod).To(BeEmpty())
	}

	// setup makes a namespace and a Profile with the name of the namespace. If label is true, the
	// namespace has the Profile label. If list is true, the Profile lists the namespace. Then setup
	// makes a Workspace with a gateway key.
	setup := func(label, list bool) {
		n++
		ns = fmt.Sprintf("wsbind-%d", n)
		ctx = context.Background()
		gw = &wsGateway{keys: map[string]bool{}}
		rec = &WorkspaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient,
			Image: "img:test", Gateway: gw, GatewayURL: "http://gw.bind.test:4000", KeyTTL: time.Hour}
		o := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if label {
			o.Labels = map[string]string{corev1alpha1.ProfileLabel: ns}
		}
		Expect(k8sClient.Create(ctx, o)).To(Succeed())
		p := &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if list {
			p.Spec.Namespaces = []string{ns}
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		ws := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1alpha1.WorkspaceSpec{Storage: resource.MustParse("1Gi"),
				Gateway: &corev1alpha1.WorkspaceGateway{Model: "m", BudgetPerDay: resource.MustParse("1")}}}
		Expect(k8sClient.Create(ctx, ws)).To(Succeed())
		DeferCleanup(func() {
			bg := context.Background()
			w := &corev1alpha1.Workspace{}
			err := k8sClient.Get(bg, nn(name), w)
			Expect(client.IgnoreNotFound(err)).To(Succeed())
			if err == nil && controllerutil.RemoveFinalizer(w, workspaceFinalizer) {
				Expect(k8sClient.Update(bg, w)).To(Succeed())
			}
			// Delete the known object, because w has no name if the Get failed. envtest has no
			// namespace controller, so the namespace stays in deletion.
			known := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
			for _, d := range []client.Object{known, p, o} {
				Expect(client.IgnoreNotFound(k8sClient.Delete(bg, d))).To(Succeed())
			}
		})
	}

	DescribeTable("makes no object and no key if the namespace is not bound",
		func(label, list bool) {
			setup(label, list)
			reconcileOnce()
			expectBound(false)
			Expect(exists(&appsv1.StatefulSet{}, name)).To(BeFalse())
			Expect(exists(&corev1.ConfigMap{}, name+"-ssh")).To(BeFalse())
			Expect(exists(&corev1.Secret{}, name+"-ssh-host")).To(BeFalse())
			Expect(exists(&corev1.Secret{}, name+"-gateway")).To(BeFalse())
			Expect(gw.order).To(BeEmpty())
		},
		Entry("no label", false, false),
		Entry("a label, but the Profile does not list the namespace", true, false),
		Entry("the Profile lists the namespace, but no label", false, true),
	)

	It("blocks the key before it stops the pod when the binding goes, and runs again when it returns", func() {
		setup(true, true)
		reconcileOnce()
		expectBound(true)
		Expect(replicas()).To(Equal(int32(1)))
		Expect(gw.order).To(Equal([]string{opCreate}))
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, nn(name), sts)).To(Succeed())
		Expect(sts.Spec.Template.Labels).To(HaveKeyWithValue(UserLabel, ns))
		Expect(sts.Spec.Template.Labels).NotTo(HaveKey(RunLabel))
		Expect(sts.Spec.Selector.MatchLabels).NotTo(HaveKey(UserLabel), "the selector cannot change")
		sts.Status.Replicas, sts.Status.ReadyReplicas, sts.Status.ObservedGeneration = 1, 1, sts.Generation
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		reconcileOnce()
		Expect(workspace().Status.Phase).To(Equal(corev1alpha1.WorkspaceRunning))
		old := workspace().Status.KeyHash

		var replicasAtBlock int32
		var secretAtBlock bool
		gw.atBlock = func() { replicasAtBlock, secretAtBlock = replicas(), exists(&corev1.Secret{}, name+"-gateway") }
		setLabel(false)
		reconcileOnce()
		Expect(replicasAtBlock).To(Equal(int32(1)))
		Expect(secretAtBlock).To(BeTrue())
		Expect(replicas()).To(Equal(int32(0)))
		Expect(exists(&corev1.Secret{}, name+"-gateway")).To(BeFalse())
		Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage}))
		expectBound(false)
		Expect(workspace().Status.KeyHash).To(BeEmpty())
		// The home PVC stays after the scale to 0.
		Expect(k8sClient.Get(ctx, nn(name), sts)).To(Succeed())
		Expect(sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled).To(
			Equal(appsv1.RetainPersistentVolumeClaimRetentionPolicyType))

		setLabel(true)
		reconcileOnce()
		expectBound(true)
		Expect(replicas()).To(Equal(int32(1)))
		Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage, opCreate}))
		Expect(workspace().Status.KeyHash).NotTo(BeEmpty())
		Expect(workspace().Status.KeyHash).NotTo(Equal(old))
	})

	It("blocks the key before it stops the pod when the Profile no longer lists the namespace", func() {
		setup(true, true)
		reconcileOnce()
		expectBound(true)
		var replicasAtBlock int32
		gw.atBlock = func() { replicasAtBlock = replicas() }
		p := &corev1alpha1.Profile{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns}, p)).To(Succeed())
		p.Spec.Namespaces = nil
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
		reconcileOnce()
		Expect(replicasAtBlock).To(Equal(int32(1)))
		Expect(replicas()).To(Equal(int32(0)))
		Expect(exists(&corev1.Secret{}, name+"-gateway")).To(BeFalse())
		Expect(gw.order).To(Equal([]string{opCreate, opBlock, opUsage}))
		expectBound(false)
	})

	It("maps a Namespace and a Profile to the Workspaces in the namespace", func() {
		setup(true, true)
		want := []reconcile.Request{{NamespacedName: nn(name)}}
		Expect(rec.workspacesForNamespace(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).
			To(Equal(want))
		p := &corev1alpha1.Profile{Spec: corev1alpha1.ProfileSpec{Namespaces: []string{ns, "wsbind-none"}}}
		Expect(rec.workspacesForProfile(ctx, p)).To(Equal(want))
	})
})

var _ = Describe("namespaceBound", func() {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nb",
		Labels: map[string]string{corev1alpha1.ProfileLabel: "alice"}}}
	profile := func() *corev1alpha1.Profile {
		return &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: "alice"},
			Spec: corev1alpha1.ProfileSpec{Namespaces: []string{"nb"}}}
	}

	It("binds only if the Profile is not in deletion", func() {
		p := profile()
		Expect(namespaceBound(p, ns)).To(BeTrue())
		p.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		Expect(namespaceBound(p, ns)).To(BeFalse())
	})

	It("finds the start of a Profile delete, and not a status write", func() {
		old, statusOnly, deleted := profile(), profile(), profile()
		statusOnly.Status.BoundNamespaces = []string{"nb"}
		deleted.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		Expect(deleteStarted(event.UpdateEvent{ObjectOld: old, ObjectNew: statusOnly})).To(BeFalse())
		Expect(deleteStarted(event.UpdateEvent{ObjectOld: old, ObjectNew: deleted})).To(BeTrue())
		Expect(deleteStarted(event.UpdateEvent{ObjectOld: nil, ObjectNew: deleted})).To(BeFalse())
	})
})
