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
| Egress | The egress proxy refuses a host with a different declared zone | `EgressOutOfZone` | yes |
| Placement | An admission policy examines the node placement and the storage class of an AppVersion | `ZonePlacement` | yes |

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
- The mock route of `gateway.bundled.mock` is a route of the install. A call for its model name gets no `RouteOutOfZone`, also if an item of a different zone has the same name.
- The gateway writes one JSON line to its log for each refusal: `{"kind": "gateway-zone", "outcome": "RouteOutOfZone", "zone": "zone-a", "model": "model-b"}`. The line has no key and no request data.
- The gateway examines the key before the zone. Thus a key that does not permit the model name gets the usual key refusal of the gateway, not `RouteOutOfZone`.

The refusal comes from a hook of the bundled LiteLLM gateway. The chart adds the hook only if the list has a route of a different zone. Its source is `config/gateway/koodam_zone.py`. A change of the routes of a different zone starts the gateway pod again.

With your own gateway (`gateway.mode: existing`), Koodam does not control the route list. The operator of that gateway must remove the routes of other zones.

### Egress

`zone.egressHosts` gives a zone to an egress host. It needs `zone.name` and `egressProxy.enabled`.

```yaml
zone:
  name: zone-a
  egressHosts:
    models.zone-b.example.com: zone-b
    "*.zone-b.example.net": zone-b
```

- The egress proxy refuses a call to a host with a declared zone that is not the zone of the install. The rule applies to each path of the proxy: CONNECT, plain HTTP and the transparent listeners.
- The pod gets the status 403 with the text `egress denied: EgressOutOfZone`, or the proxy closes the TLS connection. The egress span has the result `denied` and the reason `EgressOutOfZone`. [egress-spans.md](egress-spans.md) has the contract.
- The proxy refuses the host before the name lookup. Thus it sends no DNS query and no byte to a host of a different zone.
- A host is a DNS name, or `*.` and a DNS name. The pattern `*.zone-b.example.net` is for each name below `zone-b.example.net`, not for `zone-b.example.net`. An exact name wins against a pattern, and the longest pattern wins against a shorter pattern.
- A host with no entry follows the usual egress rules. The list is not a list of permitted hosts.
- An IP address has no declared zone, and the proxy cannot find the host of an address. If the list has a host of a zone that is not the zone of the install, the proxy refuses each call to an IP address with the reason `EgressOutOfZone`. A host name with a number as its last label is also an address. If the list has no such host, a call to an IP address follows the usual egress rules.
- The check uses the name of the call. A name with no entry passes the check, also if it has the same address as a host of a different zone. Put each name of an endpoint of a different zone in the list.
- With `zone.name`, each egress span has the field `zone`: the zone of the install.

The Agent ingress span of [ingress-spans.md](ingress-spans.md) has no zone field, because no proxy writes this span.

### Placement

The controller does not examine the nodes. With `zone.name` and `apps.enabled`, the chart adds a ValidatingAdmissionPolicy with the name `<release>-koodam-zone-placement`. It refuses an AppVersion at admission with the status 403 and a message that starts with `ZonePlacement`.

```yaml
zone:
  name: zone-a
  namespaces: [zone-a-apps]
  topologyValues: [eu-west-1, eu-west-1a, eu-west-1b]
  storageClasses: [zone-a-disk]
```

The node placement:

- The policy examines the node selector and the node affinity of the pod template, for the node labels `topology.kubernetes.io/zone` and `topology.kubernetes.io/region`. It also examines the 2 old labels `failure-domain.beta.kubernetes.io/zone` and `failure-domain.beta.kubernetes.io/region`.
- `zone.topologyValues` has the values of these labels on the nodes of the zone. With an empty list, the only permitted value is the zone name.
- A node selector for one of these labels must have a permitted value.
- A node affinity term for one of these labels must have the operator `In` and only permitted values. The policy refuses each other operator, for example `NotIn`, because such a term does not show that the node is in the zone. This rule applies to the necessary terms and to the preferred terms.
- The policy refuses a pod template that selects a node by its name, because a node name does not show that the node is in the zone. This rule applies to `nodeName`, to a `matchFields` term of the node affinity, and to the node label `kubernetes.io/hostname` in the node selector or the node affinity.
- A pod template with no term for these labels is accepted. The policy does not put the pod on a node of the zone.

The storage, only with items in `zone.storageClasses`:

- The policy refuses a pod template with an ephemeral volume claim that has no storage class or a storage class that is not in the list.
- A second policy, `<release>-koodam-zone-storage`, refuses a new PersistentVolumeClaim with no storage class or a storage class that is not in the list. It also applies to the volume claims of a Workspace. It does not change a claim that exists.
- The home claim of a new Workspace gets the first storage class of the list. Thus the policy accepts the claim, also if the default storage class of the cluster is not in the list. A Workspace from before this value keeps its claim and its claim template.
- `zone.storageClasses` needs `zone.namespaces`, because the storage policy must not refuse the volume claims of other software in the cluster.
- With the bundled gateway and `gateway.bundled.postgres.persistence.enabled`, the chart refuses a storage class of the database that is not in the list.

The policies apply in the namespaces of the install: `zone.namespaces` and the release namespace. With no `zone.namespaces`, the placement policy applies in each namespace. The checks of an AppVersion apply to a create and to a change of `spec.template` only. Thus an AppVersion from before the policy can still lose its finalizer.

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
- A pod template with no node selector for a zone can run on each node of the cluster. In a cluster with nodes of 2 zones, also use a control of the cluster, for example a node selector for each namespace.
- The placement policy is a list of refused placements. It examines the zone labels, the region labels and the node name only. It is not a full control of the node of a pod ([#433](https://github.com/mohanraj00/koodam/issues/433)). The owner of an App can direct a pod to a node of a different zone with a field that the policy does not examine:
  - a different node label, if it is only on the nodes of a different zone;
  - a pod affinity or a pod anti-affinity with a `topologyKey`, if a pod with the selected labels is on a node of a different zone;
  - a toleration for a taint of the nodes of a different zone;
  - a `schedulerName` of a scheduler that does not apply the node selector.

  The control of the cluster is also necessary for these cases.
- The placement policy does not examine the node of a volume that exists.
- An install does not move a workload or its data to a different zone.
