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
	"math"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/mohanraj00/koodam/api/v1alpha1"
)

const namespaceLabel = "namespace"

var (
	runsDesc = prometheus.NewDesc("koodam_agent_runs",
		"Number of Agents by namespace and phase.", []string{namespaceLabel, "phase"}, nil)
	durationDesc = prometheus.NewDesc("koodam_agent_run_duration_seconds",
		"Duration of finished Agent runs with recorded start and end times.", []string{namespaceLabel, "phase"}, nil)
	spendDesc = prometheus.NewDesc("koodam_agent_spend_usd",
		"Total recorded Agent spend in USD by namespace and model.", []string{namespaceLabel, "model"}, nil)
	budgetDesc = prometheus.NewDesc("koodam_agent_budget_usd",
		"Total declared Agent budget in USD by namespace and model.", []string{namespaceLabel, "model"}, nil)
)

// Collector reads Agent metrics from the manager cache at each scrape.
type Collector struct {
	reader       client.Reader
	scrapeErrors prometheus.Counter
}

// NewCollector makes a collector that uses the cache reader.
func NewCollector(reader client.Reader) *Collector {
	return &Collector{
		reader: reader,
		scrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "koodam_metrics_scrape_errors_total",
			Help: "Number of failed Agent lists and skipped metric samples.",
		}),
	}
}

// Describe sends the metric descriptors to ch.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- runsDesc
	ch <- durationDesc
	ch <- spendDesc
	ch <- budgetDesc
	c.scrapeErrors.Describe(ch)
}

type runStats struct {
	count         uint64
	durationCount uint64
	durationSum   float64
}

// Collect reads Agents from the cache and sends their metrics to ch.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	defer c.scrapeErrors.Collect(ch)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var agents corev1alpha1.AgentList
	err := c.reader.List(ctx, &agents)
	cancel()
	if err != nil {
		c.scrapeErrors.Inc()
		return
	}

	runs := make(map[[2]string]runStats)
	spend := make(map[[2]string]float64)
	budgets := make(map[[2]string]float64)
	for i := range agents.Items {
		agent := &agents.Items[i]
		key := [2]string{agent.Namespace, phaseLabel(agent.Status.Phase)}
		stats := runs[key]
		stats.count++
		if seconds, ok := runDuration(&agent.Status); ok {
			stats.durationCount++
			stats.durationSum += seconds
		}
		runs[key] = stats
		addCosts(agent, spend, budgets)
	}
	send := func(metric prometheus.Metric, err error) {
		if err != nil {
			c.scrapeErrors.Inc()
			return
		}
		ch <- metric
	}
	for key, stats := range runs {
		send(prometheus.NewConstMetric(runsDesc, prometheus.GaugeValue, float64(stats.count), key[:]...))
		if stats.durationCount > 0 {
			send(prometheus.NewConstSummary(durationDesc, stats.durationCount, stats.durationSum, nil, key[:]...))
		}
	}
	for key, value := range spend {
		send(prometheus.NewConstMetric(spendDesc, prometheus.GaugeValue, value, key[:]...))
	}
	for key, value := range budgets {
		send(prometheus.NewConstMetric(budgetDesc, prometheus.GaugeValue, value, key[:]...))
	}
}

func phaseLabel(phase corev1alpha1.AgentPhase) string {
	switch phase {
	case "":
		return string(corev1alpha1.AgentPending)
	case corev1alpha1.AgentPending, corev1alpha1.AgentRunning, corev1alpha1.AgentSucceeded, corev1alpha1.AgentFailed:
		return string(phase)
	default:
		return "Unknown"
	}
}

func runDuration(status *corev1alpha1.AgentStatus) (float64, bool) {
	if status.Phase != corev1alpha1.AgentSucceeded && status.Phase != corev1alpha1.AgentFailed {
		return 0, false
	}
	start, end := status.StartedAt, status.CompletedAt
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0, false
	}
	return end.Sub(start.Time).Seconds(), true
}

func addCosts(agent *corev1alpha1.Agent, spend, budgets map[[2]string]float64) {
	usage := agent.Status.Usage
	hasModelSpend := false
	if usage != nil {
		for _, model := range usage.Models {
			if recordSpend(spend, [2]string{agent.Namespace, model.Model}, model.Spend) {
				hasModelSpend = true
			}
		}
	}
	models := agent.Spec.ModelList()
	if len(models) == 0 {
		return
	}
	key := [2]string{agent.Namespace, models[0]}
	if usage != nil && !hasModelSpend {
		recordSpend(spend, key, usage.Spend)
	}
	// A quantity with a format can declare a zero budget.
	if budget := agent.Spec.Budget; budget.Format != "" || !budget.IsZero() {
		budgets[key] += budget.AsApproximateFloat64()
	}
}

func recordSpend(spend map[[2]string]float64, key [2]string, amount string) bool {
	value, err := strconv.ParseFloat(amount, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	spend[key] += value
	return true
}
