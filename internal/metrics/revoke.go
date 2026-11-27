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

package metrics

import (
	"context"
	"fmt"
	"slices"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mohanraj00/koodam/internal/gateway"
)

// RevokeKinds are the values of the kind label of koodam_revoke_failures_total. Each one is the
// kind of the resource that owns the gateway key.
var RevokeKinds = []string{"Agent", "AppVersion", "TestKey", "Workspace"}

// RevokeFailures counts the failed key blocks of the revoke paths. Each BlockKey call of the
// controller is in a revoke path, so a failed block is a revoke that must run again.
type RevokeFailures struct {
	failures *prometheus.CounterVec
}

// NewRevokeFailures makes the counter with the value 0 for each kind in RevokeKinds.
func NewRevokeFailures() *RevokeFailures {
	r := &RevokeFailures{failures: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "koodam_revoke_failures_total",
		Help: "Number of failed gateway key blocks in a revoke, by the kind of the key owner.",
	}, []string{"kind"})}
	for _, kind := range RevokeKinds {
		r.failures.WithLabelValues(kind)
	}
	return r
}

// Describe sends the metric descriptors to ch.
func (r *RevokeFailures) Describe(ch chan<- *prometheus.Desc) { r.failures.Describe(ch) }

// Collect sends the counter values to ch.
func (r *RevokeFailures) Collect(ch chan<- prometheus.Metric) { r.failures.Collect(ch) }

// Gateway returns gw with a BlockKey that adds 1 to the counter of kind for each error. The other
// methods do not change. kind must be in RevokeKinds, so that the label has a fixed set of values.
func (r *RevokeFailures) Gateway(gw gateway.Gateway, kind string) gateway.Gateway {
	if !slices.Contains(RevokeKinds, kind) {
		panic(fmt.Sprintf("metrics: the revoke kind %q is not in RevokeKinds", kind))
	}
	return &countedGateway{Gateway: gw, failures: r.failures.WithLabelValues(kind)}
}

type countedGateway struct {
	gateway.Gateway
	failures prometheus.Counter
}

func (g *countedGateway) BlockKey(ctx context.Context, key string) error {
	err := g.Gateway.BlockKey(ctx, key)
	if err != nil {
		g.failures.Inc()
	}
	return err
}
