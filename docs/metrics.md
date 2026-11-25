# Prometheus metrics

The controller serves metrics at `/metrics`. Use the HTTPS endpoint with authentication and authorization for these
metrics. Set `--metrics-bind-address=:8443` to enable it. The default bind address is `0`, which disables the endpoint.

HTTP mode has no authentication and exposes spend and budget data for all namespaces to any client that can reach it.
Use HTTP only for local debugging. Set `--metrics-secure=false` and bind to `127.0.0.1`, for example with
`--metrics-bind-address=127.0.0.1:8080`.

Each scrape lists Agents in the manager cache with a 5-second time limit. The run and money metrics show the Agents
that exist at scrape time. When an Agent is deleted, its values leave the totals. For a replicated controller,
query one scrape target for the run and money metrics. If the list fails, the collector emits only the error counter.
The collector skips each sample it cannot create and increases the error counter by 1 for each skipped sample.

| Metric | Type | Labels | Example PromQL |
|---|---|---|---|
| `koodam_agent_runs` | Gauge | `namespace`, `phase` | `sum by (namespace) (koodam_agent_runs{phase="Failed"})` |
| `koodam_agent_run_duration_seconds` | Summary | `namespace`, `phase` | See the mean duration query below. |
| `koodam_agent_spend_usd` | Gauge | `namespace`, `model` | `sum by (namespace) (koodam_agent_spend_usd)` |
| `koodam_agent_budget_usd` | Gauge | `namespace`, `model` | `sum by (namespace) (koodam_agent_budget_usd)` |
| `koodam_metrics_scrape_errors_total` | Counter | None | `increase(koodam_metrics_scrape_errors_total[5m])` |

The duration summary uses `status.startedAt` and `status.completedAt` for `Succeeded` and `Failed` runs. Both times
must be set and the end time must be at or after the start time. The summary has a `_count` and a `_sum` in seconds.
They can decrease when an Agent is deleted. Query the current mean duration in seconds with this expression.

```promql
koodam_agent_run_duration_seconds_sum / koodam_agent_run_duration_seconds_count
```

Spend uses finite values from `status.usage.models` and labels each value with the model in that entry. If no entry
has a finite spend value, the collector uses finite `status.usage.spend` for `spec.model` or the first entry in
`spec.models`. Budget uses the declared `spec.budget` and labels it with `spec.model` or the first entry in
`spec.models`. The gateway price table must use USD for the metric names to match the currency.

The collector uses only the `namespace`, `phase` and `model` labels. An empty phase is `Pending`. Any phase outside
the Agent phase list is `Unknown`. The collector uses no key values, key hashes, key aliases, tokens, user names or
login values for labels.
