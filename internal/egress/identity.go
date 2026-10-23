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
	"net/http"
	"net/netip"
	"strings"
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
)

// The headers that a pod can send to claim an identity. The span records them as claims only,
// and the proxy removes them before the call goes upstream.
const (
	ClaimedRunHeader  = "X-Koodam-Run"
	ClaimedUserHeader = "X-Koodam-User"
)

// Unknown is the identity in a span if the proxy cannot find one pod for the source IP.
const Unknown = "unknown"

// maxClaim is the maximum length of a claimed value in a span. It is the label value limit.
const maxClaim = 63

var (
	errUnknownIP = errors.New("no pod has the source IP")
	errSharedIP  = errors.New("more than one pod has the source IP")
)

// podID is the identity that a span takes from a pod.
type podID struct {
	namespace, pod, agent, workspace, run, user string
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
	key := p.Namespace + "/" + p.Name
	x.mu.Lock()
	defer x.mu.Unlock()
	x.remove(key)
	if !live(p) {
		return
	}
	id := podID{namespace: p.Namespace, pod: p.Name, agent: p.Labels[AgentLabel],
		workspace: p.Labels[WorkspaceLabel], run: p.Labels[RunLabel], user: p.Labels[UserLabel]}
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
	return agent || ws
}

// Identify fills the identity of the span from the pod with the source IP. It is the Identify
// function of a Proxy. If no pod or more than one pod has the IP, the identity is Unknown and it
// returns an error, thus the proxy refuses the call.
func (x *PodIndex) Identify(r *http.Request, sp *Span) error {
	sp.ClaimedRun = claim(r.Header.Get(ClaimedRunHeader))
	sp.ClaimedUser = claim(r.Header.Get(ClaimedUserHeader))
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
		sp.Namespace, sp.Pod, sp.Agent, sp.Workspace, sp.Run, sp.User =
			id.namespace, id.pod, id.agent, id.workspace, id.run, id.user
		return nil
	default:
		sp.Pod, sp.User = Unknown, Unknown
		return errSharedIP
	}
}

// claim limits a claimed value to maxClaim bytes, and does not cut a UTF-8 character.
func claim(v string) string {
	if len(v) > maxClaim {
		v = v[:maxClaim]
	}
	return strings.ToValidUTF8(v, "")
}

// Watch fills the index from 2 pod informers in all namespaces: one for the Agent pods and one
// for the Workspace pods. It needs get, list and watch on pods. It returns when the caches are
// in sync, and the informers stop when ctx ends.
func (x *PodIndex) Watch(ctx context.Context, cs kubernetes.Interface, resync time.Duration) error {
	var synced []cache.InformerSynced
	for _, label := range []string{AgentLabel, WorkspaceLabel} {
		f := informers.NewSharedInformerFactoryWithOptions(cs, resync,
			informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = label }))
		inf := f.Core().V1().Pods().Informer()
		if _, err := inf.AddEventHandler(x.handler()); err != nil {
			return err
		}
		f.Start(ctx.Done())
		synced = append(synced, inf.HasSynced)
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return errors.New("the pod caches did not sync")
	}
	return nil
}

func (x *PodIndex) handler() cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { x.set(o) },
		UpdateFunc: func(_, o any) { x.set(o) },
		DeleteFunc: func(o any) {
			if d, ok := o.(cache.DeletedFinalStateUnknown); ok {
				if ns, name, err := cache.SplitMetaNamespaceKey(d.Key); err == nil {
					x.Delete(ns, name)
				}
				return
			}
			if p, ok := o.(*corev1.Pod); ok {
				x.Delete(p.Namespace, p.Name)
			}
		},
	}
}

func (x *PodIndex) set(o any) {
	if p, ok := o.(*corev1.Pod); ok {
		x.Set(p)
	}
}
