//go:build upgrade

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

package upgrade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const (
	// The release that the test upgrades from. Its CRDs have v1alpha1 only.
	oldVersion = "0.6.0"
	oldChart   = "oci://ghcr.io/mohanraj00/charts/koodam"
	release    = "koodam"
	systemNS   = "koodam-system"
	runNS      = "koodam-upgrade"
	// The manager image of this tree. The test builds it and loads it on kind.
	imageRepository = "example.com/koodam"
	imageTag        = "upgrade"
	// isTrue is the status of a condition that is true.
	isTrue = "True"
	// limit is the time for one install, one upgrade or one status.
	limit = 10 * time.Minute
)

// The CRDs that the chart of the old release has in crds/. Helm does not own them.
var coreCRDs = []string{"agents", "approvalrecords", "holds", "profiles", "spendbudgets", "testkeys", "workspaces"}

// The CRDs of the App kinds. They are chart templates in the two releases.
var appCRDs = []string{"apps", "appversions", "tenantquotas"}

// cluster is the kind cluster of the test.
type cluster struct {
	t          *testing.T
	ctx        context.Context
	root       string
	kubeconfig string
	cfg        *rest.Config
}

// run runs a command in the project directory with the kubeconfig of the kind cluster.
func (k *cluster) run(name string, args ...string) string {
	k.t.Helper()
	cmd := exec.CommandContext(k.ctx, name, args...)
	cmd.Dir = k.root
	cmd.Env = append(os.Environ(), "KUBECONFIG="+k.kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		k.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (k *cluster) kubectl(args ...string) string {
	k.t.Helper()
	return k.run("kubectl", args...)
}

// client returns a new client. A client keeps the API versions that it found, so the test makes
// a new one after the upgrade.
func (k *cluster) client() client.Client {
	k.t.Helper()
	c, err := client.New(k.cfg, client.Options{})
	if err != nil {
		k.t.Fatal(err)
	}
	return c
}

// eventually calls check until it returns nil.
func (k *cluster) eventually(what string, check func() error) {
	k.t.Helper()
	var err error
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if err = check(); err == nil {
			return
		}
	}
	k.t.Fatalf("%s: %v", what, err)
}

// connect returns the kind cluster of KIND_CLUSTER. The test uses a kubeconfig file of its own,
// so it does not use or change the current context of the user.
func connect(t *testing.T) *cluster {
	t.Helper()
	name := os.Getenv("KIND_CLUSTER")
	if name == "" {
		t.Fatal("KIND_CLUSTER is not set. Use `make test-upgrade`.")
	}
	kind := os.Getenv("KIND")
	if kind == "" {
		kind = "kind"
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	k := &cluster{t: t, ctx: t.Context(), root: root, kubeconfig: filepath.Join(t.TempDir(), "kubeconfig")}
	config, err := exec.CommandContext(k.ctx, kind, "get", "kubeconfig", "--name", name).Output()
	if err != nil {
		t.Fatalf("kind get kubeconfig: %v", err)
	}
	if err := os.WriteFile(k.kubeconfig, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if k.cfg, err = clientcmd.BuildConfigFromFlags("", k.kubeconfig); err != nil {
		t.Fatal(err)
	}
	t.Log("building the manager image of this tree and loading it on kind")
	image := imageRepository + ":" + imageTag
	k.run("make", "docker-build", "IMG="+image)
	k.run(kind, "load", "docker-image", image, "--name", name)
	t.Cleanup(func() {
		if t.Failed() {
			// The state of the cluster helps to find the cause. It has no Secret data.
			out, _ := exec.Command("kubectl", "--kubeconfig", k.kubeconfig, "get", "pods,deployments,crds", "-A").
				CombinedOutput()
			t.Logf("the cluster after the failure:\n%s", out)
		}
	})
	return k
}

// crd returns a field of a CRD, for example the served versions.
func (k *cluster) crd(resource, jsonpath string) string {
	k.t.Helper()
	return k.kubectl("get", "crd", resource+"."+group, "-o", "jsonpath="+jsonpath)
}

// installOld installs the chart of the old release with the bundled gateway and its mock model.
func (k *cluster) installOld() {
	k.t.Helper()
	c := k.client()
	// The label binds the namespace to the Profile of newObjects.
	bound := map[string]string{corev1alpha1.ProfileLabel: "upgrade-" + runNS}
	namespaces := []*corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: systemNS}},
		{ObjectMeta: metav1.ObjectMeta{Name: runNS, Labels: bound}}}
	for _, ns := range namespaces {
		if err := c.Create(k.ctx, ns); err != nil {
			k.t.Fatal(err)
		}
	}
	// The master key of the gateway is random. The test does not print it.
	key := make([]byte, 24)
	if _, err := rand.Read(key); err != nil {
		k.t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "koodam-gateway", Namespace: systemNS},
		StringData: map[string]string{"master-key": "sk-" + hex.EncodeToString(key)}}
	if err := c.Create(k.ctx, secret); err != nil {
		k.t.Fatal("the test did not make the master key Secret")
	}
	dir := k.t.TempDir()
	k.run("helm", "pull", oldChart, "--version", oldVersion, "--untar", "--untardir", dir)
	chart := filepath.Join(dir, "koodam")
	k.run("helm", "install", release, chart, "-n", systemNS, "-f", filepath.Join(chart, "values-bundled.yaml"),
		"--set", "apps.enabled=true", "--wait", "--timeout", limit.String())
	for _, resource := range append(append([]string{}, coreCRDs...), appCRDs...) {
		if got := k.crd(resource, "{.spec.versions[*].name}"); got != alpha {
			k.t.Fatalf("the old release has the versions %q of %s, want only %s", got, resource, alpha)
		}
	}
}

