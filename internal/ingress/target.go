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

package ingress

import (
	"errors"
	"maps"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mohanraj00/koodam/internal/agentingress"
)

// The errors have fixed text. They never contain a name, a UID, an address, a port, an API error,
// a validator error or a status value. The server will map ErrTargetAbsent to 404 and
// ErrTargetUnavailable to 503. ErrTargetConfig is a startup refusal.
var (
	ErrTargetConfig      = errors.New("the ingress target configuration is not valid")
	ErrTargetAbsent      = errors.New("the ingress target does not exist")
	ErrTargetUnavailable = errors.New("the ingress target is not available")
)

// targetResolver finds the target of an ingress route. It reads only through reader, and only in
// the configured run namespaces. It keeps no result between two calls. It never writes an object.
//
// reader must be a direct API client with no cache, for example the manager API reader or a client
// from client.New. The cached manager client is not permitted: a cache can return an old Agent,
// Job, pod or NetworkPolicy. The constructor and the tests cannot prove that a reader has no cache;
// the later wiring must supply the direct client.
type targetResolver struct {
	reader client.Reader
	// guards has one Agent NetworkPolicy guard for each run namespace. The key is the namespace.
	guards map[string]agentingress.PolicyGuard
}

// newTargetResolver returns a resolver with reader and one guard for each explicit run namespace.
// It returns ErrTargetConfig if reader is nil, if guards is empty, if a guard is not valid, if two
// guards have the same namespace, or if the guards do not have the same policy name, proxy
// namespace and proxy labels. A guard namespace must be a valid namespace name, thus a wildcard
// or an empty namespace refuses. There is no all-namespace fallback. The resolver keeps a copy of
// each guard and its label map, thus a later change to guards does not change the resolver.
func newTargetResolver(reader client.Reader, guards []agentingress.PolicyGuard) (*targetResolver, error) {
	if reader == nil || len(guards) == 0 {
		return nil, ErrTargetConfig
	}
	first := guards[0]
	byNamespace := make(map[string]agentingress.PolicyGuard, len(guards))
	for _, g := range guards {
		if g.Validate() != nil || len(apivalidation.ValidateNamespaceName(g.Namespace, false)) > 0 {
			return nil, ErrTargetConfig
		}
		if _, ok := byNamespace[g.Namespace]; ok {
			return nil, ErrTargetConfig
		}
		if g.Name != first.Name || g.ProxyNamespace != first.ProxyNamespace ||
			!maps.Equal(g.ProxyLabels, first.ProxyLabels) {
			return nil, ErrTargetConfig
		}
		g.ProxyLabels = maps.Clone(g.ProxyLabels)
		byNamespace[g.Namespace] = g
	}
	return &targetResolver{reader: reader, guards: byNamespace}, nil
}
