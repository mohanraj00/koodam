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

package egress

import (
	"crypto/ed25519"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

func appVersion(namespace, name, key string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion(AppVersions.GroupVersion().String())
	u.SetKind("AppVersion")
	u.SetNamespace(namespace)
	u.SetName(name)
	if key != "" {
		_ = unstructured.SetNestedField(u.Object, key, "status", "tenantTokenKey")
	}
	return u
}

func TestAppVersionsAgreeWithTheAPI(t *testing.T) {
	if AppVersions.GroupVersion() != corev1alpha1.GroupVersion {
		t.Fatalf("The resource has the group and version %v.", AppVersions.GroupVersion())
	}
}

func TestTenantKeysWatch(t *testing.T) {
	key, other := tokenKey(t, 1), tokenKey(t, 2)
	pub, _ := key.Public().(ed25519.PublicKey)
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{AppVersions: "AppVersionList"},
		appVersion(ns, appV1, tenanttoken.PublicText(key)),
		appVersion(ns, appV2, ""),
		appVersion(ns, "shop-bad", "not a key"),
		// The proxy does not watch this namespace, so its AppVersion gives no key.
		appVersion("other", appV1, tenanttoken.PublicText(other)))
	keys := NewTenantKeys()
	ctx := t.Context()
	if err := keys.Watch(ctx, client, 0, ns, metav1.NamespaceAll); err != nil {
		t.Fatal(err)
	}
	if got, ok := keys.Key(ns, appV1); !ok || !got.Equal(pub) {
		t.Fatal("The AppVersion with a key in its status must have that key.")
	}
	for _, c := range [][2]string{{ns, appV2}, {ns, "shop-bad"}, {"other", appV1}, {ns, "none"}} {
		if _, ok := keys.Key(c[0], c[1]); ok {
			t.Errorf("%s/%s must have no key.", c[0], c[1])
		}
	}

	// A new key in the status replaces the key. A status with no key, and a delete, remove it.
	versions := client.Resource(AppVersions).Namespace(ns)
	otherPub, _ := other.Public().(ed25519.PublicKey)
	if _, err := versions.Update(ctx, appVersion(ns, appV1, tenanttoken.PublicText(other)), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new key", func() bool { got, ok := keys.Key(ns, appV1); return ok && got.Equal(otherPub) })
	if _, err := versions.Update(ctx, appVersion(ns, appV1, ""), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "no key after the status has none", func() bool { _, ok := keys.Key(ns, appV1); return !ok })
	if _, err := versions.Create(ctx, appVersion(ns, "shop-v3", tenanttoken.PublicText(key)), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the key of a new AppVersion", func() bool { _, ok := keys.Key(ns, "shop-v3"); return ok })
	if err := versions.Delete(ctx, "shop-v3", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "no key after the delete", func() bool { _, ok := keys.Key(ns, "shop-v3"); return !ok })
}

// The hash of the gateway key of an AppVersion comes from its status, in the watched namespaces
// only. A new hash replaces the hash, and a delete removes it.
func TestTenantKeysWatchTheKeyHash(t *testing.T) {
	const hash1, hash2, hash3 = "hash-1", "hash-2", "hash-3"
	withHash := func(namespace, name, hash string) *unstructured.Unstructured {
		u := appVersion(namespace, name, "")
		_ = unstructured.SetNestedField(u.Object, hash, "status", "keyHash")
		return u
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{AppVersions: "AppVersionList"},
		withHash(ns, appV1, hash1), appVersion(ns, appV2, ""), withHash("other", appV1, hash3))
	keys := NewTenantKeys()
	ctx := t.Context()
	if err := keys.Watch(ctx, client, 0, ns); err != nil {
		t.Fatal(err)
	}
	owner := func(hash string) string {
		namespace, version, ok := keys.KeyOwner(hash)
		if !ok {
			return ""
		}
		return namespace + "/" + version
	}
	if owner(hash1) != ns+"/"+appV1 || owner(hash3) != "" || owner("") != "" {
		t.Fatalf("The owners are %q, %q and %q.", owner(hash1), owner(hash3), owner(""))
	}
	versions := client.Resource(AppVersions).Namespace(ns)
	if _, err := versions.Update(ctx, withHash(ns, appV1, hash2), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new hash", func() bool { return owner(hash2) == ns+"/"+appV1 && owner(hash1) == "" })
	if _, err := versions.Update(ctx, withHash(ns, appV2, hash1), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the hash of the second version", func() bool { return owner(hash1) == ns+"/"+appV2 })
	if err := versions.Delete(ctx, appV1, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "no owner after the delete", func() bool { return owner(hash2) == "" && owner(hash1) == ns+"/"+appV2 })
	if _, err := versions.Update(ctx, appVersion(ns, appV2, ""), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "no owner after the status has no hash", func() bool { return owner(hash1) == "" })
}
