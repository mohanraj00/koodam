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

// Package upgrade has the upgrade test from the last release (#511). The files with no build tag
// have the test objects and the data checks, and `make check` runs their tests with envtest. The
// file with the build tag "upgrade" has the test on kind, which `make test-upgrade` runs.
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	group = "core.koodam.dev"
	alpha = "v1alpha1"
	beta  = "v1beta1"

	kindAgent     = "Agent"
	kindHold      = "Hold"
	kindProfile   = "Profile"
	kindTestKey   = "TestKey"
	kindWorkspace = "Workspace"

	// The keys of an object.
	keySpec     = "spec"
	keyStatus   = "status"
	keyMetadata = "metadata"
	keyName     = "name"
	// The name of the TestKey field in each version.
	oldID = "testID"
	newID = "testRunID"

	// succeeded is the phase of an Agent that ended with no failure.
	succeeded = "Succeeded"
	// mockModel is the model of the mock upstream of charts/koodam/values-bundled.yaml.
	mockModel = "mock-1"
	// agentImage has a shell. The Agent of the test starts no call.
	agentImage = "docker.io/curlimages/curl:8.11.1"
	// absent is the name of a target that is not there. A Hold and an AppVersion with this
	// target are data only: the controllers start no workload for them.
	absent = "upgrade-absent"
)

// kinds are the 10 kinds of the API group. A value is true if the kind has a namespace.
var kinds = map[string]bool{kindAgent: true, "App": true, "AppVersion": true, "ApprovalRecord": true,
	kindHold: false, kindProfile: false, "SpendBudget": true, "TenantQuota": true, kindTestKey: true,
	kindWorkspace: true}

type object = map[string]any

func newObject(kind, namespace, name string, spec object) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: object{keySpec: spec}}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: alpha, Kind: kind})
	u.SetName(name)
	if kinds[kind] {
		u.SetNamespace(namespace)
	}
	return u
}

// newAgent returns an Agent that ends with no call to the gateway.
func newAgent(namespace, name string) *unstructured.Unstructured {
	return newObject(kindAgent, namespace, name, object{"image": agentImage,
		"command": []any{"sh", "-c", "exit 0"}, "model": mockModel, "budget": "2"})
}

// newWorkspace returns a Workspace with a gateway key and with permission for a TestKey.
func newWorkspace(namespace, name string) *unstructured.Unstructured {
	return newObject(kindWorkspace, namespace, name, object{"storage": "1Gi",
		"gateway":  object{"model": mockModel, "budgetPerDay": "1"},
		"testKeys": object{"maxBudget": "1", "maxTTL": "1h"}})
}

// newTestKey returns a TestKey for a Workspace that is in the cluster.
func newTestKey(name string, workspace *unstructured.Unstructured) *unstructured.Unstructured {
	return newObject(kindTestKey, workspace.GetNamespace(), name, object{
		"workspaceRef": object{keyName: workspace.GetName(), "uid": string(workspace.GetUID())},
		oldID:          name, "budget": "0.5", "ttl": "1h"})
}

// newObjects returns one v1alpha1 object of each kind but the TestKey, which needs the UID of
// the Workspace. The Profile binds the namespace. user is the user of the Hold: the admission
// policy of the chart permits only the name of the client.
func newObjects(namespace, user string) []*unstructured.Unstructured {
	const data = "eA=="
	return []*unstructured.Unstructured{
		newObject(kindProfile, "", "upgrade-"+namespace, object{"namespaces": []any{namespace}}),
		newObject("SpendBudget", namespace, "default", object{"spend": "100"}),
		newWorkspace(namespace, "upgrade-a"),
		newAgent(namespace, "upgrade-a"),
		newObject(kindHold, "", "upgrade-"+namespace, object{"action": "Stop", "user": user,
			"target": object{"kind": kindAgent, "namespace": namespace, keyName: absent},
			"reason": "The upgrade test holds a target that is not there."}),
		newObject("ApprovalRecord", namespace, "upgrade-a", object{"target": absent, "signedRecord": data,
			"snapshotRecord": data, "manifest": data}),
		newObject("App", namespace, "upgrade-a", object{}),
		newObject("AppVersion", namespace, "upgrade-a", object{"appRef": object{keyName: absent},
			"template": object{"podTemplate": object{keySpec: object{"containers": []any{object{
				keyName: "app", "image": agentImage,
				"ports":          []any{object{keyName: "http", "containerPort": int64(8080)}},
				"readinessProbe": object{"httpGet": object{"path": "/", "port": "http"}},
			}}}}}}),
		newObject("TenantQuota", namespace, "upgrade-a", object{"tenant": "upgrade-tenant"}),
	}
}

