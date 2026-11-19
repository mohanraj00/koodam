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

## Cost groups

Set the label `koodam.dev/cost-group` in `metadata.labels` of the App. Each new AppVersion uses this value for its gateway key. A label on the AppVersion does not set the cost group. An empty value is the same as no label.

The value must be in `spec.costGroups` of the Profile bound to the namespace. Before the controller makes a key, it checks the value. If the value is not permitted, the AppVersion has `GatewayKey=False` with the reason `CostGroupNotAllowed`. It gets no key Secret and no Deployment. The controller checks again. Fix the App label or the Profile to let the version start.

The controller saves the value in `status.costGroup` before it makes the key, and adds `costGroup` to the gateway key metadata if the value is not empty. A later change of the App label or the Profile does not change the saved group or the group of the key. A version with no model has no key and no cost group.

## Admission checks

A `ValidatingAdmissionPolicy` holds the checks of an AppVersion that the CRD cannot hold:

- After the version has a gateway key, or after the controller started to make its objects (the condition `Started`), `spec.template` cannot change. A later failure does not remove this lock. To change the template, make a new AppVersion. `replicas`, `budgetPerDay` and `lifecycle` can change.
- The name of the AppVersion is a DNS label with 60 characters or fewer, and `spec.appRef.name` has 63 characters or fewer.
- Each port of the `app` container has a name and the TCP protocol, with no host port.
- `template.rules` is not permitted. The NetworkPolicy of an App pod has no path to the API server, so a version Role has no effect. [#340](https://github.com/mohanraj00/koodam/issues/340) adds this path. An AppVersion from before this check keeps its rules, and it can still lose its finalizer.

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

## The evidence gate

An AppVersion can have an evidence gate. A gated version gets traffic only after the controller accepts a sealed result of a Nookku gate run with the verdict `pass`. [evidence-gate.md](evidence-gate.md) has the design, the check and the threat model.

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: AppVersion
metadata:
  name: shop-v2
spec:
  appRef:
    name: shop
  gate:
    nookku:
      image: registry.example.com/shop-gate@sha256:...
      root: /project
      gateFile: gates/release.json
      deadlineSeconds: 1800
  template:
    podTemplate:
      spec:
        containers:
        - name: app
          image: registry.example.com/shop@sha256:...
          ports:
          - name: http
            containerPort: 8080
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
```

- `spec.gate` is immutable. You cannot add it, change it or remove it after create.
- The image of the `app` container must have a `sha256` digest. If not, the condition is `Evidence=False/ImageNotPinned`.
- The gate image has `python3` with the Nookku package, the Nookku project in `root`, and the gate file `gateFile` in that project. The path of the gate file is relative to `root`. Name the gate image by digest, because the gate trusts the code of this image.
- The project has its egress answer in `.nookku/egress.json`. Nookku accepts an answer only for 30 minutes, so the gate Job registers this file before the run. With no answer, Nookku runs no test, and the condition is `Evidence=False/NotVerified`.
- The entries of the project send the test messages to the address in the env variable `KOODAM_GATE_TARGET`. The value is a host and a port: the Service of the version and the first port of the `app` container.
- `deadlineSeconds` is the maximum time of the gate Job. The default is 1800.

The controller does these steps:

1. It waits until each pod of the version is Ready.
2. It makes the Job `kd-<version>-gate`. If this name has more than 63 characters, the Job gets a short form of the name with a hash. The Job runs `nookku gate run` with the subject of the version, and then `nookku gate verify`. The gate pod has no ServiceAccount token, no gateway key and no Secret. A NetworkPolicy lets the pod reach only the pods of the version and the cluster DNS in `kube-system`.
3. It reads the short report of the verify step from the pod status, and compares the subject of the report with the subject that it computes again.
4. It writes the record to `status.evidence` one time, and sets the condition `Evidence`.

The condition `Evidence` of the AppVersion has these states:

| Status | Reason | Meaning |
|---|---|---|
| `Unknown` | `Waiting` | Not all pods of the version are Ready. |
| `Unknown` | `Running` | The gate Job runs. |
| `True` | `Passed` | The controller accepted a record with the verdict `pass`. |
| `False` | `Failed` | The controller accepted a record with the verdict `fail`. |
| `False` | `NotVerified` | The result cannot be used, the report does not parse, or the verdict does not agree with the exit code. |
| `False` | `SubjectMismatch` | The subject of the report is not the subject of this version. |
| `False` | `JobFailed` | The gate Job failed after its retries, or its deadline ended. |
| `False` | `ImageNotPinned` | The image of the `app` container has no digest. |

- Only `True/Passed` gives the version the weights of the spec. In each other state the version has the weight 0 in effect, `status.endpoints` of the App shows the weight 0, and the header `Koodam-App-Version` sends no request to the version. Thus the old version keeps its traffic.
- Each `False` state is terminal. The template is immutable, so a new run tests the same code. To test again, make a new AppVersion.
- A version with no `spec.gate` has no condition `Evidence`, and its weights work as before.
- The full `result.json` of the gate run is in the log of the `verify` container of the gate pod.

The gate has these limits:

- If the signed claim check is on, the Service of the version refuses a request with no claim. The gate pod has no claim, so the entries of the gate project must get a claim from a different source. [#354](https://github.com/mohanraj00/koodam/issues/354) tracks this.
- The egress rule of the gate pod names port 53 of the namespace `kube-system`. A cluster DNS at a different location is not in the rule, and then the gate fails closed.

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

The check protects a version only if the version has its NetworkPolicy. The policy permits ingress to the port of the check container only. Thus the claim check needs `egressProxy.enabled`: the chart refuses an issuer with no egress proxy, and the manager does not start with the claim flags and no egress proxy flags. The condition `ClaimCheck` of the AppVersion shows the state: `True` with the reason `Enforced`, `False` with the reason `NoNetworkPolicy` if the policy does not exist, or `False` with the reason `NotApplied` if the objects of the version stay from an earlier template.

The JWKS has public keys only. The manager refuses a JWKS with private or symmetric key material, because it copies the document into an annotation of each App pod.

The kubelet sends the probes of the `app` container directly to that container, with no claim.

## Retire a version

Set `spec.lifecycle: Retiring` on the AppVersion. This change is terminal.

1. The version gets no new session. Its weight in effect is 0.
2. For `spec.drainSeconds` of the App, the version keeps its pods and its key. A client with the version header stays on the version.
3. At the end of the drain time, the controller blocks the key. Then it deletes the pods and the other objects of the version.

Koodam cannot see a session, so the end of the drain time is the end of the sessions. A delete of the AppVersion does not wait for the drain time.

## Egress

With the egress proxy of the chart, name each namespace of the Apps in `workspace.networkPolicy.namespaces`. The proxy gives an App pod an identity only in a named namespace, and it refuses each call of a pod with no identity. The chart refuses `apps.enabled` with `egressProxy.enabled` and an empty list.

With the egress proxy, each AppVersion gets a NetworkPolicy. The pods get egress only to the egress proxy. The policy permits no ingress by default. Set `apps.ingressNamespaces` to the namespace of the Gateway data plane, and then the pods of that namespace can send traffic to the ports of the `app` container. Then the condition `TracedEgress` of the AppVersion is `True`. With this policy, an App pod has no path to the API server. Thus the admission policy refuses `template.rules`.