// upgrade does the steps of docs/upgrade.md to the chart and the image of this tree.
func (k *cluster) upgrade() {
	k.t.Helper()
	// Step 1: Helm adopts the CRDs that the old chart had in crds/.
	for _, resource := range coreCRDs {
		crd := resource + "." + group
		k.kubectl("label", "crd", crd, "app.kubernetes.io/managed-by=Helm", "--overwrite")
		k.kubectl("annotate", "crd", crd, "meta.helm.sh/release-name="+release,
			"meta.helm.sh/release-namespace="+systemNS, "--overwrite")
	}
	// Step 2. The Workspace image stays the image of the old release: this tree has no release.
	k.run("helm", "upgrade", release, "charts/koodam", "-n", systemNS, "-f", "charts/koodam/values-bundled.yaml",
		"--set", "apps.enabled=true", "--set", "image.repository="+imageRepository, "--set", "image.tag="+imageTag,
		"--set", "workspace.image.tag="+oldVersion, "--wait", "--timeout", limit.String())
	images := k.kubectl("-n", systemNS, "get", "deployments", "-o",
		"jsonpath={.items[*].spec.template.spec.containers[*].image}")
	if n := strings.Count(images, imageRepository+":"+imageTag); n < 2 || strings.Contains(images, "/koodam:"+oldVersion) {
		k.t.Fatalf("want the image of this tree in the manager and in the conversion webhook, got %s", images)
	}
	for _, resource := range append(append([]string{}, coreCRDs...), appCRDs...) {
		if got := k.crd(resource, "{.spec.versions[?(@.storage==true)].name}"); got != beta {
			k.t.Fatalf("after the upgrade, the storage version of %s is %q", resource, got)
		}
	}
}

// condition returns the status and the reason of a condition of an object.
func condition(o *unstructured.Unstructured, name string) (string, string) {
	conditions, _, _ := unstructured.NestedSlice(o.Object, keyStatus, "conditions")
	for _, item := range conditions {
		if c, ok := item.(object); ok && c["type"] == name {
			status, _ := c[keyStatus].(string)
			reason, _ := c["reason"].(string)
			return status, reason
		}
	}
	return "", ""
}

// reconciled waits until the controllers gave each object of the list its end status: the Agent
// ended and its key is revoked, the TestKey has a key, and the Hold has a result.
func (k *cluster) reconciled(c client.Client, objects []*unstructured.Unstructured) {
	k.t.Helper()
	want := map[string][2]string{kindAgent: {"Revoked", isTrue}, kindTestKey: {"Active", isTrue},
		kindHold: {"Held", isTrue}}
	for _, o := range objects {
		w, ok := want[o.GetKind()]
		if !ok {
			continue
		}
		k.eventually(fmt.Sprintf("%s %s is not reconciled", o.GetKind(), o.GetName()), func() error {
			got, err := read(k.ctx, c, o, alpha)
			if err != nil {
				return err
			}
			status, reason := condition(got, w[0])
			phase, _, _ := unstructured.NestedString(got.Object, keyStatus, "phase")
			if status != w[1] || (o.GetKind() == kindAgent && phase != succeeded) {
				return fmt.Errorf("the condition %s is %q (%s) and the phase is %q", w[0], status, reason, phase)
			}
			return nil
		})
	}
}

