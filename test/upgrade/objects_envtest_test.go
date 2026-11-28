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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
	"github.com/mohanraj00/koodam/internal/conversion"
)

const (
	testNS   = "upgrade-unit"
	testUser = "tester"
)

// pair returns the two forms of a TestKey with the resource version 1.
func pair() (*unstructured.Unstructured, *unstructured.Unstructured) {
	workspace := newWorkspace(testNS, "w")
	workspace.SetUID("uid-1")
	old := newTestKey("key-1", workspace)
	old.SetResourceVersion("1")
	old.Object[keyStatus] = object{"keyHash": "hash"}
	hub := old.DeepCopy()
	hub.SetAPIVersion(group + "/" + beta)
	spec := hub.Object[keySpec].(object)
	spec[newID] = spec[oldID]
	delete(spec, oldID)
	return old, hub
}

func TestSameData(t *testing.T) {
	if old, hub := pair(); sameData(old, hub) != nil {
		t.Errorf("the two forms of one TestKey are not the same: %v", sameData(old, hub))
	}
	cases := map[string]func(old, hub *unstructured.Unstructured){
		"a different ID":       func(_, hub *unstructured.Unstructured) { hub.Object[keySpec].(object)[newID] = "other" },
		"the old ID name":      func(_, hub *unstructured.Unstructured) { hub.Object[keySpec].(object)[oldID] = "key-1" },
		"no spec field":        func(_, hub *unstructured.Unstructured) { delete(hub.Object[keySpec].(object), "budget") },
		"a different status":   func(_, hub *unstructured.Unstructured) { hub.Object[keyStatus] = object{"keyHash": "b"} },
		"no status":            func(_, hub *unstructured.Unstructured) { delete(hub.Object, keyStatus) },
		"a different label":    func(_, hub *unstructured.Unstructured) { hub.SetLabels(map[string]string{"a": "b"}) },
		"a different UID":      func(_, hub *unstructured.Unstructured) { hub.SetUID("uid-2") },
		"a different kind":     func(_, hub *unstructured.Unstructured) { hub.SetKind(kindAgent) },
		"v1alpha1 two times":   func(old, hub *unstructured.Unstructured) { hub.Object = old.DeepCopy().Object },
		"a different revision": func(_, hub *unstructured.Unstructured) { hub.SetResourceVersion("2") },
	}
	for name, change := range cases {
		old, hub := pair()
		change(old, hub)
		err := sameData(old, hub)
		if err == nil || errors.Is(err, errChanged) != (name == "a different revision") {
			t.Errorf("%s: got the error %v", name, err)
		}
	}
	old, hub := pair()
	old.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "a", APIVersion: group + "/" + alpha}})
	hub.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "a", APIVersion: group + "/" + beta}})
	if err := sameData(old, hub); err != nil {
		t.Errorf("the managed fields are not object data: %v", err)
	}
}

func TestSameSpec(t *testing.T) {
	before, _ := pair()
	after := before.DeepCopy()
	after.Object[keyStatus] = object{}
	if err := sameSpec(before, after); err != nil {
		t.Errorf("a status change is not a spec change: %v", err)
	}
	after.Object[keySpec].(object)["budget"] = "1"
	if sameSpec(before, after) == nil {
		t.Error("the check did not find the spec change")
	}
	after = before.DeepCopy()
	after.SetUID("uid-2")
	if sameSpec(before, after) == nil {
		t.Error("the check did not find the new object")
	}
}

// apiServer starts an API server with the CRDs of this tree and the conversion webhook. It is
// the state of a cluster after the upgrade: the CRDs serve v1alpha1 and v1beta1.
func apiServer(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	adds := []func(*runtime.Scheme) error{corev1.AddToScheme, corev1alpha1.AddToScheme, corev1beta1.AddToScheme}
	for _, add := range adds {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	// With the two versions in the scheme, envtest gives each CRD the conversion webhook.
	env := &envtest.Environment{Scheme: scheme, ErrorIfCRDPathMissing: true, CRDDirectoryPaths: []string{
		filepath.Join("..", "..", "config", "crd", "bases"), filepath.Join("..", "..", "config", "apps", "crd", "bases")}}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// `make test` sets the variable. With no variable, use the binaries of `make setup-envtest`.
		dirs, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "*"))
		if len(dirs) == 0 {
			if os.Getenv("CI") != "" {
				t.Fatal("the envtest binaries are not there")
			}
			t.Skip("the envtest binaries are not there: run `make setup-envtest`")
		}
		env.BinaryAssetsDirectory = dirs[0]
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if err := env.Stop(); err != nil {
			t.Errorf("the API server did not stop: %v", err)
		}
	})
	hook := env.WebhookInstallOptions
	server := conversion.NewServer(hook.LocalServingHost, hook.LocalServingPort, hook.LocalServingCertDir, func(string) {})
	go func() { _ = server.Start(ctx) }()
	for deadline := time.Now().Add(30 * time.Second); server.StartedChecker()(nil) != nil; {
		if time.Now().After(deadline) {
			t.Fatal("the conversion webhook did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestObjectsInTheTwoVersions makes each test object in v1alpha1 and reads it in the two
// versions. It makes sure that the objects of the upgrade test are valid, that they include each
// kind, and that the data check accepts the result of the conversion webhook.
func TestObjectsInTheTwoVersions(t *testing.T) {
	c := apiServer(t)
	ctx := t.Context()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}); err != nil {
		t.Fatal(err)
	}
	objects := newObjects(testNS, testUser)
	for i := 0; i < len(objects); i++ {
		o := objects[i]
		if err := c.Create(ctx, o); err != nil {
			t.Fatalf("%s %s: %v", o.GetKind(), o.GetName(), err)
		}
		// The TestKey needs the UID that the API server gave the Workspace.
		if o.GetKind() == kindWorkspace {
			objects = append(objects, newTestKey("upgrade-a", o))
		}
	}
	// A status must go through the conversion too.
	agent := objects[3]
	agent.Object[keyStatus] = object{"phase": succeeded}
	if err := c.Status().Update(ctx, agent); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, o := range objects {
		seen[o.GetKind()] = true
		old, err := checkTwoVersions(ctx, c, o)
		if err != nil {
			t.Errorf("%s %s: %v", o.GetKind(), o.GetName(), err)
			continue
		}
		if err := sameSpec(old, old); err != nil || old.GetUID() != o.GetUID() {
			t.Errorf("%s %s: the read is not the object of the create: %v", o.GetKind(), o.GetName(), err)
		}
		hub, err := read(ctx, c, o, beta)
		if err != nil {
			t.Fatal(err)
		}
		id, _, _ := unstructured.NestedString(hub.Object, keySpec, newID)
		phase, _, _ := unstructured.NestedString(hub.Object, keyStatus, "phase")
		if (o.GetKind() == kindTestKey) != (id == "upgrade-a") || (o.GetKind() == kindAgent) != (phase == succeeded) {
			t.Errorf("%s %s: got the v1beta1 ID %q and the phase %q", o.GetKind(), o.GetName(), id, phase)
		}
	}
	for kind := range kinds {
		if !seen[kind] {
			t.Errorf("the test objects have no %s", kind)
		}
	}
	if len(seen) != len(kinds) {
		t.Errorf("the test objects have %d kinds, want %d", len(seen), len(kinds))
	}
}
