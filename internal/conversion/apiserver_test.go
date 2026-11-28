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

package conversion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	apix "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

const (
	v1alpha1  = "v1alpha1"
	v1beta1   = "v1beta1"
	namespace = "default"
	testKey   = "TestKey"
	agent     = "Agent"
	budget    = "SpendBudget"
	profile   = "Profile"
	// The keys and the values of the test specs and labels.
	keyBudget   = "budget"
	keyPlugins  = "plugins"
	keyHarness  = "harness"
	labelKey    = "changed"
	labelValue  = "yes"
	specKey     = "spec"
	oneUnit     = "1"
	halfAnHour  = "30m"
	workspaceID = "workspaceRef"
)

// webhookKinds are the kinds whose CRD has the conversion webhook. The 7 other kinds have the
// same schema in the two versions, so their CRD has the strategy None.
var webhookKinds = []string{agent, testKey, "Workspace"}

var specs = map[string]object{
	testKey: {workspaceID: object{keyName: "w", "uid": "00000000-0000-0000-0000-000000000000"}, oldID: run,
		keyBudget: oneUnit, "ttl": halfAnHour},
	agent:  {"image": "example.test/agent:1", "command": []any{"run"}, "model": "m", keyBudget: oneUnit},
	budget: {"period": "Day", "spend": "25"},
	// A Profile is for the full cluster. The 3 other kinds have a namespace.
	profile: {"namespaces": []any{"dev"}},
}

// loadCRDs reads the 10 generated CRDs and makes version the storage version of each one.
func loadCRDs(t *testing.T, version string) []*apix.CustomResourceDefinition {
	t.Helper()
	var crds []*apix.CustomResourceDefinition
	for _, dir := range []string{"crd", filepath.Join("apps", "crd")} {
		paths, err := filepath.Glob(filepath.Join("..", "..", "config", dir, "bases", "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			crd := &apix.CustomResourceDefinition{}
			if err := yaml.UnmarshalStrict(raw, crd); err != nil {
				t.Fatal(err)
			}
			setStorage(crd, version)
			crds = append(crds, crd)
		}
	}
	if len(crds) != 10 {
		t.Fatalf("found %d CRDs, want 10", len(crds))
	}
	return crds
}

func setStorage(crd *apix.CustomResourceDefinition, version string) {
	for i := range crd.Spec.Versions {
		crd.Spec.Versions[i].Storage = crd.Spec.Versions[i].Name == version
	}
}

// cluster is an envtest API server with the 10 CRDs and the conversion server.
type cluster struct {
	t          *testing.T
	c          client.Client
	kubeconfig string
	stopServer func()
}

// start starts the API server with v1alpha1 as the storage version, as in an install before #511.
// Only the CRDs of webhookKinds get the conversion webhook, as in the chart.
func start(t *testing.T) *cluster {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("KUBEBUILDER_ASSETS is not set")
		}
		t.Skip("KUBEBUILDER_ASSETS is not set. Run make test.")
	}
	hooked := runtime.NewScheme()
	hooked.AddKnownTypes(corev1alpha1.GroupVersion, &corev1alpha1.Agent{}, &corev1alpha1.TestKey{}, &corev1alpha1.Workspace{})
	hooked.AddKnownTypes(corev1beta1.GroupVersion, &corev1beta1.Agent{}, &corev1beta1.TestKey{}, &corev1beta1.Workspace{})
	// envtest waits only 5 seconds for the default namespace. On a host with a high load, the
	// first start can be too slow, so the test tries 3 times.
	var env *envtest.Environment
	for try := 1; ; try++ {
		env = &envtest.Environment{CRDs: loadCRDs(t, v1alpha1), Scheme: hooked}
		if _, err := env.Start(); err == nil {
			break
		} else if _ = env.Stop(); try == 3 {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = env.Stop() })

	hook := env.WebhookInstallOptions
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(hook.LocalServingHost, hook.LocalServingPort, hook.LocalServingCertDir, func(string) {})
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("the conversion server stopped with an error: %v", err)
		}
		done <- nil
	}
	t.Cleanup(stop)

	scheme := runtime.NewScheme()
	if err := apix.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := env.AddUser(envtest.User{Name: "admin", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err := admin.KubeConfig()
	if err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, config, 0o600); err != nil {
		t.Fatal(err)
	}
	return &cluster{t: t, c: c, kubeconfig: kubeconfig, stopServer: stop}
}

// key returns the namespace and the name of a test object of a kind: the old object, which the
// migration script must write again, or the probe, which shows when the API server uses a new
// storage version. A namespace has only one SpendBudget, and its name is default. A Profile has
// no namespace.
func key(kind string, probe bool) (namespace, name string) {
	namespace, name = "default", "old"
	if probe {
		namespace = "kube-public"
	}
	switch kind {
	case budget:
		name = "default"
	case profile:
		namespace = ""
		if probe {
			name = "probe"
		}
	}
	return namespace, name
}

