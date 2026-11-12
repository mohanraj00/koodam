# Changelog

This file records the changes for each Koodam version. The newest version is first.

## Unreleased

No changes yet.

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
