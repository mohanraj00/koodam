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
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/mohanraj00/koodam/api/v1beta1"
)

const costGroupLabel = "koodam.dev/cost-group"

type amounts struct {
	Spend           *float64 `json:"spend,omitempty"`
	Budget          *float64 `json:"budget,omitempty"`
	RemainingBudget *float64 `json:"remainingBudget,omitempty"`
}

type run struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Namespace   string     `json:"namespace"`
	CreatedAt   time.Time  `json:"createdAt"`
	Phase       string     `json:"phase,omitempty"`
	Models      []string   `json:"models,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	amounts
}

func utc(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (a *api) agents(ctx context.Context, namespace string, limit int, cursor string) (*corev1beta1.AgentList, error) {
	list := &corev1beta1.AgentList{}
	err := a.kube.List(ctx, list, client.InNamespace(namespace), client.Limit(limit), client.Continue(cursor))
	return list, err
}

// runs reads the namespaces in order. A cursor holds a namespace and its Kubernetes position.
func (a *api) runs(ctx context.Context, namespaces []string, limit int, cursor string) (page[run], error) {
	var result page[run]
	scope := runsPath + ":" + strings.Join(namespaces, ",")
	position, err := decodeCursor(scope, cursor)
	if err != nil {
		return result, err
	}
	start, next := 0, ""
	if position != "" {
		namespace, rest, _ := strings.Cut(position, "/")
		// The caller can change a cursor, so permit only a namespace in its scope.
		if start = slices.Index(namespaces, namespace); start < 0 {
			return result, errCursor
		}
		position = rest
	}
	var agents []corev1beta1.Agent
	for i := start; i < len(namespaces); i++ {
		if len(agents) == limit {
			next = namespaces[i] + "/"
			break
		}
		list, err := a.agents(ctx, namespaces[i], limit-len(agents), position)
		if err != nil {
			return result, err
		}
		agents, position = append(agents, list.Items...), ""
		if list.Continue != "" {
			next = namespaces[i] + "/" + list.Continue
			break
		}
	}
	result.Items = make([]run, len(agents))
	if next != "" {
		result.NextCursor = encodeCursor(scope, next)
	}
	err = parallel(ctx, len(agents), func(ctx context.Context, i int) error {
		agent := &agents[i]
		cost, err := a.runAmounts(ctx, agent)
		id := agent.Status.RunID
		if id == "" {
			id = string(agent.UID)
		}
		models := agent.Spec.Models
		if agent.Spec.Model != "" {
			models = []string{agent.Spec.Model}
		}
		result.Items[i] = run{
			ID: id, Name: agent.Name, Namespace: agent.Namespace, CreatedAt: agent.CreationTimestamp.UTC(),
			Phase: string(agent.Status.Phase), Models: models,
			StartedAt: utc(agent.Status.StartedAt), CompletedAt: utc(agent.Status.CompletedAt),
			amounts: cost,
		}
		return err
	})
	return result, err
}

func (a *api) runAmounts(ctx context.Context, agent *corev1beta1.Agent) (amounts, error) {
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

func (a *api) runSpend(ctx context.Context, agent *corev1beta1.Agent) (*float64, error) {
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

// byNamespace groups the period spend by the Agent namespace in place of a label.
const byNamespace = "namespace"

type period struct {
	from, to time.Time
	// label is a label name, or empty for byNamespace.
	label string
}

func (p period) groupBy() string {
	if p.label == "" {
		return byNamespace
	}
	return "label:" + p.label
}

func parsePeriod(q url.Values) (period, bool) {
	from, fromErr := time.Parse(time.DateOnly, q.Get("from"))
	to, toErr := time.Parse(time.DateOnly, q.Get("to"))
	label, ok := strings.CutPrefix(q.Get("groupBy"), "label:")
	if q.Get("groupBy") == byNamespace {
		label, ok = "", true
	} else if len(validation.IsQualifiedName(label)) != 0 {
		ok = false
	}
	return period{from: from, to: to, label: label},
		fromErr == nil && toErr == nil && from.Year() > 0 && to.Year() < 9999 && !from.After(to) && ok
}

type group struct {
	LabelValue string `json:"labelValue"`
	amounts
}

type periodPage struct {
	From    string `json:"from"`
	To      string `json:"to"`
	GroupBy string `json:"groupBy"`
	page[group]
}

func (a *api) spend(ctx context.Context, namespaces []string, p period, limit int, cursor string) (periodPage, error) {
	result := periodPage{From: p.from.Format(time.DateOnly), To: p.to.Format(time.DateOnly), GroupBy: p.groupBy()}
	scope := strings.Join([]string{spendPath, strings.Join(namespaces, ","), result.From, result.To, result.GroupBy}, ":")
	position, err := decodeCursor(scope, cursor)
	if err != nil {
		return result, err
	}
	keys, err := a.groupKeys(ctx, namespaces, p, position, cursor != "")
	if err != nil {
		return result, err
	}
	names := slices.Sorted(maps.Keys(keys))
	if len(names) > limit {
		names = names[:limit]
		result.NextCursor = encodeCursor(scope, names[limit-1])
	}
	result.Items, err = a.groupSpend(ctx, p, names, keys)
	return result, err
}

// groupKeys lists the Agents in pages. Save only the group names and the key hashes.
func (a *api) groupKeys(
	ctx context.Context, namespaces []string, p period, after string, continued bool,
) (map[string][]string, error) {
	keys := map[string][]string{}
	end := p.to.AddDate(0, 0, 1)
	for n, cursor := 0, ""; n < len(namespaces); {
		agents, err := a.agents(ctx, namespaces[n], maxPage, cursor)
		if err != nil {
			return nil, err
		}
		for _, agent := range agents.Items {
			if !agent.CreationTimestamp.Time.Before(end) {
				continue
			}
			label := agent.Labels[p.label]
			switch p.label {
			case costGroupLabel:
				label = agent.Status.CostGroup
			case "":
				label = agent.Namespace
			}
			if continued && label <= after {
				continue
			}
			keys[label] = append(keys[label], agent.Status.KeyHash)
		}
		if cursor = agents.Continue; cursor == "" {
			n++
		}
	}
	return keys, nil
}

func (a *api) groupSpend(ctx context.Context, p period, names []string, keys map[string][]string) ([]group, error) {
	type key struct {
		group int
		hash  string
	}
	items := make([]group, len(names))
	var tasks []key
	for i, name := range names {
		items[i] = group{LabelValue: name, amounts: amounts{Spend: new(float64)}}
		for _, hash := range keys[name] {
			if hash != "" {
				tasks = append(tasks, key{group: i, hash: hash})
			}
		}
	}
	end := p.to.AddDate(0, 0, 1)
	costs := make([]float64, len(tasks))
	err := parallel(ctx, len(tasks), func(ctx context.Context, i int) error {
		hash, err := keyHash(tasks[i].hash)
		if err != nil {
			return err
		}
		costs[i], err = a.gateway.period(ctx, hash, p.from, end)
		return err
	})
	if err != nil {
		return nil, err
	}
	for i, task := range tasks {
		spend := items[task.group].Spend
		*spend += costs[i]
		if !finite(*spend) {
			return nil, errGateway
		}
	}
	return items, nil
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
