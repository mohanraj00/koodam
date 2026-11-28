# Model registry and response cache: design

Release: v0.6.0.

Status: decided design, not built. The decision is on [#249](https://github.com/mohanraj00/koodam/issues/249). The follow-up issues build it.

## Problem

An operator must control which models each namespace can use, and which exact provider version each model name uses. Today a `model_name` such as `claude` can point to any provider model. A change of `litellm_params.model` changes the version for each key, with no record. Repeated identical calls also cost money, and the gateway has no response cache.

## The current state

- `KeySpec` in `internal/gateway` has `Models`, `Budget`, `BudgetPeriod`, `TTL` and `Metadata`. The controller sends `models` and `max_budget` to `/key/generate` and `/key/update`. The key metadata has the namespace, the object UID, the object name and the cost group.
- The `model` or `models` of an Agent is a LiteLLM `model_name`. The bundled chart renders `gateway.bundled.litellm.modelList` as the LiteLLM `model_list`. In the existing-gateway mode, Koodam has no model list.
- Open PR [#387](https://github.com/mohanraj00/koodam/pull/387) adds a Koodam key (`zone`) on a `modelList` item. The chart removes the key from the route list. A LiteLLM `CustomLogger` hook gives a typed 403 (`RouteOutOfZone`). This design uses the same pattern.

## The registry

The registry is a chart value: Koodam keys on each item of `gateway.bundled.litellm.modelList`.

| Key | Meaning |
| --- | --- |
| `version` | The exact provider model id |
| `namespaces` | The namespaces that can use the model. No list means each namespace of the install. |
| `retireAfter` | The time after which no key can use the model |

Rules:

- The chart removes the Koodam keys from the route list, and renders a registry ConfigMap for the controller.
- `version` is necessary when `namespaces` or `retireAfter` is set.
- `litellm_params.model` must be equal to `version`. The chart refuses a difference.
- For an existing gateway, the value `gateway.models` gives the same registry, with no routes.
- A change of the registry is a chart change. `helm diff` shows it.

The key names are the same as the fields of a possible later `Model` resource, so that resource can come later with no change of meaning.

## The refusals

1. **The controller refusal.** The controller refuses an Agent, a Workspace, a TestKey or an AppVersion whose model is not permitted in its namespace, or is retired. It makes no key. The reason goes to a condition: `ModelNotPermitted` or `ModelRetired`. At `retireAfter`, the controller calls `/key/update` to remove the model from each live key. This refusal works in both gateway modes.
2. **The gateway hook.** In the bundled mode, a LiteLLM hook reads a fixed set of retired model names from the chart values. A call for a retired model gets the status 403 and the typed reason `ModelRetired`, with the retirement time. The hook closes the time between `retireAfter` and the key update. The key `models` list stays the namespace control.

No aliases. A model name maps to one pinned `version`. A version change is a chart change with a record. An alias would change the version with no change of the object, and it would break the cache key.

## The response cache

There is no response cache in this release. A later release can add the scoped cache below.

### Risks that decide the design

- **Scope.** The default LiteLLM cache key is the request: the model, the messages and the parameters. It does not include the key or the namespace (verify). Two namespaces with the same prompt can get the same response. That is a data leak across namespaces.
- **Forged scope.** The cache controls of a request come from the pod, and the code in a pod is not trusted (trust boundary 3 of the threat model in open PR [#407](https://github.com/mohanraj00/koodam/pull/407)). A pod can set `cache.namespace` to the name of a different namespace. Thus the scope must come from the key, not from the request.
- **Version.** The cache key uses `model`, which is the `model_name`. If the route changes its provider model, a hit gives a response of the old version. The cache key must include `version`.
- **Spend.** A hit must record zero provider spend. The spend reads of Koodam use the key spend, so a wrong cost goes to the budget.

### The scoped cache (follow-up)

- LiteLLM cache with `mode: default_off`.
- A pre-call hook removes each `cache` control that the pod sends. Then it sets the cache namespace to `<namespace>/<version>`, from the key metadata and the registry.
- The TTL is a chart value with a maximum. A request cannot make it longer.
- The backend is a chart value: `redis` (a new bundled dependency) or `local` (each LiteLLM replica has its own cache, and a restart clears it).
- A span attribute shows each hit.
- The scope is the namespace. A scope for each tenant of an App waits for [#193](https://github.com/mohanraj00/koodam/issues/193), because the App code sends the tenant.
- The cache keeps prompt and response text at rest. This is a new asset, and it needs a row in the threat model.

Tests before the cache is on by default:

1. Two identical calls in one namespace give one provider call and one hit.
2. The same call in a second namespace gives a second provider call.
3. A pod that sets `cache.namespace` to a different namespace gets a miss.
4. A version change of the route gives a miss.
5. A hit adds zero to the key spend and shows in the span.

## Open points to verify

Check each point in the pinned LiteLLM image (`litellm-database:v1.104.2`) before the build:

- The field names of `litellm_settings.cache` and `cache_params`: `type`, `ttl`, `namespace` and `mode: default_off`.
- The field names of the cache controls of a request: `no-cache`, `no-store`, `ttl` and `namespace`.
- The fields of the default cache key, and that it does not include the key or the namespace.
- That a pre-call hook can remove and set the cache controls before the cache lookup.
- That the spend log writes `cache_hit`, and that the cost of a hit is zero in the key spend.

## Options not chosen

- **A new cluster-scoped `Model` resource** for the registry. It gives RBAC and a status for each entry. It adds a new API type and a second source of truth beside the LiteLLM `model_list`. It can come later with the same field names.
- **A `models` field on the Profile.** A Profile is the binding of one person, not the registry of the operator. It has no version, and a namespace with no Profile gets no control.
- **Model aliases**, with the key `aliases` field or `router_settings.model_group_alias`. They change the version with no change of the object, and they break the cache key.
- **A cache in the egress proxy.** It covers only traced pods, and it needs a full HTTP cache with streaming and spend records outside LiteLLM.
