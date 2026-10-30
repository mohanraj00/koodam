/*
Copyright 2026.

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
	"errors"
	"maps"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// The pod labels that the controller sets. The values must agree with internal/controller.
const (
	AgentLabel     = "koodam.dev/agent"
	WorkspaceLabel = "koodam.dev/workspace"
	RunLabel       = "koodam.dev/run"
	UserLabel      = "koodam.dev/user"
	// AppLabel and AppVersionLabel are the reserved labels of an App pod. The values are the names
	// of the App and the AppVersion. A pod with these labels must have no Agent, Workspace or run
	// label. There is no App controller yet. It must use these names.
	AppLabel        = "koodam.dev/app"
	AppVersionLabel = "koodam.dev/app-version"
)

// The headers that a pod can send to claim an identity. The span records only that a claim came,
// and the proxy removes them before the call goes upstream.
const (
	ClaimedRunHeader  = "X-Koodam-Run"
	ClaimedUserHeader = "X-Koodam-User"
)

// Unknown is the identity in a span if the proxy cannot find one pod for the source IP.
const Unknown = "unknown"

var (
	errUnknownIP = errors.New("no pod has the source IP")
	errSharedIP  = errors.New("more than one pod has the source IP")
	errAppLabels = errors.New("the pod with the source IP has incorrect App labels")
)

// podID is the identity that a span takes from a pod. If invalid is true, the pod has a partial
// or mixed App identity. The pod keeps its IPs in the index, and the proxy refuses its calls.
type podID struct {
	namespace, pod, agent, workspace, run, user, app, appVersion string
	invalid                                                      bool
}

// newPodID returns the identity of the pod from its labels. The user is the user label, as for
// each pod. An App identity needs the 2 App labels with values, and no Agent, Workspace or run
// label. If apps is false, a pod with an App label is invalid.
func newPodID(p *corev1.Pod, apps bool) podID {
	l := p.Labels
	id := podID{namespace: p.Namespace, pod: p.Name, user: l[UserLabel]}
	_, app := l[AppLabel]
	_, version := l[AppVersionLabel]
	if !app && !version {
		id.agent, id.workspace, id.run = l[AgentLabel], l[WorkspaceLabel], l[RunLabel]
		return id
	}
	_, agent := l[AgentLabel]
	_, ws := l[WorkspaceLabel]
	_, run := l[RunLabel]
	if !apps || l[AppLabel] == "" || l[AppVersionLabel] == "" || agent || ws || run {
		id.invalid = true
		return id
	}
	id.app, id.appVersion = l[AppLabel], l[AppVersionLabel]
	return id
}

// PodIndex maps a pod IP to the Koodam pod with that IP. Its methods are safe for concurrent use.
// A pod that ends or that has a deletion timestamp leaves the index, because Kubernetes can give
// its IP to a new pod.
type PodIndex struct {
	mu   sync.RWMutex
	byIP map[string]map[string]podID // IP to pod key to identity
	ipOf map[string][]string         // pod key to its IPs
}

// NewPodIndex returns an empty index.
func NewPodIndex() *PodIndex {
	return &PodIndex{byIP: map[string]map[string]podID{}, ipOf: map[string][]string{}}
}

// Set adds the pod, or changes its entry. A pod with no Koodam label, or a pod that is not live,
// leaves the index.
func (x *PodIndex) Set(p *corev1.Pod) {
	x.set(p, true)
}

// set is Set. If apps is false, a pod with an App label keeps its IPs, and the proxy refuses its
// calls.
func (x *PodIndex) set(p *corev1.Pod, apps bool) {
	key := p.Namespace + "/" + p.Name
	x.mu.Lock()
	defer x.mu.Unlock()
	x.remove(key)
	if !live(p) {
		return
	}
	id := newPodID(p, apps)
	var ips []string
	for _, ip := range p.Status.PodIPs {
		if a, err := netip.ParseAddr(ip.IP); err == nil {
			ips = append(ips, a.Unmap().String())
		}
	}
	if a, err := netip.ParseAddr(p.Status.PodIP); err == nil && len(ips) == 0 {
		ips = []string{a.Unmap().String()}
	}
	for _, ip := range ips {
		if x.byIP[ip] == nil {
			x.byIP[ip] = map[string]podID{}
		}
		x.byIP[ip][key] = id
	}
	x.ipOf[key] = ips
}

// Delete removes the pod.
func (x *PodIndex) Delete(namespace, name string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.remove(namespace + "/" + name)
}

func (x *PodIndex) remove(key string) {
	for _, ip := range x.ipOf[key] {
		delete(x.byIP[ip], key)
		if len(x.byIP[ip]) == 0 {
			delete(x.byIP, ip)
		}
	}
	delete(x.ipOf, key)
}

func live(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return false
	}
	_, agent := p.Labels[AgentLabel]
	_, ws := p.Labels[WorkspaceLabel]
	_, app := p.Labels[AppLabel]
	_, version := p.Labels[AppVersionLabel]
	return agent || ws || app || version
}

// Identify fills the identity of the span from the pod with the source IP. It is the Identify
// function of a Proxy. If no pod or more than one pod has the IP, the identity is Unknown and it
// returns an error, thus the proxy refuses the call. It also refuses a pod with incorrect App
// labels. No header can give an identity.
func (x *PodIndex) Identify(r *http.Request, sp *Span) error {
	// The span records only that the pod sent a claim. The value can be a key (#170).
	sp.Claimed = r.Header.Get(ClaimedRunHeader) != "" || r.Header.Get(ClaimedUserHeader) != ""
	r.Header.Del(ClaimedRunHeader)
	r.Header.Del(ClaimedUserHeader)

	x.mu.RLock()
	pods := x.byIP[sp.Source]
	var id podID
	for _, v := range pods {
		id = v
	}
	n := len(pods)
	x.mu.RUnlock()
	switch n {
	case 0:
		sp.Pod, sp.User = Unknown, Unknown
		return errUnknownIP
	case 1:
		if id.invalid {
			sp.Pod, sp.User = Unknown, Unknown
			return errAppLabels
		}
		sp.Namespace, sp.Pod, sp.Agent, sp.Workspace, sp.Run, sp.User, sp.App, sp.AppVersion =
			id.namespace, id.pod, id.agent, id.workspace, id.run, id.user, id.app, id.appVersion
		return nil
	default:
		sp.Pod, sp.User = Unknown, Unknown
		return errSharedIP
	}
}

// Watch fills the index from pod informers. For each named namespace, 1 informer watches all the
// pods of the namespace, thus each change of a pod comes in 1 stream. These informers give the
// Agent, Workspace and App identities. If namespaces is empty or has metav1.NamespaceAll, 2
// informers watch all namespaces: one for the Agent pods and one for the Workspace pods. They skip
// the pods of the named namespaces, and they give no App identity. It needs get, list and watch on
// pods in these namespaces. It returns when the caches are in sync, and the informers stop when
// ctx ends.
func (x *PodIndex) Watch(ctx context.Context, cs kubernetes.Interface, resync time.Duration,
	namespaces ...string) error {
	all := len(namespaces) == 0
	named := map[string]bool{}
	for _, namespace := range namespaces {
		if namespace == metav1.NamespaceAll {
			all = true
		} else {
			named[namespace] = true
		}
	}
	var synced []cache.InformerSynced
	start := func(namespace, label string, h cache.ResourceEventHandler) error {
		f := informers.NewSharedInformerFactoryWithOptions(cs, resync, informers.WithNamespace(namespace),
			informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = label }))
		inf := f.Core().V1().Pods().Informer()
		if _, err := inf.AddEventHandler(h); err != nil {
			return err
		}
		f.Start(ctx.Done())
		synced = append(synced, inf.HasSynced)
		return nil
	}
	// A label selector cannot select the pods with one label or an other, thus the informer of a
	// named namespace has no selector. Set selects the Koodam pods.
	for _, namespace := range slices.Sorted(maps.Keys(named)) {
		if err := start(namespace, "", x.handler(nil, true)); err != nil {
			return err
		}
	}
	if all {
		for _, label := range []string{AgentLabel, WorkspaceLabel} {
			if err := start(metav1.NamespaceAll, label, x.handler(named, false)); err != nil {
				return err
			}
		}
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return errors.New("the pod caches did not sync")
	}
	return nil
}

// handler returns the event handler of an informer. It skips the pods of the namespaces in skip.
// If apps is false, a pod with an App label keeps its IPs, and the proxy refuses its calls.
func (x *PodIndex) handler(skip map[string]bool, apps bool) cache.ResourceEventHandlerFuncs {
	set := func(o any) {
		if p, ok := o.(*corev1.Pod); ok && !skip[p.Namespace] {
			x.set(p, apps)
		}
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    set,
		UpdateFunc: func(_, o any) { set(o) },
		DeleteFunc: func(o any) {
			if d, ok := o.(cache.DeletedFinalStateUnknown); ok {
				if ns, name, err := cache.SplitMetaNamespaceKey(d.Key); err == nil && !skip[ns] {
					x.Delete(ns, name)
				}
				return
			}
			if p, ok := o.(*corev1.Pod); ok && !skip[p.Namespace] {
				x.Delete(p.Namespace, p.Name)
			}
		},
	}
}
