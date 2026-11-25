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

package spendapi

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

type amounts struct {
	Spend           *float64 `json:"spend,omitempty"`
	Budget          *float64 `json:"budget,omitempty"`
	RemainingBudget *float64 `json:"remainingBudget,omitempty"`
}

type run struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"createdAt"`
	amounts
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (a *api) agents(ctx context.Context, limit int, cursor string) (*corev1alpha1.AgentList, error) {
	list := &corev1alpha1.AgentList{}
	err := a.kube.List(ctx, list, client.InNamespace(a.namespace), client.Limit(limit), client.Continue(cursor))
	return list, err
}

func (a *api) runs(ctx context.Context, limit int, cursor string) (page[run], error) {
	var result page[run]
	scope := runsPath + ":" + a.namespace
	position, err := decodeCursor(scope, cursor)
	if err != nil {
		return result, err
	}
	agents, err := a.agents(ctx, limit, position)
	if err != nil {
		return result, err
	}
	result.Items = make([]run, len(agents.Items))
	if agents.Continue != "" {
		result.NextCursor = encodeCursor(scope, agents.Continue)
	}
	err = parallel(ctx, len(agents.Items), func(ctx context.Context, i int) error {
		agent := &agents.Items[i]
		cost, err := a.runAmounts(ctx, agent)
		id := agent.Status.RunID
		if id == "" {
			id = string(agent.UID)
		}
		result.Items[i] = run{
			ID: id, Name: agent.Name, Namespace: agent.Namespace, CreatedAt: agent.CreationTimestamp.UTC(),
			amounts: cost,
		}
		return err
	})
	return result, err
}

func (a *api) runAmounts(ctx context.Context, agent *corev1alpha1.Agent) (amounts, error) {
	budget := agent.Spec.Budget.AsApproximateFloat64()
	if !finite(budget) {
		return amounts{}, errGateway
	}
	result := amounts{Budget: &budget}
	spend, err := a.runSpend(ctx, agent)
	if spend != nil {
		remaining := max(0, budget-*spend)
		if !finite(remaining) {
			return amounts{}, errGateway
		}
		result.Spend, result.RemainingBudget = spend, &remaining
	}
	return result, err
}

func (a *api) runSpend(ctx context.Context, agent *corev1alpha1.Agent) (*float64, error) {
	if agent.Status.KeyHash != "" {
		hash, err := keyHash(agent.Status.KeyHash)
		if err != nil {
			return nil, err
		}
		spend, err := a.gateway.lifetime(ctx, hash)
		if !errors.Is(err, errMissingKey) {
			return spend, err
		}
	}
	if u := agent.Status.Usage; u != nil {
		if saved, err := strconv.ParseFloat(u.Spend, 64); err == nil && finite(saved) {
			return &saved, nil
		}
	}
	if agent.Status.KeyHash == "" {
		return new(float64), nil
	}
	return nil, nil
}

// parallel stops the remaining reads when a read fails or the request ends.
func parallel(ctx context.Context, count int, read func(context.Context, int) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var workers sync.WaitGroup
	size := min(count, maxGatewayReads)
	for start := range size {
		workers.Go(func() {
			for i := start; i < count && ctx.Err() == nil; i += size {
				if err := read(ctx, i); err != nil {
					cancel(err)
					return
				}
			}
		})
	}
	workers.Wait()
	return context.Cause(ctx)
}
