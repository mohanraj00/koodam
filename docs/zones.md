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
| Ingress of an App | The claim check compares the zone claim of the caller with the zone of the install | `ZoneMismatch` | no |
| Model route | The route list of the gateway has only the routes of the zone | `RouteOutOfZone` | no |
| Egress | The egress proxy refuses a host with a different declared zone | `EgressOutOfZone` | no |
| Placement | An admission policy examines the node placement and the storage class of an AppVersion | `ZonePlacement` | no |

Each refusal has one typed reason. The reason is in the response and in the span or the log of the component that refuses.

### Ingress of an App

`apps.claimCheck.zoneClaim` names the claim of the signed token that has the zone of the caller. If the claim is not the zone of the install, the claim check answers 403 with the reason `ZoneMismatch`, and the ingress span has this outcome. By default, a token with no zone claim is accepted. With `zone.requireClaim: true`, such a token gets the same refusal.

### Model route

Each item of `gateway.bundled.litellm.modelList` can have a `zone`. The chart puts an item in the route list of the gateway only if the item has no zone or the zone of the install. A call for the model name of a removed item gets a 403 with the reason `RouteOutOfZone`. An item with no zone is a route that the operator permits in each zone.

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
