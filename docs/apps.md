# Apps

An `App` is an application that serves clients. Each `AppVersion` of the App is a Deployment and a Service, with its own gateway key. The API is `v1alpha1`, and the App parts are off by default.

## Install

Set `apps.enabled=true` in the chart. The chart then installs the `App` and `AppVersion` CRDs and the App role, and the manager runs the App controllers. Helm keeps the App CRDs on an uninstall.

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

A request with the header `Koodam-App-Version: <version>` goes to that version only, if the version has a weight of more than 0 in the endpoint or drains. The header is not proof of a session: each caller can send it. A version with the weight 0 that does not retire gets no traffic. The app container has the name of its version in the env variable `KOODAM_APP_VERSION`. Thus the app can give the name to its client, and the client sends the header to stay on the version. Koodam does not use the session persistence of the Gateway API, because it is not in the standard channel.

## Retire a version

Set `spec.lifecycle: Retiring` on the AppVersion. This change is terminal.

1. The version gets no new session. Its weight in effect is 0.
2. For `spec.drainSeconds` of the App, the version keeps its pods and its key. A client with the version header stays on the version.
3. At the end of the drain time, the controller blocks the key. Then it deletes the pods and the other objects of the version.

Koodam cannot see a session, so the end of the drain time is the end of the sessions. A delete of the AppVersion does not wait for the drain time.

## Egress

With the egress proxy of the chart, each AppVersion gets a NetworkPolicy. The pods get egress only to the egress proxy. The policy permits no ingress by default. Set `apps.ingressNamespaces` to the namespace of the Gateway data plane, and then the pods of that namespace can send traffic to the ports of the `app` container. Then the condition `TracedEgress` of the AppVersion is `True`. With this policy, an App pod has no path to the API server.
