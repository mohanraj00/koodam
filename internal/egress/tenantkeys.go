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
	"context"
	"crypto/ed25519"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/mohanraj00/koodam/internal/tenanttoken"
)

// AppVersions is the resource of the AppVersions. The values must agree with api/v1alpha1.
var AppVersions = schema.GroupVersionResource{Group: "core.koodam.dev", Version: "v1alpha1", Resource: "appversions"}

// TenantKeys has the public key of the tenant token of each AppVersion. The keys come from the
// status of the AppVersions, which only the controller writes. No data of a pod gives a key.
// Its methods are safe for concurrent use.
//
// It also has the hash of the gateway key of each AppVersion, from the same status.
type TenantKeys struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
	// hashes has the gateway key hash of each AppVersion. owners has the AppVersion of each hash.
	hashes map[string]string
	owners map[string][2]string
}

// NewTenantKeys returns an empty set of keys.
func NewTenantKeys() *TenantKeys {
	return &TenantKeys{keys: map[string]ed25519.PublicKey{}, hashes: map[string]string{}, owners: map[string][2]string{}}
}

// KeyOwner gives the AppVersion that has the gateway key with this hash. It is the AppKeyOwner
// function of a Proxy.
func (k *TenantKeys) KeyOwner(keyHash string) (namespace, appVersion string, ok bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	owner, ok := k.owners[keyHash]
	return owner[0], owner[1], ok
}

// setHash replaces the gateway key hash of an AppVersion. An empty hash removes it. The caller
// has the lock.
func (k *TenantKeys) setHash(namespace, version, hash string) {
	name := namespace + "/" + version
	if old, ok := k.hashes[name]; ok && k.owners[old] == [2]string{namespace, version} {
		delete(k.owners, old)
	}
	delete(k.hashes, name)
	if hash != "" {
		k.hashes[name], k.owners[hash] = hash, [2]string{namespace, version}
	}
}

// Key gives the public key of an AppVersion. It is the TenantKey function of a Proxy. With no
// valid key in the status of the AppVersion, ok is false, and the proxy refuses the call.
func (k *TenantKeys) Key(namespace, appVersion string) (key ed25519.PublicKey, ok bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	key, ok = k.keys[namespace+"/"+appVersion]
	return key, ok
}

// set takes the key and the gateway key hash from the status of the AppVersion. An AppVersion
// with no valid key has no key entry.
func (k *TenantKeys) set(u *unstructured.Unstructured) {
	text, _, _ := unstructured.NestedString(u.Object, "status", "tenantTokenKey")
	key, err := tenanttoken.PublicKey(text)
	hash, _, _ := unstructured.NestedString(u.Object, "status", "keyHash")
	name := u.GetNamespace() + "/" + u.GetName()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.setHash(u.GetNamespace(), u.GetName(), hash)
	if err != nil {
		delete(k.keys, name)
		return
	}
	k.keys[name] = key
}

func (k *TenantKeys) remove(namespace, name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, namespace+"/"+name)
	k.setHash(namespace, name, "")
}

// Watch fills the keys from one informer on the AppVersions of each named namespace. Only the
// pods of a named namespace can have an App identity, so the other namespaces have no key. It
// needs get, list and watch on appversions in these namespaces. It returns when the caches are
// in sync, and the informers stop when ctx ends.
func (k *TenantKeys) Watch(ctx context.Context, client dynamic.Interface, resync time.Duration, namespaces ...string) error {
	named := map[string]bool{}
	for _, namespace := range namespaces {
		if namespace != metav1.NamespaceAll {
			named[namespace] = true
		}
	}
	set := func(o any) {
		if u, ok := o.(*unstructured.Unstructured); ok {
			k.set(u)
		}
	}
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    set,
		UpdateFunc: func(_, o any) { set(o) },
		DeleteFunc: func(o any) {
			if d, ok := o.(cache.DeletedFinalStateUnknown); ok {
				if ns, name, err := cache.SplitMetaNamespaceKey(d.Key); err == nil {
					k.remove(ns, name)
				}
				return
			}
			if u, ok := o.(*unstructured.Unstructured); ok {
				k.remove(u.GetNamespace(), u.GetName())
			}
		},
	}
	var synced []cache.InformerSynced
	for _, namespace := range slices.Sorted(maps.Keys(named)) {
		f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(client, resync, namespace, nil)
		inf := f.ForResource(AppVersions).Informer()
		if _, err := inf.AddEventHandler(handler); err != nil {
			return err
		}
		f.Start(ctx.Done())
		synced = append(synced, inf.HasSynced)
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return errors.New("the AppVersion caches did not sync")
	}
	return nil
}
