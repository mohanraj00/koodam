# Tenants

A tenant is a client group of an App. The tenant label comes from the signed claim that the claim check of the App verifies ([apps.md](apps.md#the-signed-claim-check)). It does not come from a ServiceAccount. A `TenantQuota` gives one tenant label a quota for each period and a rate limit. Koodam uses the tenant label only for the quota, the rate limit and the spend group. The application isolates the data of its tenants.

## What is active

- The `TenantQuota` API is in `main`. The chart installs its CRD with `apps.enabled=true`.
- No controller reads a `TenantQuota` at this time. The rate limit, the gateway quota, the status and the default for an unknown tenant come in later changes of [#193](https://github.com/mohanraj00/koodam/issues/193).

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
| `tenant` | Necessary. The tenant label of the signed claim, with 1 to 253 characters. You cannot change it after create. |
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
| `observedTime` | The time of the last read of the use from the gateway. |

## The typed reasons

Each refusal of a tenant has one of these reasons:

| Reason | Meaning |
|---|---|
| `TenantRateLimited` | The tenant sent more requests than its rate limit. |
| `TenantQuotaExceeded` | The tenant has no more quota in the current period. |
| `UnknownTenant` | No `TenantQuota` names the tenant label, and the install refuses such a tenant. |
