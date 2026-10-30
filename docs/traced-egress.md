# Traced egress

With traced egress, each call from an Agent run pod or a Workspace pod to the internet or to the gateway goes through the Koodam egress proxy. The proxy writes a span for each call. The span has the namespace, the pod and the run of the source pod. Thus you can see which run called which host, and when.

Traced egress is off by default. This page tells how it operates, how to turn it on, and its limits.

## How it operates

The chart adds the egress proxy (a Deployment and a Service with a fixed ClusterIP). The controller gets the proxy address and changes each Agent run pod and each Workspace pod:

1. The pod DNS server is the proxy. The controller sets `dnsPolicy: None` and removes the `dnsConfig` and the `hostAliases` of `spec.podTemplate`.
2. The proxy sends a name in the cluster domain to the cluster DNS. It answers each other name with its own IP address.
3. Thus a call to `example.com` goes to the proxy. The proxy reads the host from the TLS SNI or the HTTP `Host` header. Then it connects to the real host and writes a span.
4. The gateway URL of the pod is the `/llm` route of the proxy. The proxy sends the model call to the gateway and writes a span of the kind `llm`.
5. A NetworkPolicy makes the pod egress default-deny. The pod can send only to the proxy, the API server and the rules of `extraEgress`.

The proxy also accepts an explicit proxy call (`HTTPS_PROXY` and CONNECT) on its proxy port.

| Service port | Use |
| --- | --- |
| `egressProxy.port` (3128) | The explicit proxy, CONNECT and the `/llm` route to the gateway. |
| 53, UDP and TCP | The DNS responder. |
| 443 | TLS. The proxy reads the SNI and does not decrypt the call. |
| 80 | Plain HTTP. |

The proxy identifies the source pod from the source IP address, with a watch on the pods. It does not use a header from the pod for the identity. Each Agent and Workspace status has the condition `TracedEgress`. It is `True` with the reason `Proxy` if the pods use the proxy, and `False` with the reason `NoProxy` if they do not.

## Turn it on

1. Use a CNI that enforces NetworkPolicy, for example Calico, Cilium or the kindnet of kind.
2. Select a free IPv4 address in the Service range of the cluster for `egressProxy.clusterIP`.
3. Get the endpoint addresses of the API server:

   ```sh
   kubectl get endpointslices -n default -l kubernetes.io/service-name=kubernetes
   ```

4. Add the values below, and run `helm upgrade`. Put each namespace with Agents or Workspaces in `workspace.networkPolicy.namespaces`, also a namespace with no Profile.

> **Caution:** Do not omit a namespace that has Agents. The proxy watches the pods only in these namespaces, and only these namespaces get the NetworkPolicy. An Agent in a different namespace gets the proxy DNS, but the proxy cannot identify the pod.

```yaml
egressProxy:
  enabled: true
  clusterIP: 10.96.100.100
  networkPolicy:
    apiServer:
    - to:
      - ipBlock:
          cidr: 172.18.0.2/32
      ports:
      - protocol: TCP
        port: 6443
workspace:
  networkPolicy:
    namespaces: [dev-sample]
```

The new pods use the proxy. A Workspace pod starts again with the new DNS and the new gateway URL. [charts/koodam/README.md](../charts/koodam/README.md#values) has each `egressProxy` value. [The Workspace network](profiles.md#with-the-egress-proxy) has the NetworkPolicy rules.

> **Caution:** Do not add the gateway to `workspace.networkPolicy.extraEgress`. A call through `extraEgress` goes direct to the peer and has no span.

## Read the spans

The proxy writes each span as one JSON line in its log. [The egress span contract](egress-spans.md) has the fields, the kinds, the results and the version rules.

`koodam trace` reads the spans of an Agent or a Workspace from the proxy logs:

```sh
koodam trace agent/my-run
koodam trace workspace/dev --json --since 2026-10-23T09:00:00Z > spans.jsonl
koodam trace my-run -f
```

`koodam trace app/NAME` reads the spans of all versions of an App. The App identity of a span comes only from the reserved pod labels `koodam.dev/app` and `koodam.dev/app-version`. This is not a complete App integration: #183 still needs the App wiring in production, default-deny ingress and egress, proxy steering and the Nookku correlation. [The App pods](egress-spans.md#app-pods-183) has the rules.

Your account needs `list` on `pods` and `get` on `pods/log` in the namespace of the proxy. [The CLI](cli.md#traced-egress) has the flags.

[Koodam with Nookku](nookku.md) tells how Nookku uses the spans to check the calls of a harness, and how to run Nookku in a Workspace.

## The evidence

The traced egress spike (#8) decided go on 2026-10-22. The results of its confirmatory run are in `spikes/traced-egress/results/score-test2.json` of the private `os-initiatives` project:

- 360 of 360 planted calls had a span with the correct run, with 0 wrong spans.
- 0 of 23 bypass attempts got through. With no NetworkPolicy, 20 of 21 attempts got through.
- The p95 delay that the proxy added was 1.239 ms for HTTPS and 0.235 ms for ingress. For the `llm` calls, the p95 with the proxy was 0.444 ms less than the direct p95.

The e2e test `Traced egress` (#145) does the same checks on kind for each change. It makes planted calls from an Agent pod and a Workspace pod and checks their spans in `koodam trace`. It also makes each bypass attempt from both pods, after the same attempt works from a control pod with no policy.

## Limits

- A span is metadata. The proxy does not decrypt TLS, so a span has no path, no header and no body of an HTTPS call.
- The proxy does not block DNS over HTTPS or other tunnels inside an HTTPS call. It records the host of each call.
- A call to the API server, a call to an `extraEgress` peer and a call to 127.0.0.1 in the pod have no span.
- The pod has no direct IPv6 egress. The e2e test does not test IPv6.
- The proxy permits only the ports in `egressProxy.httpPorts` and `egressProxy.connectPorts`. Thus `git` over SSH on port 22 does not work. Use HTTPS for git.
- A managed cluster can have more paths out of a pod, for example the cloud metadata service, a node-local DNS cache or a public API server address. Make sure that the NetworkPolicy blocks each path before you trust the spans. The e2e test checks kind only.
- An external database, or an app at a private address outside `extraEgress`, does not work in a pod with the egress proxy.