// create makes the objects and a TestKey for each Workspace of the list. It returns all of them.
func (k *cluster) create(c client.Client, objects []*unstructured.Unstructured) []*unstructured.Unstructured {
	k.t.Helper()
	all := make([]*unstructured.Unstructured, 0, len(objects)+1)
	for _, o := range objects {
		if err := c.Create(k.ctx, o); err != nil {
			k.t.Fatalf("%s %s: %v", o.GetKind(), o.GetName(), err)
		}
		all = append(all, o)
		if o.GetKind() == kindWorkspace {
			key := newTestKey(o.GetName(), o)
			if err := c.Create(k.ctx, key); err != nil {
				k.t.Fatalf("%s %s: %v", key.GetKind(), key.GetName(), err)
			}
			all = append(all, key)
		}
	}
	return all
}

// checkData makes sure that each object reads in the two versions with the same data, and that
// its v1alpha1 spec is the spec of before the upgrade.
func (k *cluster) checkData(c client.Client, before []*unstructured.Unstructured) {
	k.t.Helper()
	for _, o := range before {
		old, err := checkTwoVersions(k.ctx, c, o)
		if err == nil {
			err = sameSpec(o, old)
		}
		if err != nil {
			k.t.Errorf("%s %s: %v", o.GetKind(), o.GetName(), err)
		}
	}
	if k.t.Failed() {
		k.t.FailNow()
	}
}

// TestUpgrade installs the old release on kind, makes one v1alpha1 object of each kind, and
// upgrades to this tree with the steps of docs/upgrade.md.
func TestUpgrade(t *testing.T) {
	k := connect(t)
	t.Log("installing the release " + oldVersion)
	k.installOld()
	user := k.kubectl("auth", "whoami", "-o", "jsonpath={.status.userInfo.username}")

	t.Log("making one v1alpha1 object of each kind")
	c := k.client()
	objects := k.create(c, newObjects(runNS, user))
	k.reconciled(c, objects)
	before := make([]*unstructured.Unstructured, 0, len(objects))
	seen := map[string]bool{}
	for _, o := range objects {
		got, err := read(k.ctx, c, o, alpha)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, got)
		seen[o.GetKind()] = true
	}
	if len(seen) != len(kinds) {
		t.Fatalf("the test has objects of %d kinds, want %d", len(seen), len(kinds))
	}

	t.Log("upgrading to the chart and the image of this tree")
	k.upgrade()
	c = k.client()

	t.Log("check 1: each object reads in v1alpha1 and in v1beta1 with the same data")
	k.checkData(c, before)

	t.Log("check 2: the storage migration")
	t.Log(k.run("hack/migrate-storage.sh"))
	for _, resource := range append(append([]string{}, coreCRDs...), appCRDs...) {
		if got := k.crd(resource, "{.status.storedVersions[*]}"); got != beta {
			t.Errorf("after the migration, the stored versions of %s are %q, want only %s", resource, got, beta)
		}
	}
	k.checkData(c, before)

	t.Log("check 3: the controllers reconcile after the upgrade")
	// The Agent, Workspace and TestKey controllers make a run and 2 keys for new objects.
	const second = "upgrade-b"
	k.reconciled(c, k.create(c, []*unstructured.Unstructured{newAgent(runNS, second), newWorkspace(runNS, second)}))
	// The Hold controller sees the release of the Hold of before the upgrade.
	var hold *unstructured.Unstructured
	for _, o := range before {
		if o.GetKind() == kindHold {
			hold = o.DeepCopy()
		}
	}
	patch := fmt.Sprintf(`{"spec":{"release":{"user":%q,"reason":"The upgrade test releases the Hold."}}}`, user)
	if err := c.Patch(k.ctx, hold, client.RawPatch(types.MergePatchType, []byte(patch))); err != nil {
		t.Fatal(err)
	}
	k.eventually("the Hold is not released", func() error {
		got, err := read(k.ctx, c, hold, alpha)
		if err != nil {
			return err
		}
		if status, reason := condition(got, "Held"); status != "False" || reason != "Released" {
			return fmt.Errorf("the condition Held is %q (%s)", status, reason)
		}
		return nil
	})
}
