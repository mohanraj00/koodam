# Tenants

A tenant is a client group of an App. The tenant label comes from the signed claim that the claim check of the App verifies ([apps.md](apps.md#the-signed-claim-check)). It does not come from a ServiceAccount. A `TenantQuota` gives one tenant label a quota for each period and a rate limit. Koodam uses the tenant label only for the quota, the rate limit and the spend group. The application isolates the data of its tenants.

## What is active

- The `TenantQuota` API is in `main`. The chart installs its CRD with `apps.enabled=true`.
- The claim check container can refuse a tenant from a limits file. See "The refusals of the claim check".
- With `apps.enabled`, the TenantQuota controller makes the gateway customer of each tenant label and writes the use to the status. See "The gateway customer" and "The controller".
- With a tenant claim (`apps.claimCheck.tenantClaim`), each AppVersion pod gets the limits file of its tenants. Then the claim check refuses a tenant above its rate, a tenant at its quota and, if the chart says so, an unknown tenant. See "The limits file" and "A tenant with no TenantQuota".
- The spend group `tenant` of the spend report comes in a later change of [#193](https://github.com/mohanraj00/koodam/issues/193).

## The TenantQuota spec

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: TenantQuota
metadata:
  name: acme
spec:
  tenant: acme
  appRef:
    name: shop
  period: Day
  requests: 10000
  spend: "5"
  rateLimit:
    requestsPerSecond: "5"
    burst: 10
```

| Field | Rule |
|---|---|
| `tenant` | Necessary. The tenant label of the signed claim, with 1 to 253 characters and a maximum of 253 bytes in UTF-8. You cannot change it after create. |
| `appRef.name` | Optional. The App that the rate limit and the claim check refusals apply to. With no value, they apply to each App of the namespace. |
| `period` | `Hour` or `Day`. The default is `Day`. The gateway sets the start of each period. |
| `requests` | Optional. The maximum count of model calls of the tenant in one period. The minimum is 1. |
| `spend` | Optional. The maximum spend of the tenant in one period, in the currency of the gateway price table. It must be more than zero. |
| `rateLimit.requestsPerSecond` | Necessary with `rateLimit`. A quantity more than zero, for example `"5"` or `"500m"`. |
| `rateLimit.burst` | Optional. The count of requests that the tenant can send at one time. With no value, it is the rate, with a minimum of 1. |

A `TenantQuota` with no limit only makes the tenant label a known tenant.

The gateway has one customer for each tenant label of the install. Thus use one `TenantQuota` for each tenant label. A `TenantQuota` sets the gateway budget of its tenant label for the full install, so give the right to create one only to the operator of the install.

[config/samples/core_v1alpha1_tenantquota.yaml](../config/samples/core_v1alpha1_tenantquota.yaml) is an example.

## The status

| Field | Meaning |
|---|---|
| `conditions` | `Ready` shows that the gateway has the customer of the tenant with the limits of the spec. `QuotaExceeded` shows that the tenant is at a limit of the current period. |
| `periodEnd` | The time when the gateway starts the next period of the tenant. |
| `requests` | The count of model calls of the tenant in the current period. |
| `spend` | The spend of the tenant in the current period. |
| `observedTime` | The time when the controller last wrote a change of the status. |

## The refusals of the claim check

The claim check container `koodam-claim` reads the limits of the tenants from a file, and it reads the file again each 10 seconds. A file that is not valid keeps the last valid limits. The container does not start with a file that is not valid.

The check examines a request after it verified the signed claim:

| Reason | Status | Condition |
|---|---|---|
| `UnknownTenant` | `403` | The file does not name the tenant label, and the file refuses an unknown tenant. |
| `TenantQuotaExceeded` | `429` | The file marks the tenant at its limit of the current period. |
| `TenantRateLimited` | `429` | The token bucket of the tenant is empty. The header `Retry-After` gives the seconds until the next token. |

- The answer has the reason in the header `Koodam-Reason` and in the body. The app container does not get a refused request.
- The ingress span of the request has the reason as its `outcome`, with the caller label and the tenant label ([app-ingress-spans.md](app-ingress-spans.md)).
- Each pod has its own token bucket for each tenant. A refused request uses no token, and the limit of one tenant has no effect on a different tenant.
- A request with no tenant label has no tenant limit. This occurs only if the claim check has no tenant claim.

## The limits file

With `apps.claimCheck.tenantClaim`, the controller writes one ConfigMap for each AppVersion, with the name `kd-<version>-tenants`. The check container of each pod of the version mounts it as its limits file.

- The file has each `TenantQuota` of the namespace of the version that applies to its App: a `TenantQuota` with no `appRef`, or with the `appRef` of the App. A tenant label of a different App or a different namespace is an unknown tenant for the version.
- Each pod gets the rate divided by `spec.replicas` of the version. The burst of a pod is the burst divided by the replicas, to the next full number, with a minimum of 1. Thus the sum of the bursts can be more than the burst of the tenant.
- The file has the mark of the condition `QuotaExceeded`. While the mark is set, the claim check refuses each request of the tenant with `TenantQuotaExceeded`. Only the status `True` sets the mark. The status `Unknown` (the reason `RequestCountCapped`) sets no mark.
- A change of a `TenantQuota`, of its mark or of `spec.replicas` changes the ConfigMap. The kubelet then updates the file in the pods, and the check container reads it again. This can take more than 1 minute. The pods do not start again.
- A ConfigMap has a maximum of 1 MiB. This is space for some thousand tenants of one App.
- Without a tenant claim, the claim check has no tenant label, and a pod gets no limits file.

## A tenant with no TenantQuota

By default, the claim check accepts a tenant label with no `TenantQuota`, with no limit. The chart can change this:

```yaml
tenants:
  defaultQuota:
    rateLimit:
      requestsPerSecond: "5"
      burst: 10
  refuseUnknown: false
  usageInterval: 30s
```

- `tenants.defaultQuota.rateLimit` gives such a tenant a rate limit. Each tenant label has its own token bucket. The gateway has no customer for such a tenant, so it has no quota for each period.
- `tenants.refuseUnknown: true` refuses such a tenant with the status 403 and the reason `UnknownTenant`. Then `defaultQuota` has no effect.
- `tenants.usageInterval` is the time between 2 reads of the use from the gateway.
- `defaultQuota` and `refuseUnknown` need `apps.claimCheck.issuer` and `apps.claimCheck.tenantClaim`. The chart refuses them without these values.

## The gateway customer

The quota for each period is in the gateway. The gateway has one customer for each tenant label, with these values from the `TenantQuota`:

| Gateway value | Source |
|---|---|
| The budget of the period | `spend`. With no value, the customer has no spend limit. |
| The length of the period | `period`. A period of an hour starts at the full hour. A period of a day starts at 00:00 UTC. |
| The model calls for each minute | `rateLimit.requestsPerSecond` times 60. With no value, the customer has no rate. |

- The app sends the tenant label of the header `Koodam-Tenant` as the `user` field of each model call. Then the gateway counts the call for the customer. A call with no `user` field is not in the count of a tenant.
- The gateway refuses a model call of a customer at its budget with the error type `budget_exceeded`. This is the refusal `TenantQuotaExceeded` of the gateway. It refuses a call above the rate with the status 429.
- The spend is the count of the gateway for the current period. The requests are the successful model calls of the tenant label in the spend log, from the start of that period.
- The gateway stops the count of the spend log at a maximum (`SPEND_LOGS_PAGINATION_COUNT_CAP` in LiteLLM), and then it sets the flag `total_is_capped`. The client gives this flag with the count. A count with the flag is a minimum, not an exact count.
- Koodam owns one LiteLLM budget for each tenant label, with the ID `kd-tenant-` and a hash of the label. It changes no other budget. A customer that the gateway already has keeps its spend when it gets this budget.
- The tenant label is in the URL of 2 read calls to the gateway, so it can be in the access log of the gateway. The label is not a credential.

## The controller

The TenantQuota controller runs with `apps.enabled`. For each `TenantQuota` it does these steps:

1. It adds the finalizer `core.koodam.dev/tenantquota-customer`.
2. It makes the gateway customer of the tenant label with the limits of the spec. After a change of the spec, it sets the limits again.
3. Each 30 seconds, it reads the spend and the requests of the current period from the gateway and writes them to the status. The manager flag `--tenant-usage-interval` changes this time. The controller writes the status only if a value changed.
4. It sets the condition `QuotaExceeded`. The status is `True` with the reason `TenantQuotaExceeded` when the requests or the spend are at the limit of the spec or above it. A new period of the gateway sets it back to `False` with the reason `WithinQuota`.
5. If the gateway stopped the count of the requests at its maximum, and this count is below `spec.requests`, the status is `Unknown` with the reason `RequestCountCapped`. The `requests` of the status are then a minimum. A spend at its limit still gives `True`.

- Each change of `QuotaExceeded` goes to the log of the manager, with the typed reason, the tenant label, the requests and the spend.
- The condition `Ready` is `False` with the reason `GatewayError` while a gateway call fails. The message is fixed text. `QuotaExceeded` then keeps its last value.
- If 2 or more `TenantQuota` objects of the install name one tenant label, only the oldest one has an effect. Each other one has `Ready=False` with the reason `DuplicateTenant`, and no use in its status. When the oldest one goes, the next one gets the label, and the customer keeps its spend.
- A delete of the last `TenantQuota` of a tenant label deletes the gateway customer and its budget first, and then removes the finalizer. While the gateway does not answer, the `TenantQuota` stays.
