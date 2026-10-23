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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/test/utils"
)

const (
	// wsLabel is on each object that the controller makes for a Workspace.
	wsLabel = "koodam.dev/workspace"
	// wsProfile binds wsNS. No Profile binds freeNS.
	wsProfile = "koodam-e2e-dev"
	wsNS      = "koodam-e2e-ws"
	freeNS    = "koodam-e2e-ws-unbound"
	wsName    = "dev"
	// wsKeyField is the field of the gateway key in the Secret <workspace>-gateway.
	wsKeyField = "ANTHROPIC_AUTH_TOKEN"
)

// The Workspace specs connect with ssh through the ProxyCommand that "koodam ssh setup" writes.
// The SSH client key is made at run time in a temporary HOME. The test never prints a key.
// Each Eventually gives its timeout. The specs do not change the defaults of the suite.
var _ = Describe("Workspaces", Ordered, Label("workspace"), func() {
	var (
		// home is the temporary HOME of koodam. It has .ssh with the client key and the host file.
		home   string
		koodam string
		env    []string
		ws     *corev1alpha1.Workspace
		marker string
	)

	// ssh runs command in the Workspace with the host entry of koodam ssh setup. It returns
	// stdout. OpenSSH expands ~ with the passwd home, not HOME, so the options give the paths.
	ssh := func(command string) (string, error) {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		dir := filepath.Join(home, ".ssh")
		cmd := exec.CommandContext(c, "ssh", "-F", filepath.Join(dir, "config.d", "koodam"),
			"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=30",
			"-o", "UserKnownHostsFile="+filepath.Join(dir, "koodam_known_hosts"),
			"-o", "IdentityFile="+filepath.Join(dir, "koodam_ed25519"),
			"koodam-"+wsName+"."+wsNS, command)
		var out bytes.Buffer
		cmd.Env, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = env, &out, GinkgoWriter, 5*time.Second
		_, _ = fmt.Fprintf(GinkgoWriter, "ssh: %q\n", command)
		err := cmd.Run()
		return out.String(), err
	}

	BeforeAll(func() {
		var err error
		home, err = os.MkdirTemp("", "koodam-e2e-home-")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, home)
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			h, err := os.UserHomeDir()
			Expect(err).NotTo(HaveOccurred())
			kubeconfig = filepath.Join(h, ".kube", "config")
		}
		env = append(os.Environ(), "HOME="+home, "KUBECONFIG="+kubeconfig)

		By("building the koodam CLI")
		// koodam ssh setup writes the absolute path of this program in the ProxyCommand.
		koodam = filepath.Join(home, "bin", "koodam")
		Expect(os.MkdirAll(filepath.Dir(koodam), 0o700)).To(Succeed())
		_, err = utils.Run(exec.Command("go", "build", "-o", koodam, "./cmd/koodam"))
		Expect(err).NotTo(HaveOccurred())

		By("making the SSH client key in the temporary HOME")
		Expect(os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)).To(Succeed())
		key := filepath.Join(home, ".ssh", "koodam_ed25519")
		_, err = utils.Run(exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "koodam-e2e", "-f", key))
		Expect(err).NotTo(HaveOccurred())
		pub, err := os.ReadFile(key + ".pub")
		Expect(err).NotTo(HaveOccurred())

		By("making a Profile that binds " + wsNS + ", and the namespace " + freeNS + " with no Profile")
		// Register the cleanup before the first object, so that a failure in BeforeAll leaks nothing.
		DeferCleanup(cleanWorkspaces)
		objs := []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: wsNS,
				Labels: map[string]string{corev1alpha1.ProfileLabel: wsProfile}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: freeNS}},
			&corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: wsProfile},
				Spec: corev1alpha1.ProfileSpec{Namespaces: []string{wsNS}}},
		}
		for _, o := range objs {
			Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, o))).To(Succeed())
		}
		ws = &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: wsName, Namespace: wsNS},
			Spec: corev1alpha1.WorkspaceSpec{
				SSHPublicKeys: []string{strings.TrimSpace(string(pub))},
				Image:         workspaceImage, Storage: resource.MustParse("1Gi"),
				Gateway:   &corev1alpha1.WorkspaceGateway{Model: mockModel, BudgetPerDay: resource.MustParse("1")},
				Harnesses: []corev1alpha1.HarnessSpec{{Name: "claude-code"}, {Name: "codex"}},
			}}
		b := make([]byte, 8)
		_, err = rand.Read(b)
		Expect(err).NotTo(HaveOccurred())
		marker = "koodam-e2e-" + hex.EncodeToString(b)

		By("storing a synthetic Codex login before the Workspace starts")
		cmd := exec.Command(koodam, "-n", wsNS, "login", "codex", "--stdin")
		cmd.Env = env
		login := rand.Text()
		cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"tokens":{"id_token":%q,"access_token":%q,"refresh_token":%q}}`,
			login, login, login))
		Expect(cmd.Run()).To(Succeed(), "Cannot store the synthetic Codex login.")
	})

	AfterEach(func() { dumpWorkspaces() })

	It("keeps the tmux session after an SSH disconnect", func() {
		By("making the Workspace with the SSH public key")
		Expect(k8s.Create(ctx, ws)).To(Succeed())
		waitPhase(ws, corev1alpha1.WorkspaceRunning)

		By("running koodam ssh setup in the temporary HOME")
		cmd := exec.Command(koodam, "-n", wsNS, "ssh", "setup", wsName, "--yes")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		Expect(string(out)).To(ContainSubstring("SSH is configured"))
		Expect(k8s.Get(ctx, client.ObjectKeyFromObject(ws), ws)).To(Succeed())
		Expect(ws.Spec.SSHPublicKeys).To(HaveLen(1), "koodam ssh setup added the key a second time.")
		entry, err := os.ReadFile(filepath.Join(home, ".ssh", "config.d", "koodam"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(entry)).To(ContainSubstring("tunnel " + wsNS + "/" + wsName))

		By("connecting through koodam tunnel and running a command in tmux")
		Eventually(func() error { _, err := ssh("true"); return err }, 2*time.Minute, 5*time.Second).Should(Succeed())
		By("checking the login copy before an SSH login shell runs")
		_, err = ssh(`test -f ~/.codex/auth.json && cmp -s /etc/koodam/login/codex ~/.codex/auth.json && ` +
			`test "$(stat -c %a ~/.codex)" = 700 && test "$(stat -c %a ~/.codex/auth.json)" = 600`)
		Expect(err).NotTo(HaveOccurred(), "The Workspace did not get a private, writable Codex login.")
		By("finding the checked harnesses of spec.harnesses with no login shell")
		bins, err := ssh(`command -v claude codex && readlink -e /usr/local/bin/claude /usr/local/bin/codex && ` +
			`claude --version && codex --version`)
		Expect(err).NotTo(HaveOccurred(), bins)
		Expect(bins).To(HavePrefix("/usr/local/bin/claude\n/usr/local/bin/codex\n/koodam/bin/claude\n/koodam/bin/codex\n"),
			"The harnesses are not in /koodam/bin.")
		_, err = ssh("tmux new-session -d -s e2e && tmux send-keys -t e2e 'echo " + marker +
			" | tee ~/e2e-marker' Enter")
		Expect(err).NotTo(HaveOccurred())

		By("connecting again and reading the same tmux session")
		Eventually(func(g Gomega) {
			out, err := ssh("tmux capture-pane -p -t e2e")
			g.Expect(err).NotTo(HaveOccurred())
			lines := strings.Split(out, "\n")
			for i := range lines {
				lines[i] = strings.TrimSpace(lines[i])
			}
			// The echo output is a line with only the marker. The command line has more text.
			g.Expect(lines).To(ContainElement(marker))
		}, 2*time.Minute, 2*time.Second).Should(Succeed(), "The tmux session e2e does not have the output.")
	})

	It("blocks the key at a suspend, stops the pod and keeps the PVC", func() {
		By("reading the gateway key while the Workspace is Running")
		Eventually(func() bool {
			_ = k8s.Get(ctx, client.ObjectKeyFromObject(ws), ws)
			return meta.IsStatusConditionTrue(ws.Status.Conditions, "GatewayKey")
		}, 2*time.Minute, time.Second).Should(BeTrue(), "The gateway key is not active.")
		hash := ws.Status.KeyHash
		s := &corev1.Secret{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: wsNS, Name: wsName + "-gateway"}, s)).To(Succeed())
		key := string(s.Data[wsKeyField])
		Expect(key != "").To(BeTrue(), "The gateway Secret has no key.")
		code, _ := modelCall(ctx, key)
		Expect(code).To(Equal(http.StatusOK), "The Workspace key did not work.")

		By("suspending the Workspace")
		setSuspend(ws, true)
		waitPhase(ws, corev1alpha1.WorkspaceSuspended)
		Eventually(func() string {
			_ = k8s.Get(ctx, client.ObjectKeyFromObject(ws), ws)
			if c := meta.FindStatusCondition(ws.Status.Conditions, "GatewayKey"); c != nil {
				return c.Reason
			}
			return ""
		}, time.Minute, time.Second).Should(Equal("Revoked"))

		By("checking that the gateway refuses the key and has it blocked")
		Eventually(func(g Gomega) {
			code, errType := modelCall(ctx, key)
			g.Expect(code == http.StatusUnauthorized && errType == "auth_error").To(BeTrue(), fmt.Sprintf(
				"The gateway did not refuse the key: status %d, error type %q.", code, errType))
			g.Expect(keyInfo(hash).Blocked).To(BeTrue(), "The gateway did not block the key.")
		}, 30*time.Second, time.Second).Should(Succeed())

		By("checking that the pod stopped and the home PVC stays")
		Eventually(func(g Gomega) {
			sts := &appsv1.StatefulSet{}
			g.Expect(k8s.Get(ctx, client.ObjectKey{Namespace: wsNS, Name: wsName}, sts)).To(Succeed())
			g.Expect(sts.Spec.Replicas).NotTo(BeNil())
			g.Expect(*sts.Spec.Replicas).To(BeZero())
		}, time.Minute, time.Second).Should(Succeed(), "The StatefulSet does not have 0 replicas.")
		Eventually(func() int {
			pods := &corev1.PodList{}
			_ = k8s.List(ctx, pods, client.InNamespace(wsNS), client.MatchingLabels{wsLabel: wsName})
			return len(pods.Items)
		}, 2*time.Minute, time.Second).Should(BeZero(), "The Workspace pod did not stop.")
		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: wsNS, Name: "home-" + wsName + "-0"}, pvc)).To(Succeed())
		Expect(pvc.DeletionTimestamp).To(BeNil(), "The home PVC is in deletion.")

		By("resuming the Workspace and reading the file from the first session")
		setSuspend(ws, false)
		waitPhase(ws, corev1alpha1.WorkspaceRunning)
		Eventually(func(g Gomega) {
			out, err := ssh("cat ~/e2e-marker")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal(marker))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "The home PVC did not keep the file.")
	})

	It("keeps a Workspace Pending in a namespace that no Profile binds", func() {
		free := &corev1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: wsName, Namespace: freeNS},
			Spec: corev1alpha1.WorkspaceSpec{Image: workspaceImage, SSHPublicKeys: ws.Spec.SSHPublicKeys}}
		Expect(k8s.Create(ctx, free)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(free), free)).To(Succeed())
			g.Expect(free.Status.Phase).To(Equal(corev1alpha1.WorkspacePending))
			c := meta.FindStatusCondition(free.Status.Conditions, "ProfileBound")
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(c.Reason).To(Equal("NamespaceNotBound"))
		}, time.Minute, time.Second).Should(Succeed())
		err := k8s.Get(ctx, client.ObjectKey{Namespace: freeNS, Name: wsName}, &appsv1.StatefulSet{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "The controller made a StatefulSet in "+freeNS+".")
	})
})

// waitPhase waits until the Workspace ws has the phase p.
func waitPhase(ws *corev1alpha1.Workspace, p corev1alpha1.WorkspacePhase) {
	By("waiting for the phase " + string(p))
	Eventually(func() corev1alpha1.WorkspacePhase {
		_ = k8s.Get(ctx, client.ObjectKeyFromObject(ws), ws)
		return ws.Status.Phase
	}, 5*time.Minute, time.Second).Should(Equal(p), "The Workspace does not have the phase "+string(p)+".")
}

// cleanWorkspaces deletes the Workspaces in the test namespaces, the Profile and the namespaces.
// It waits until the namespaces are gone, with the home PVC.
func cleanWorkspaces() {
	By("deleting the Workspaces, the Profile and the namespaces")
	namespaces := []string{wsNS, freeNS}
	for _, ns := range namespaces {
		_ = k8s.DeleteAllOf(ctx, &corev1alpha1.Workspace{}, client.InNamespace(ns))
	}
	Eventually(func() int {
		n := 0
		for _, ns := range namespaces {
			l := &corev1alpha1.WorkspaceList{}
			_ = k8s.List(ctx, l, client.InNamespace(ns))
			n += len(l.Items)
		}
		return n
	}, 2*time.Minute, time.Second).Should(BeZero(), "The finalizer kept a Workspace.")
	_ = k8s.Delete(ctx, &corev1alpha1.Profile{ObjectMeta: metav1.ObjectMeta{Name: wsProfile}})
	for _, ns := range namespaces {
		_ = k8s.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	Eventually(func() bool {
		for _, ns := range namespaces {
			if !apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKey{Name: ns}, &corev1.Namespace{})) {
				return false
			}
		}
		return true
	}, 5*time.Minute, 2*time.Second).Should(BeTrue(), "A test namespace was not deleted.")
}

// setSuspend writes spec.suspend of ws with a merge patch.
func setSuspend(ws *corev1alpha1.Workspace, suspend bool) {
	Expect(k8s.Get(ctx, client.ObjectKeyFromObject(ws), ws)).To(Succeed())
	patch := client.MergeFrom(ws.DeepCopy())
	ws.Spec.Suspend = suspend
	Expect(k8s.Patch(ctx, ws, patch)).To(Succeed())
}

// dumpWorkspaces writes the controller log, the Workspaces, the pods, the events and the
// Workspace pod log if the spec failed.
func dumpWorkspaces() {
	if !CurrentSpecReport().Failed() {
		return
	}
	for _, args := range [][]string{
		{"-n", systemNS, "logs", "-l", controllerSelector, "--tail", "200"},
		{"get", "statefulsets,pods,pvc", "-A", "-l", wsLabel, "-o", "wide"},
		{"get", "workspaces", "-A", "-o", "yaml"},
		{"-n", wsNS, "get", "events", "--sort-by=.lastTimestamp"},
		{"-n", wsNS, "logs", wsName + "-0", "--tail", "100"},
	} {
		out, _ := utils.Run(exec.Command("kubectl", args...))
		_, _ = fmt.Fprintln(GinkgoWriter, out)
	}
}
