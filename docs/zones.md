# Zones

Status: this file is the design of #247. The table in "The parts" shows which parts are in `main`.

Some operators must keep the data of a tenant in one geographic zone. A zone in Koodam is a name that the operator gives to one install, for example `zone-a`. Koodam does not find the zone of a node or of a provider. The operator declares the zone of each thing, and Koodam refuses a thing with a different zone.

## The rule: one install is one zone

The zone is a value of the install, not of a namespace, a Profile or a tenant. Thus one answer is sufficient for each question about a workload: the zone of its install.

```yaml
zone:
  name: zone-a
```

The manager, the claim check, the route list of the gateway and each span get this name. An install routes no traffic to a different zone. If you have 2 zones, you have 2 installs, and each install has its own manager, gateway, database and egress proxy.

An install with an empty `zone.name` has no zone. It makes no zone check, and its spans have no zone.

A tenant of #193 has no zone of its own. A tenant label is known only in the install that has its `TenantQuota`, so the tenant is in the zone of that install. [tenants.md](tenants.md) has the tenant rules.

## The parts

| Part | Control | Typed reason | In `main` |
|---|---|---|---|
| Install | `zone.name`, `zone.namespaces`, `apps.sharedResources` | none | yes |
| Ingress of an App | The claim check compares the zone claim of the caller with the zone of the install | `ZoneMismatch` | yes |
| Model route | The route list of the gateway has only the routes of the zone | `RouteOutOfZone` | yes |
| Egress | The egress proxy refuses a host with a different declared zone | `EgressOutOfZone` | no |
| Placement | An admission policy examines the node placement and the storage class of an AppVersion | `ZonePlacement` | no |

Each refusal has one typed reason. The reason is in the response and in the span or the log of the component that refuses.

### Ingress of an App

`apps.claimCheck.zoneClaim` names the claim of the signed token that has the zone of the caller. It needs `zone.name` and the claim check of [apps.md](apps.md).

```yaml
zone:
  name: zone-a
  requireClaim: false
apps:
  claimCheck:
    zoneClaim: zone
```

- If the zone claim of a valid token is not the zone of the install, the claim check answers 403. The reason `ZoneMismatch` is in the header `Koodam-Reason` and in the body. The app container does not get the request.
- The comparison is exact. A zone claim that is not a string, for example a list or `null`, is a different zone.
- By default, a token with no zone claim is accepted. With `zone.requireClaim: true`, such a token gets the same refusal.
- The zone check comes before the tenant check. Thus a refused request uses no part of the rate limit of the tenant.
- The ingress span has the outcome `ZoneMismatch`, the labels of the caller and the zone of the install. [app-ingress-spans.md](app-ingress-spans.md) has the contract.
- With `zone.name`, each ingress span has the field `zone`, also with no zone claim.

A change of these values gives each AppVersion a new pod template, so its pods start again one time.

### Model route

Each item of `gateway.bundled.litellm.modelList` can have a `zone`. You can then use one list for each install.

```yaml
zone:
  name: zone-a
gateway:
  bundled:
    litellm:
      modelList:
      - model_name: model-a
        zone: zone-a
        litellm_params: {model: openai/model-a, api_base: https://models.zone-a.example.com}
      - model_name: model-b
        zone: zone-b
        litellm_params: {model: openai/model-b, api_base: https://models.zone-b.example.com}
```

- The chart puts an item in the route list of the gateway only if the item has no zone or the zone of the install. The key `zone` is not in the route list.
- An item with no zone is a route that the operator permits in each zone.
- A call for a model name that only items of a different zone have gets the status 403. The message starts with `RouteOutOfZone`. Here, `model-b` gets this refusal in the install of `zone-a`.
- If a model name has an item of the install zone and an item of a different zone, the gateway uses only the item of the install zone.
- The gateway writes one JSON line to its log for each refusal: `{"kind": "gateway-zone", "outcome": "RouteOutOfZone", "zone": "zone-a", "model": "model-b"}`. The line has no key and no request data.
- The gateway examines the key before the zone. Thus a key that does not permit the model name gets the usual key refusal of the gateway, not `RouteOutOfZone`.

The refusal comes from a hook of the bundled LiteLLM gateway. The chart adds the hook only if the list has a route of a different zone. Its source is `config/gateway/koodam_zone.py`. A change of the routes of a different zone starts the gateway pod again.

With your own gateway (`gateway.mode: existing`), Koodam does not control the route list. The operator of that gateway must remove the routes of other zones.

### Egress

`zone.egressHosts` gives a zone to an egress host. The egress proxy refuses a connection to a host with a zone that is not the zone of the install. The egress span has the outcome `EgressOutOfZone`. A host with no declared zone follows the usual egress rules.

### Placement

The controller does not examine the nodes. A ValidatingAdmissionPolicy refuses an AppVersion at admission with a message that starts with `ZonePlacement`:

- The pod template has a node selector or a node affinity for `topology.kubernetes.io/zone` or `topology.kubernetes.io/region` with a value that is not in `zone.topologyValues`. The default of `zone.topologyValues` is the zone name.
- The pod template has a volume claim with a storage class that is not in `zone.storageClasses`.

The policy applies in the namespaces of the install (`zone.namespaces`).

## Two installs in one cluster

Two installs can be in one cluster, each with its own zone. They share only the CRDs and the admission policy of the AppVersion.

The rules:

- Use a different release name and a different namespace for each install. The names of the cluster roles come from the release name.
- Give each install its namespaces in `zone.namespaces`. A namespace must be in one install only. With this value, the manager reads the namespaced objects of these namespaces and of the release namespace only. Thus an Agent, a Workspace, an App or a `TenantQuota` belongs to one install.
- Set `apps.sharedResources: false` in the second install. The first install has the App CRDs and the admission policy of the AppVersion. Helm installs the other CRDs one time.
- Give each install its own gateway and its own egress proxy address (`egressProxy.clusterIP`).
- A Profile is for the full cluster. The 2 managers read each Profile, and they write the same status.

The steps:

1. Install the first zone.

   ```sh
   helm install zone-a charts/koodam --namespace zone-a-system --create-namespace \
     -f zone-a-values.yaml \
     --set zone.name=zone-a --set 'zone.namespaces={zone-a-apps}' \
     --set apps.enabled=true
   ```

2. Install the second zone with no shared resources.

   ```sh
   helm install zone-b charts/koodam --namespace zone-b-system --create-namespace \
     -f zone-b-values.yaml \
     --set zone.name=zone-b --set 'zone.namespaces={zone-b-apps}' \
     --set apps.enabled=true --set apps.sharedResources=false
   ```

3. Make sure that each manager has its zone and its namespaces.

   ```sh
   kubectl -n zone-a-system logs deploy/zone-a-koodam-controller-manager | grep "The scope of this install"
   ```

Remove the installs in the reverse order: the second install first. If you remove the first install while the second install stays, these things occur:

- The admission policy of the AppVersion goes away with the first install.
- The App CRDs stay, because Helm keeps them. They keep the ownership annotations of the first release. Helm then refuses `apps.sharedResources=true` in a different release until you change the annotations `meta.helm.sh/release-name` and `meta.helm.sh/release-namespace` of the App CRDs.

## The limits

- The zone is a declared name. Koodam does not prove where a node, a disk, a model provider or an egress host is.
- The cluster role of each manager is for the full cluster. `zone.namespaces` limits what the manager reads. It is not an RBAC limit.
- An install does not move a workload or its data to a different zone.
