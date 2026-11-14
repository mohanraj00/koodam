# Changelog

This file records the changes for each Koodam version. The newest version is first.

## Unreleased

### Apps (not active yet)

- Add the namespaced `App` and `AppVersion` types with their CRD rules. The CRDs are in `config/apps/crd/bases`, so the default install and the chart do not install them. ([#294](https://github.com/mohanraj00/koodam/pull/294)).
- Add the AppVersion controller. It makes a Deployment and a Service for each `AppVersion`, with one gateway key that has the daily budget, a scoped Role and traced egress. The pod reads the key from the read-only file `/etc/koodam/gateway/key`. A delete or `lifecycle: Retiring` blocks the key before the controller deletes an object. The controller is off by default. To use it, apply `config/apps/crd` and `config/apps/rbac`, and start the manager with `--apps-enabled`. The endpoints, the traffic weights and the admission check are not in this change. The condition `TracedEgress` of an AppVersion is `False` with the reason `NoEgressPolicy`, because no NetworkPolicy selects the App pods before #183. One block response of the gateway is the fence of a revoke. A gateway with more than one replica or worker keeps the auth cache window after the block, as for an Agent and a Workspace (#192).
- Add the endpoints of an `App` (`spec.endpoints`) and the App controller. Each endpoint names a Gateway API `Gateway`, a port name and a weight for each `AppVersion`. The controller makes one `HTTPRoute` with the name `kd-<app>.<endpoint>`, and the backend weights send the traffic to the Service of each version. Koodam has no router: the Gateway API implementation of the cluster moves the traffic. A version that is retiring, is not bound to the App or has no Service gets the weight 0. `status.endpoints` has the weights in effect and the route state. The controller starts on a cluster with no Gateway API, and then the route state is `NoGatewayAPI`. Session affinity, the drain of a retiring version and the kind test are not in this change ([#308](https://github.com/mohanraj00/koodam/pull/308)).
- Add a default-deny NetworkPolicy for the pods of each `AppVersion`. The policy permits no ingress by default. With `--app-ingress-namespaces`, the pods of these namespaces can send traffic to the ports of the app container. Use the namespace of the Gateway data plane. The egress is the pods of the egress proxy only. The manager makes the policy if it has `--egress-proxy-ip`, `--egress-proxy-namespace` and `--egress-proxy-pod-labels`, and then the condition `TracedEgress` is `True`. An App pod has no direct path to the API server with this policy ([#308](https://github.com/mohanraj00/koodam/pull/308)).
- Keep a client session on one version with the header `Koodam-App-Version`. The route of an endpoint has one rule for each version with a match on this header. A request with the header and the name of a version goes to that version only, if the version has a weight of more than 0 in the spec. A retiring version with such a weight keeps its rule for the drain time. A version with the weight 0 is not open to a caller, also while it retires. The app container has the name of its version in `KOODAM_APP_VERSION`. Gateway API session persistence is not in the standard channel of Gateway API v1.4.0, so Koodam does not use it. An endpoint has 15 versions at most ([#310](https://github.com/mohanraj00/koodam/pull/310)).
- Drain a retiring version before the revoke. With `lifecycle: Retiring`, the version gets no new session, and it keeps its key and its pods for `spec.drainSeconds` of the App (default 300). A session with the version header stays on the version in this time. Then the controller blocks the key and deletes the objects. Koodam cannot see a session, so the end of the drain time is the end of the sessions. A delete of the AppVersion does not wait. The conditions `TrafficAllowed` and `SessionsEnded` show the state ([#310](https://github.com/mohanraj00/koodam/pull/310)).
- Add `apps.enabled` to the chart. If it is true, the chart installs the App CRDs and the App role, and the manager runs the App controllers. With the egress proxy, the manager gets the selector of the proxy pods, and the policy of the proxy permits the App pods. The default is false. [docs/apps.md](docs/apps.md) has the use ([#311](https://github.com/mohanraj00/koodam/pull/311)).
- Add a kind test for the thin App. One App serves 2 versions with a split through Envoy Gateway, a header keeps a session on one version, and a retiring version drains before the revoke ([#311](https://github.com/mohanraj00/koodam/pull/311)).
- Add a `ValidatingAdmissionPolicy` for the AppVersion, in `config/apps/admission` and in the chart with `apps.enabled`. It refuses a change of `spec.template` after the version has a gateway key. It also refuses a name that is not a DNS label with 60 characters or fewer, and a port of the `app` container with no name, a protocol that is not TCP or a host port ([#312](https://github.com/mohanraj00/koodam/pull/312)).
- `make manifests` writes the App grants to `config/apps/rbac/role.yaml`. The default role and the chart get no App grant.

## v0.4.0-alpha.1 (pre-release, 2026-11-12)

This is a pre-release, not the v0.4 release. The API is `v1alpha1` and can change between tags. Some parts are in the tag but are not active yet. The sections below mark those parts.

### TestKeys and Nookku tests

- Add the namespaced `TestKey` API and the optional test-key limits of a Workspace ([#208](https://github.com/mohanraj00/koodam/pull/208)).
- Issue a gateway key for each TestKey, and revoke it at the end of its TTL or on a delete ([#280](https://github.com/mohanraj00/koodam/pull/280)).
- Mount the TestKey slot in the Workspace pod at `/etc/koodam/test-gateway`. A Workspace delete waits for the TestKey revokes ([#281](https://github.com/mohanraj00/koodam/pull/281)).
- Add the optional `nookku` plugin set to the harness API, with the PVC check for an Agent ([#214](https://github.com/mohanraj00/koodam/pull/214)).
- Give the Nookku test folder to Agent and Workspace pods, and export `NOOKKU_TESTS_DIR` in a login shell ([#282](https://github.com/mohanraj00/koodam/pull/282)).
- Add a kind test. One Nookku test gets its own TestKey, calls the gateway with it, and the key is revoked after the TTL ([#288](https://github.com/mohanraj00/koodam/pull/288)).
- Not active yet: the Nookku install, the plugin pin and the runtime dependencies. These steps wait for owner approval.

### Apps (not active yet)

- Add App pod identity to egress spans ([#206](https://github.com/mohanraj00/koodam/pull/206)).
- Add App targets to `koodam trace` ([#207](https://github.com/mohanraj00/koodam/pull/207)).
- Add envtest tests for the App template rule and the finalizer enrollment fence ([#205](https://github.com/mohanraj00/koodam/pull/205)).
- Add the CRD routing plan for the App output ([#226](https://github.com/mohanraj00/koodam/pull/226)).
- Not active yet: the `App` resource, the traffic split and the rollout.

### Agent ingress (not active yet)

- Add the optional, immutable `Agent.spec.ingressPort`. The default chart and Kustomize output refuse the field ([#210](https://github.com/mohanraj00/koodam/pull/210), [#212](https://github.com/mohanraj00/koodam/pull/212)).
- Add the strict Agent admission checks, the HTTP handler and the admission binary ([#213](https://github.com/mohanraj00/koodam/pull/213), [#215](https://github.com/mohanraj00/koodam/pull/215), [#216](https://github.com/mohanraj00/koodam/pull/216)).
- Add the source for the Agent admission guard and workload ([#221](https://github.com/mohanraj00/koodam/pull/221), [#222](https://github.com/mohanraj00/koodam/pull/222)).
- Add the ingress port to the Agent run Job ([#220](https://github.com/mohanraj00/koodam/pull/220)).
- Read the Agent run state from the API server, not from the cache ([#217](https://github.com/mohanraj00/koodam/pull/217)).
- Check the ingress caller with TokenReview and SubjectAccessReview, and keep the caller identity for the span ([#233](https://github.com/mohanraj00/koodam/pull/233), [#234](https://github.com/mohanraj00/koodam/pull/234), [#235](https://github.com/mohanraj00/koodam/pull/235), [#236](https://github.com/mohanraj00/koodam/pull/236)).
- Parse the ingress route. Resolve and check the ingress target pod and its NetworkPolicy ([#237](https://github.com/mohanraj00/koodam/pull/237), [#238](https://github.com/mohanraj00/koodam/pull/238), [#239](https://github.com/mohanraj00/koodam/pull/239), [#240](https://github.com/mohanraj00/koodam/pull/240), [#242](https://github.com/mohanraj00/koodam/pull/242), [#244](https://github.com/mohanraj00/koodam/pull/244), [#253](https://github.com/mohanraj00/koodam/pull/253), [#254](https://github.com/mohanraj00/koodam/pull/254)).
- Add the ingress span record, its mapping, its JSON encoding and a bounded writer ([#223](https://github.com/mohanraj00/koodam/pull/223), [#225](https://github.com/mohanraj00/koodam/pull/225), [#228](https://github.com/mohanraj00/koodam/pull/228), [#230](https://github.com/mohanraj00/koodam/pull/230), [#231](https://github.com/mohanraj00/koodam/pull/231)).
- Not active yet: the manager flag `--agent-ingress-enabled` is off by default ([#219](https://github.com/mohanraj00/koodam/pull/219)). No ingress handler, Deployment or Service uses this code yet.

### RBAC generation

- Put the RBAC markers of the base role and the App role in separate packages, and generate each role separately ([#227](https://github.com/mohanraj00/koodam/pull/227), [#229](https://github.com/mohanraj00/koodam/pull/229), [#241](https://github.com/mohanraj00/koodam/pull/241), [#245](https://github.com/mohanraj00/koodam/pull/245)).
- Validate the generated role output ([#232](https://github.com/mohanraj00/koodam/pull/232)).

### Chart

- Add a PodDisruptionBudget and a node spread for the egress proxy ([#204](https://github.com/mohanraj00/koodam/pull/204)).

### Build and CI

- Retry the harness downloads after a gateway error, and retry the envtest download ([#277](https://github.com/mohanraj00/koodam/pull/277), [#278](https://github.com/mohanraj00/koodam/pull/278)).
- Update the Go toolchain to go1.26.11 ([#284](https://github.com/mohanraj00/koodam/pull/284)).

### Governance

- Adopt the project standard and its shared gates ([#256](https://github.com/mohanraj00/koodam/pull/256), [#259](https://github.com/mohanraj00/koodam/pull/259), [#260](https://github.com/mohanraj00/koodam/pull/260), [#268](https://github.com/mohanraj00/koodam/pull/268), [#269](https://github.com/mohanraj00/koodam/pull/269), [#286](https://github.com/mohanraj00/koodam/pull/286)).
- Add `CONSTITUTION.md`, and point `CONTRIBUTING.md` to it ([#274](https://github.com/mohanraj00/koodam/pull/274), [#275](https://github.com/mohanraj00/koodam/pull/275)).
