# Apps

An `App` is an application that serves clients. Each `AppVersion` of the App is a Deployment and a Service, with its own gateway key. The API is `v1alpha1`, and the App parts are off by default.

## Install

Set `apps.enabled=true` in the chart. The chart then installs the `App` and `AppVersion` CRDs, the App role and the admission policy of the AppVersion, and the manager runs the App controllers. Helm keeps the App CRDs on an uninstall.

An endpoint of an App needs a Gateway API implementation in the cluster, with the `HTTPRoute` of the standard channel. The chart does not install one. Koodam has no router: the Gateway API implementation moves the traffic. The kind test of this repository uses Envoy Gateway.

## An App with 2 versions

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: App
metadata:
  name: shop
spec:
  drainSeconds: 300
  endpoints:
  - name: web
    gateway:
      name: public
    port: http
    versions:
    - name: shop-v1
      weight: 80
    - name: shop-v2
      weight: 20
---
apiVersion: core.koodam.dev/v1alpha1
kind: AppVersion
metadata:
  name: shop-v1
spec:
  appRef:
    name: shop
  budgetPerDay: "5"
  template:
    model: my-model
    podTemplate:
      spec:
        containers:
        - name: app
          image: registry.example.com/shop:v1
          ports:
          - name: http
            containerPort: 8080
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
```

The second version `shop-v2` has the same form. The `Gateway` with the name `public` must permit routes from the namespace of the App.

## Models and the gateway key

- `template.model` is one gateway model. `template.models` is a list of gateway models. Use one of the two forms.
- With a model, the version gets one gateway key with `budgetPerDay`. The key permits only the declared models, and the gateway refuses a call to a different model. The `app` container gets the gateway address in `KOODAM_GATEWAY_URL`, the first model in `KOODAM_GATEWAY_MODEL`, each model in `KOODAM_GATEWAY_MODELS` with a comma between them, and the path of the key file in `KOODAM_GATEWAY_KEY_FILE`.
- With no model, the version gets no key, no key file and no budget. `budgetPerDay` is not permitted. The version still gets its Role, its Service and its traced egress, and it does not count for `maxLiveVersions`.

## Admission checks

A `ValidatingAdmissionPolicy` holds the checks of an AppVersion that the CRD cannot hold:

- After the version has a gateway key, or after the controller started to make its objects (the condition `Started`), `spec.template` cannot change. A later failure does not remove this lock. To change the template, make a new AppVersion. `replicas`, `budgetPerDay` and `lifecycle` can change.
- The name of the AppVersion is a DNS label with 60 characters or fewer, and `spec.appRef.name` has 63 characters or fewer.
- Each port of the `app` container has a name and the TCP protocol, with no host port.

## Endpoints and weights

The App controller makes one `HTTPRoute` for each endpoint, with the name `kd-<app>.<endpoint>`. Each backend is the Service `kd-<version>` with the weight of the version. A version that cannot get a new session has the weight 0 in effect. `status.endpoints` of the App shows the weights in effect and the route state:

| Route state | Meaning |
|---|---|
| `Accepted` | Each parent Gateway accepted the route. |
| `Pending` | No Gateway controller accepted the route at this time. |
| `NotAccepted` | A Gateway refused the route or a backend. |
| `NoGatewayAPI` | The cluster does not have the `HTTPRoute`. |
| `NameConflict` | An `HTTPRoute` with the route name is not from this App. |
| `Refused` | The API server refused the route. |

## Sessions

A request with the header `Koodam-App-Version: <version>` goes to that version only, if the version has a weight of more than 0 in the spec of the endpoint. A retiring version with such a weight keeps this rule for the drain time. The header is not proof of a session: each caller can send it. A version with the weight 0 gets no traffic, also while it retires. The app container has the name of its version in the env variable `KOODAM_APP_VERSION`. Thus the app can give the name to its client, and the client sends the header to stay on the version. Koodam does not use the session persistence of the Gateway API, because it is not in the standard channel.

## The signed claim check

The operator can require a signed claim for each request to an App. The claim is a JWT from one issuer. The check is on for each App of the cluster when the chart has an issuer:

```yaml
apps:
  enabled: true
  claimCheck:
    issuer: https://issuer.example.com
    audience: shop
    callerClaim: sub
    tenantClaim: tenant
    jwks: '{"keys":[...]}'
```

Then each AppVersion pod gets the container `koodam-claim` in front of the `app` container:

- The Service of the version sends each request to the check container. The route and the weights do not change.
- The check accepts the algorithms RS256 and ES256 only. It verifies the signature with the keys of `jwks`, and it checks `iss`, `aud`, `exp` and `nbf`.
- A request with no valid claim gets the status 401. It does not get to the `app` container.
- The `app` container does not get the token. It gets the caller label in the header `Koodam-Caller`, and the tenant label in `Koodam-Tenant` if `tenantClaim` is set. A caller cannot set these headers.
- The check container writes one JSON line for each request to its log: the time, the App, the version, the method, the status, the outcome and the 2 labels. The line has no token, no path and no header.

`jwks` has the public keys of the issuer. A change of `jwks` starts the manager again, and then each AppVersion gets new pods. A JWKS URL is not in this step.

The check protects a version only if the version has its NetworkPolicy, thus only with `egressProxy.enabled`. The policy permits ingress to the port of the check container only. With no policy, a pod of the cluster can call the port of the `app` container directly. The condition `ClaimCheck` of the AppVersion shows the state: `True` with the reason `Enforced`, or `False` with the reason `NoNetworkPolicy`.

The kubelet sends the probes of the `app` container directly to that container, with no claim.

## Retire a version

Set `spec.lifecycle: Retiring` on the AppVersion. This change is terminal.

1. The version gets no new session. Its weight in effect is 0.
2. For `spec.drainSeconds` of the App, the version keeps its pods and its key. A client with the version header stays on the version.
3. At the end of the drain time, the controller blocks the key. Then it deletes the pods and the other objects of the version.

Koodam cannot see a session, so the end of the drain time is the end of the sessions. A delete of the AppVersion does not wait for the drain time.

## Egress

With the egress proxy of the chart, each AppVersion gets a NetworkPolicy. The pods get egress only to the egress proxy. The policy permits no ingress by default. Set `apps.ingressNamespaces` to the namespace of the Gateway data plane, and then the pods of that namespace can send traffic to the ports of the `app` container. Then the condition `TracedEgress` of the AppVersion is `True`. With this policy, an App pod has no path to the API server.