// errChanged tells the caller to read the object again: it changed between the two reads.
var errChanged = errors.New("the object changed between the read of v1alpha1 and the read of v1beta1")

// sameData compares the v1alpha1 form and the v1beta1 form of one object. The two forms must
// have the same metadata, spec and status. The one permitted difference is the name of the ID
// field of a TestKey.
func sameData(old, hub *unstructured.Unstructured) error {
	if old.GetAPIVersion() != group+"/"+alpha || hub.GetAPIVersion() != group+"/"+beta ||
		old.GetKind() != hub.GetKind() {
		return fmt.Errorf("want one kind in %s and %s, got %s %s and %s %s", alpha, beta,
			old.GetAPIVersion(), old.GetKind(), hub.GetAPIVersion(), hub.GetKind())
	}
	if old.GetResourceVersion() != hub.GetResourceVersion() {
		return errChanged
	}
	want := runtime.DeepCopyJSON(old.Object)
	if spec, ok := want[keySpec].(object); ok && old.GetKind() == kindTestKey {
		if id, ok := spec[oldID]; ok {
			spec[newID] = id
			delete(spec, oldID)
		}
	}
	got := runtime.DeepCopyJSON(hub.Object)
	for _, o := range []object{want, got} {
		// The managed fields name the version of each writer, so they are not object data.
		unstructured.RemoveNestedField(o, keyMetadata, "managedFields")
	}
	for _, key := range []string{keyMetadata, keySpec, keyStatus} {
		if !reflect.DeepEqual(want[key], got[key]) {
			return fmt.Errorf("%s %s: %s is not the same in %s and %s:\n%v\n%v", old.GetKind(), old.GetName(),
				key, alpha, beta, want[key], got[key])
		}
	}
	return nil
}

// read returns the object of o in a version.
func read(ctx context.Context, c client.Client, o *unstructured.Unstructured, version string) (
	*unstructured.Unstructured, error) {
	out := &unstructured.Unstructured{}
	out.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: o.GetKind()})
	return out, c.Get(ctx, client.ObjectKeyFromObject(o), out)
}

// checkTwoVersions reads o in v1alpha1 and in v1beta1 and makes sure that the two forms have the
// same data. A controller can change the object between the two reads, so the check reads again
// when the resource versions are not the same. It returns the v1alpha1 form.
func checkTwoVersions(ctx context.Context, c client.Client, o *unstructured.Unstructured) (
	*unstructured.Unstructured, error) {
	err := errChanged
	for try := 0; try < 20 && errors.Is(err, errChanged); try++ {
		if try > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		var old, hub *unstructured.Unstructured
		if old, err = read(ctx, c, o, alpha); err != nil {
			return nil, err
		}
		if hub, err = read(ctx, c, o, beta); err != nil {
			return nil, err
		}
		if err = sameData(old, hub); err == nil {
			return old, nil
		}
	}
	return nil, err
}

// sameSpec makes sure that the v1alpha1 spec of an object is the spec of before.
func sameSpec(before, after *unstructured.Unstructured) error {
	if before.GetUID() != after.GetUID() || !reflect.DeepEqual(before.Object[keySpec], after.Object[keySpec]) {
		return fmt.Errorf("%s %s: the %s spec changed:\n%v\n%v", before.GetKind(), before.GetName(), alpha,
			before.Object[keySpec], after.Object[keySpec])
	}
	return nil
}