func newUnstructured(version, kind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(corev1beta1.GroupVersion.Group + "/" + version)
	u.SetKind(kind)
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

func (k *cluster) create(version, kind, namespace, name string, spec object) error {
	u := newUnstructured(version, kind, namespace, name)
	u.Object[specKey] = runtime.DeepCopyJSON(spec)
	return k.c.Create(k.t.Context(), u)
}

func (k *cluster) mustCreate(version, kind, namespace, name string, spec object) {
	k.t.Helper()
	if err := k.create(version, kind, namespace, name, spec); err != nil {
		k.t.Fatalf("create %s %s as %s: %v", kind, name, version, err)
	}
}

// get reads the old object or the probe of a kind.
func (k *cluster) get(version, kind string, probe bool) *unstructured.Unstructured {
	k.t.Helper()
	namespace, name := key(kind, probe)
	return k.named(version, kind, namespace, name)
}

func (k *cluster) old(version, kind string) *unstructured.Unstructured {
	k.t.Helper()
	return k.get(version, kind, false)
}

func (k *cluster) named(version, kind, namespace, name string) *unstructured.Unstructured {
	k.t.Helper()
	u := newUnstructured(version, kind, namespace, name)
	if err := k.c.Get(k.t.Context(), types.NamespacedName{Namespace: namespace, Name: name}, u); err != nil {
		k.t.Fatalf("get %s %s as %s: %v", kind, name, version, err)
	}
	return u
}

func (k *cluster) crds() []apix.CustomResourceDefinition {
	k.t.Helper()
	list := &apix.CustomResourceDefinitionList{}
	if err := k.c.List(k.t.Context(), list); err != nil {
		k.t.Fatal(err)
	}
	return list.Items
}

// useV1beta1Storage makes v1beta1 the storage version of each CRD, as the upgrade does. Then it
// waits until the API server writes each kind of specs as v1beta1: an update with no change gets a
// new resourceVersion only if the API server writes the object in a different version.
func (k *cluster) useV1beta1Storage() {
	k.t.Helper()
	for _, crd := range k.crds() {
		setStorage(&crd, v1beta1)
		if err := k.c.Update(k.t.Context(), &crd); err != nil {
			k.t.Fatal(err)
		}
	}
	for kind := range specs {
		deadline := time.Now().Add(time.Minute)
		for {
			probe := k.get(v1alpha1, kind, true)
			before := probe.GetResourceVersion()
			if err := k.c.Update(k.t.Context(), probe); err == nil && probe.GetResourceVersion() != before {
				break
			}
			if time.Now().After(deadline) {
				k.t.Fatalf("the API server did not write the %s as v1beta1", kind)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (k *cluster) migrate() string {
	k.t.Helper()
	cmd := exec.CommandContext(k.t.Context(), "bash", filepath.Join("..", "..", "hack", "migrate-storage.sh"))
	cmd.Env = append(os.Environ(), "KUBECTL="+filepath.Join(os.Getenv("KUBEBUILDER_ASSETS"), "kubectl")+" --kubeconfig "+k.kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		k.t.Fatalf("migrate-storage.sh: %v\n%s", err, out)
	}
	return string(out)
}

// TestWithTheAPIServer does the steps of an upgrade on a real API server: objects that are stored
// as v1alpha1, the change of the storage version, the conversion in the two directions, the
// storage migration and a stop of the webhook.
func TestWithTheAPIServer(t *testing.T) {
	k := start(t)
	for _, crd := range k.crds() {
		want := apix.NoneConverter
		if slices.Contains(webhookKinds, crd.Spec.Names.Kind) {
			want = apix.WebhookConverter
		}
		if crd.Spec.Conversion == nil || crd.Spec.Conversion.Strategy != want {
			t.Fatalf("%s: got the conversion %+v, want the strategy %s", crd.Name, crd.Spec.Conversion, want)
		}
	}

	// These objects are stored as v1alpha1.
	baseline := map[string]any{}
	for kind, spec := range specs {
		for _, probe := range []bool{false, true} {
			namespace, name := key(kind, probe)
			k.mustCreate(v1alpha1, kind, namespace, name, spec)
		}
		baseline[kind] = k.old(v1alpha1, kind).Object[specKey]
	}

	k.useV1beta1Storage()

	// The steps use the state of the steps before them, so they are not subtests.
	// An old object reads in the two versions.
	k.checkOldObjects(baseline)
	// A v1alpha1 client keeps the data that only v1beta1 has.
	k.checkHubOnlyData()
	// The migration script writes each object as v1beta1.
	k.checkMigration()
	// A stop of the webhook fails only the requests that need a conversion.
	k.checkStoppedWebhook()
	// The API server accepts the removal of v1alpha1 after the migration.
	k.checkRemoval()
}

func (k *cluster) checkOldObjects(baseline map[string]any) {
	t := k.t
	t.Helper()
	for kind, want := range baseline {
		if got := k.old(v1alpha1, kind).Object[specKey]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s as v1alpha1: got %v, want %v", kind, got, want)
		}
	}
	spec := k.old(v1beta1, testKey).Object[specKey].(object)
	if spec[newID] != run || spec[oldID] != nil {
		t.Errorf("TestKey as v1beta1: got %v", spec)
	}
	if got := k.old(v1beta1, budget).Object[specKey]; !reflect.DeepEqual(got, baseline[budget]) {
		t.Errorf("SpendBudget as v1beta1: got %v", got)
	}
}

func (k *cluster) checkHubOnlyData() {
	t := k.t
	t.Helper()
	plugins := []any{"other-a", "other-b"}
	spec := runtime.DeepCopyJSON(specs[agent])
	spec[keyHarness] = object{keyName: "codex", keyPlugins: plugins}
	k.mustCreate(v1beta1, agent, namespace, "new", spec)

	spoke := k.named(v1alpha1, agent, namespace, "new")
	if got, _, _ := unstructured.NestedSlice(spoke.Object, specKey, keyHarness, keyPlugins); len(got) != 0 {
		t.Errorf("v1alpha1 plugins: got %v", got)
	}
	if spoke.GetAnnotations()[corev1alpha1.ConversionDataAnnotation] == "" {
		t.Error("the v1alpha1 form has no conversion data")
	}
	spoke.SetLabels(map[string]string{labelKey: labelValue})
	if err := k.c.Update(t.Context(), spoke); err != nil {
		t.Fatal(err)
	}
	hub := k.named(v1beta1, agent, namespace, "new")
	if got, _, _ := unstructured.NestedSlice(hub.Object, specKey, keyHarness, keyPlugins); !reflect.DeepEqual(got, plugins) {
		t.Errorf("v1beta1 plugins after the v1alpha1 update: got %v", got)
	}
	if _, found := hub.GetAnnotations()[corev1alpha1.ConversionDataAnnotation]; found || hub.GetLabels()[labelKey] != labelValue {
		t.Errorf("v1beta1 metadata after the v1alpha1 update: %v, %v", hub.GetAnnotations(), hub.GetLabels())
	}
}

func (k *cluster) checkMigration() {
	t := k.t
	t.Helper()
	versions := func() map[string]string {
		out := map[string]string{}
		for kind := range specs {
			out[kind] = k.old(v1beta1, kind).GetResourceVersion()
		}
		return out
	}
	before := versions()
	out := k.migrate()
	if !strings.Contains(out, "Done: 10 CRDs") {
		t.Errorf("got the output:\n%s", out)
	}
	for _, crd := range k.crds() {
		if !reflect.DeepEqual(crd.Status.StoredVersions, []string{v1beta1}) {
			t.Errorf("%s: got the stored versions %v", crd.Name, crd.Status.StoredVersions)
		}
	}
	after := versions()
	for kind := range before {
		if after[kind] == before[kind] {
			t.Errorf("the script did not write the %s again", kind)
		}
	}
	// A second run finds each object in the storage version, so the API server writes nothing.
	k.migrate()
	if again := versions(); !reflect.DeepEqual(again, after) {
		t.Errorf("the second run changed an object: %v, then %v", after, again)
	}
}

func (k *cluster) checkStoppedWebhook() {
	t := k.t
	t.Helper()
	k.stopServer()
	err := k.create(v1alpha1, testKey, namespace, "closed", specs[testKey])
	if err == nil || !strings.Contains(err.Error(), "conversion webhook") {
		t.Errorf("a v1alpha1 TestKey with no webhook: got the error %v", err)
	}
	// The stored objects are v1beta1, so a v1beta1 read and write need no conversion.
	k.old(v1beta1, testKey)
	k.mustCreate(v1beta1, testKey, namespace, "open", object{workspaceID: specs[testKey][workspaceID],
		newID: "run-2", keyBudget: oneUnit, "ttl": halfAnHour})
	// A kind with the strategy None does not use the webhook: the API server converts the
	// stored v1beta1 SpendBudget to v1alpha1 and back.
	old := k.old(v1alpha1, budget)
	old.SetLabels(map[string]string{labelKey: labelValue})
	if err := k.c.Update(t.Context(), old); err != nil {
		t.Errorf("a v1alpha1 SpendBudget with no webhook: %v", err)
	}
}

func (k *cluster) checkRemoval() {
	t := k.t
	t.Helper()
	for _, crd := range k.crds() {
		crd.Spec.Versions = slices.DeleteFunc(crd.Spec.Versions, func(v apix.CustomResourceDefinitionVersion) bool {
			return v.Name == v1alpha1
		})
		if err := k.c.Update(t.Context(), &crd); err != nil {
			t.Errorf("%s: %v", crd.Name, err)
		}
	}
	for kind := range specs {
		k.old(v1beta1, kind)
	}
}
