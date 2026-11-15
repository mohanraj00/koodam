# Evidence gate (design)

Status: design for #189. Nothing in this file is in `main`. The API part needs owner approval, because it changes `/api/`.

An evidence gate holds a rollout, or a paused run, until the controller gets a valid evidence record. One gate design serves 2 sources:

1. A sealed result of a scripted Nookku gate run. This design covers this source for an AppVersion (#189).
2. A signed approval record. #190 and #246 build this source for a paused run. This design names only its seam.

Nookku owns the format of its result. Koodam owns the gate, the check of the record and the seam test.

## The Nookku format

The format is the gate run of Nookku SPEC.md section 7.15, with the seal of section 7.4. The design uses the Nookku `main` branch at commit `f7a6804e`. These formats have version 1 at that commit:

| Nookku file | Content | Version |
|---|---|---|
| gate file | The declared tests and the optional proof files. | `v: 1` |
| `run.json` | The ledger of the run, with the subject. | `v: 1` |
| `result.json` | The aggregate verdict, the subject, the reasons and the result of each test. | `v: 1` |
| `seal.json` and its copy | The SHA-256 of each file of the gate folder. | `v: 1` |

The Nookku changelog puts the gate run in 0.5.8, and says that 0.5.8 is not released yet. The tag `v0.5.8` (commit `27dfc9eb`) is older than the gate run and does not have section 7.15. Thus the build must pin the first Nookku release that has section 7.15, and not the tag `v0.5.8`. The build starts only after that release.

These facts of the Nookku format control the design:

- `nookku gate run FILE --subject DIGEST` runs each test and writes one sealed gate folder. Nookku does not interpret the subject. A reader compares it with the digest that it expects.
- `nookku gate verify GATE --json` checks the seal, the copy of the seal and the pinned files of each test, and computes the result again. It exits with 0 for a usable `pass`, 1 for a usable `fail`, and 2 if the result cannot be used.
- A seal is a list of SHA-256 values. It has no key and no signature. It shows a change after the run. It does not show who made the record.
- Nookku states a known limit: the gate trusts the tested code, because a tested entry runs with the permissions of the account.

## Decisions

| Question of #189 | Decision |
|---|---|
| Where the result goes | In the status of the gated object (`status.evidence` of the AppVersion). No separate resource for the Nookku source. |
| Who makes the record | The controller makes the gate Job and reads its report. A user cannot write the record. |
| Rollout tool | The endpoint weights of #183. Koodam adds no Argo Rollouts dependency. |
| Signals that stop a rollout | The verdict of the gate run. The verdict includes the limits of the gate file. |
| Signals during a canary | Not in this design. See "Out of scope". |

A separate record resource is necessary only for a record that a user delivers. Such a record needs a signature, because a user can write any bytes. This is the approval source of #190, not the Nookku source.

## The record

The controller makes one record from each source. The record has the same fields for each source:

| Field | Type | Meaning |
|---|---|---|
| `source` | string | `nookku-gate` or `approval`. |
| `subject` | string | The subject digest of the record. |
| `verdict` | string | `pass` or `fail`. A denied approval is `fail`. |
| `id` | string | The Nookku gate id, or the identity of the approval record. |
| `digest` | string | The SHA-256 of the bytes of the record: `result.json`, or the approval record. |
| `integrity` | string | `seal` for a Nookku result. `signature` for an approval record. |
| `nookkuVersion` | string | Only for `nookku-gate`: the Nookku version of the verify step. |
| `checkedAt` | time | The time of the check by the controller. |
| `expiresAt` | time | Optional. Only for an approval record with a TTL. |

The record has no key value, no token and no text from a test. The gate id and the subject have a fixed form, so they cannot hold a credential.

## The subject digest

For an AppVersion, the controller computes the subject from the spec:

```text
koodam.dev/appversion/v1:<namespace>/<name>:<uid>:<image digest>
```

- `<image digest>` is the `sha256:` digest of the image of the `app` container. A gated version must name this image by digest (`image@sha256:...`). Else the gate refuses the version with the reason `ImageNotPinned`.
- `<uid>` binds the record to this AppVersion object. A record of a deleted version with the same name does not match.
- The admission policy keeps `spec.template` immutable after the condition `Started`. Thus the subject of a version does not change after the gate starts.

The controller gives the subject to `nookku gate run --subject`. It accepts a record only if the subject of the record is the subject that it computes again at the time of the check.

## The gate Job

The App owner adds `spec.gate` to the AppVersion. The field sketch is below. The final field names come in the API PR.

```yaml
spec:
  gate:
    nookku:
      # An image with Nookku, the project, the gate file, the scripts and the entries.
      image: registry.example.com/shop-gate@sha256:...
      # The project root and the gate file in the image.
      root: /project
      gateFile: gates/release.json
      deadlineSeconds: 1800
```

The controller makes the Job after each pod of the version is Ready. The Job has these parts:

1. An init container `gate` with the gate image. Its fixed argv copies the project from `<root>` to a work volume, because Nookku writes the tests and the gate folder in `.nookku/` of the project. Then it runs `nookku gate run <gateFile> --subject <subject> --root <copy> --json`. The env `KOODAM_GATE_TARGET` gives the address of the Service `kd-<version>`. The entries of the project send the test messages to that address.
2. A container `verify` with the same image and a fixed argv. It runs `nookku gate verify --root <copy> --json`. The work volume has only one gate folder, so the newest gate folder is the gate folder of this run. A fixed script then writes a short JSON report to the termination message: the exit code, the gate id, the subject, the verdict, the SHA-256 of `result.json` and the Nookku version. The full `result.json` stays in the pod log, because a termination message has a small size limit.
3. Volumes: an `emptyDir` work volume for the project copy, and an `emptyDir` for `NOOKKU_HOME` with the seal copies. The `verify` container mounts both read-only. If `nookku gate verify` must write a file, the kind test shows it, and the design changes.

These rules apply to the Job:

- The pod gets no service account token, no gateway key and no Secret. The tested app makes its model calls with the key of its own version.
- The tested app runs in the pods of the version, not in the gate pod. Thus the app code cannot write the gate folder or the seal copy.
- A NetworkPolicy lets the gate pod reach only the Service of the version and the cluster DNS. The NetworkPolicy of the version permits ingress from the gate pod.
- The Job has `backoffLimit` and `activeDeadlineSeconds` from `deadlineSeconds`.
- The controller reads the report from the pod status with the existing `list` verb on pods. The controller already has the Job verbs. Thus the gate adds no RBAC verb.
- The controller parses the report with fixed limits: exactly the known keys, a gate id of the Nookku form, and the subject. A report that does not parse gives `NotVerified`.

## The check

The controller accepts a record only if each of these is true:

1. The Job completed, and the `verify` container wrote a report that parses.
2. The exit code of `nookku gate verify` is 0 or 1. Exit code 2 means that the result cannot be used.
3. The subject of the report is the subject that the controller computes again.
4. The verdict agrees with the exit code: `pass` with 0, `fail` with 1.

Then the controller writes the record to `status.evidence` and sets the condition `Evidence`. It writes the record one time. A later Job of the same version cannot change it.

## Failure states

The condition `Evidence` of the AppVersion has these states:

| Status | Reason | Meaning | Traffic |
|---|---|---|---|
| `Unknown` | `Waiting` | Not all pods of the version are Ready. | none |
| `Unknown` | `Running` | The gate Job runs. | none |
| `True` | `Passed` | The controller accepted a record with the verdict `pass`. | the weights of the spec |
| `False` | `Failed` | The controller accepted a record with the verdict `fail`. | none |
| `False` | `NotVerified` | Exit code 2, a report that does not parse, or a verdict that does not agree with the exit code. | none |
| `False` | `SubjectMismatch` | The subject of the report is not the expected subject. | none |
| `False` | `JobFailed` | The Job failed after its retries, or its deadline ended. | none |
| `False` | `ImageNotPinned` | The image of the `app` container has no digest. | none |

- "none" means that the App controller gives the version the weight 0 in effect. The header `Koodam-App-Version` also sends no request to the version. `status.endpoints` of the App shows the weight 0.
- Each `False` state is terminal for the version. The template is immutable, so a new run tests the same code. To test again, make a new AppVersion. A field to start a second run is an open question.
- A failed verdict thus stops the rollout: the old version keeps its traffic, and the new version gets none.
- A version with no `spec.gate` has no condition `Evidence`, and its weights work as today.

A platform team can require a gate with a `ValidatingAdmissionPolicy`: each AppVersion must have `spec.gate`, and the gate image must be on a list of digests. Then the platform team owns the tests, and the App owner cannot select a gate that always passes.

## Key source

- **Nookku source.** A seal has no key. The integrity check is the seal and its copy, which `nookku gate verify` checks. The origin check is the Job: the controller makes its spec, and the kubelet writes the report to the pod status. No user can write `status.evidence`.
- **Approval source.** A signed record has a key. #190 decides the key source, for example a public key in a Secret that the admin names. The gate needs only the record of the table above from that verifier.

## Threat model

The gate protects against these failures:

- A version gets traffic before its declared tests pass.
- A version gets traffic with a result of a different version, or of an older object with the same name.
- A file of the gate folder changes after the run.
- A namespace user writes a record by hand.

The gate does not protect against these actors:

- An App owner who selects the gate image. That owner selects the tests and the entries, and can already set the weights. The admission policy above removes this case.
- A gate image that is not safe. Nookku states that the gate trusts the tested code, and the entries of the project run in the gate container. Thus pin the gate image by digest.

## Paused runs (#190 and #246)

A paused Agent uses the same gate. Its subject is the run identity and the output digest in the Agent status (#315). The approval source writes its record to the status of the Agent. A changed subject keeps the run paused. A `fail` verdict or an expired record stops the run. #190 owns the approval resource and its fields.

## Out of scope

- Signals during a canary. The spend for each call and the denied egress spans need data that the gate pod does not have. A later issue can add them as Nookku proof files with a bound (SPEC.md section 7.15), or as a separate check in the controller.
- A gate run in an outside CI job. Such a record needs a signature, so it uses the approval source.
- The model evaluation of Nookku. A gate run of version 1 uses no model judgment.

## Seam test

The seam test proves that Koodam reads the Nookku format of the pinned release.

- **Pin.** Pin the first Nookku release with section 7.15 by its version and by the SHA-256 of its wheel. Nookku has the Apache-2.0 licence.
- **Report parser.** A Go unit test for the parser of the report: a `pass`, a `fail`, exit code 2, a report that is not JSON, an unknown key, a verdict that does not agree with the exit code, and a wrong subject.
- **Seam.** A CI job installs the pinned wheel, runs the fixed verify script on the gate cases of `conformance/gate/` of the same release, and gives each report to the Go parser. Cases: a `pass`, a `fail`, a missing test, a `result.json` that changed after the seal, and a subject that does not match. The expected reason of each case is in the test.
- **envtest.** An AppVersion with `spec.gate`: the Job spec (no token, read-only mounts, fixed argv), each state of the table from a fake pod status, and the weight 0 in effect until `Passed`.
- **kind.** A gate image from the pinned release, with one gate that passes and one that fails. The test checks the condition and the weights of the HTTPRoute.

## Build plan

Each PR stays at about 400 changed lines or fewer, not counting generated files.

1. The API: `spec.gate`, `status.evidence` and the condition. Level 1.
2. The gate Job, the report parser and the check, with the unit tests and envtest.
3. The weight 0 in effect in the App controller, and the NetworkPolicy rule.
4. The seam test with the pinned release, and the kind test.
5. The doc in `docs/apps.md`.

Start PR 1 only after the owner approves this design and the Nookku release exists.
