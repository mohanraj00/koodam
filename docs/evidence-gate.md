# Evidence gate

Status: the gate of an AppVersion is built (#342). The pin is Nookku 0.6.0 (#373). [apps.md](apps.md) has the use. The seam test with the pinned release and the kind test are in `main` (#343). The approval source of a paused run is #190, #246 and #344. Of that source, only the offline verifier (#344) is in `main`. No controller calls the verifier, and no resource carries an approval record.

An evidence gate holds a rollout, or a paused run, until the controller gets a valid evidence record. One gate design serves 2 sources:

1. A sealed result of a scripted Nookku gate run. This design covers this source for an AppVersion (#189).
2. A signed approval record. #344 adds the offline verifier. #190 and #246 use it for a paused run.

Nookku owns the format of its result. Koodam owns the gate, the check of the record and the seam test.

## The Nookku format

The format is the gate run of Nookku SPEC.md section 7.15, with the seal of section 7.4. The design uses the Nookku `main` branch at commit `f7a6804e`. These formats have version 1 at that commit:

| Nookku file | Content | Version |
|---|---|---|
| gate file | The declared tests and the optional proof files. | `v: 1` |
| `run.json` | The ledger of the run, with the subject. | `v: 1` |
| `result.json` | The aggregate verdict, the subject, the reasons and the result of each test. | `v: 1` |
| `seal.json` and its copy | The SHA-256 of each file of the gate folder. | `v: 1` |

The build uses Nookku 0.6.0 (tag `v0.6.0`). This release has section 7.15 and the gate cases in `conformance/gate/`. Release 0.6.0 does not change a record version. A test in `result.json` can have the optional key `persona`, and the verify program does not read the tests. Nookku 0.6.0 verifies a result of 0.5.9 with no `persona` key.

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

The App owner adds `spec.gate` to the AppVersion. The field is immutable.

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

1. An init container `gate` with the gate image. Its fixed argv copies the project from `<root>` to a work volume, because Nookku writes the tests and the gate folder in `.nookku/` of the project. Nookku accepts an egress answer only for 30 minutes (SPEC.md section 7.11), so the step then registers the answer of the project, `.nookku/egress.json`, with `nookku register`. Then it runs `nookku gate run <gateFile> --subject <subject> --root <copy> --json`. The env `KOODAM_GATE_TARGET` gives the address of the Service `kd-<version>`: a host and the first port of the `app` container. The entries of the project send the test messages to that address. The step does not use the exit code of the run, because the verify step decides.
2. A container `verify` with the same image and a fixed argv. It runs `nookku gate verify --root <copy> --json`. The work volume has only one gate folder, so the newest gate folder is the gate folder of this run. A fixed program then writes a short JSON report to the termination message: the exit code, the gate id, the subject, the verdict, the SHA-256 of `result.json`, the format version of `result.json` and the Nookku version. The full `result.json` stays in the pod log, because a termination message has a small size limit.
3. Volumes: an `emptyDir` work volume for the project copy, and an `emptyDir` for `NOOKKU_HOME` with the seal copies. The `verify` container mounts both read-only. If `nookku gate verify` must write a file, the kind test shows it, and the design changes.

The two fixed programs are Python text in `internal/evidence`. The controller gives each program to `python3 -c`, with the values of the spec as separate arguments. Thus the gate image must have `python3` with the Nookku package, and no value of the spec goes through a shell.

These rules apply to the Job:

- The pod gets no service account token, no gateway key and no Secret. The tested app makes its model calls with the key of its own version.
- The tested app runs in the pods of the version, not in the gate pod. Thus the app code cannot write the gate folder or the seal copy.
- A NetworkPolicy lets the gate pod reach only the pods of the version and the cluster DNS in the namespace `kube-system`. The NetworkPolicy of the version permits ingress from the gate pod until the gate has an end state. Each reconcile applies the policy of the gate pod again while the Job runs. The controller has no watch on NetworkPolicy (#359). The name of this policy is `kd-<version>.gate`. A version name has no dot, so the name is never the name of the NetworkPolicy of a version.
- The gate pod has the label `koodam.dev/app-gate`, and not the labels of the version. Thus it is not behind the Service of the version.
- The Job has a `backoffLimit` of 2 and `activeDeadlineSeconds` from `deadlineSeconds`. Each retry is a new pod with new volumes.
- The name of the Job is `kd-<version>-gate`. A Job name has 63 characters or fewer, so a long version name gets a short form with a hash of the name.
- A revoke of the version deletes the Job and its pods after the block of the key.
- The controller reads the report from the pod status with the existing `list` verb on pods. The controller already has the Job verbs. Thus the gate adds no RBAC verb.
- The controller parses the report with fixed limits: exactly the known keys, each one time, no null value, a gate id of the Nookku form, and the subject. A report that does not parse gives `NotVerified`. A format version of `result.json` that the parser does not know also gives `NotVerified`.
- The controller reads the report only from a pod that the Job controls, that succeeded, and that has the two containers of the gate Job: the same names, images, argv and env. If the Job has more than one such report, the controller accepts none.

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
- The App controller gives the weights of the spec only if the condition is `True` and the record has the verdict `pass`.
- A failed verdict thus stops the rollout: the old version keeps its traffic, and the new version gets none.
- A version with no `spec.gate` has no condition `Evidence`, and its weights work as today.

A platform team can require a gate with a `ValidatingAdmissionPolicy`: each AppVersion must have `spec.gate`, and the gate image must be on a list of digests. Then the platform team owns the tests, and the App owner cannot select a gate that always passes.

## Key source

- **Nookku source.** A seal has no key. The integrity check is the seal and its copy, which `nookku gate verify` checks. The origin check is the Job: the controller makes its spec, and the kubelet writes the report to the pod status. No user can write `status.evidence`.
- **Approval source.** The caller gives the verifier the bytes of a trusted key set that the admin selects separately from the signed record. #190 owns the choice of key source.

## Threat model

The gate protects against these failures:

- A version gets traffic before its declared tests pass.
- A version gets traffic with a result of a different version, or of an older object with the same name.
- A file of the gate folder changes after the run.
- A namespace user writes a record by hand.

The gate does not protect against these actors:

- An App owner who selects the gate image. That owner selects the tests and the entries, and can already set the weights. The admission policy above removes this case.
- A gate image that is not safe. Nookku states that the gate trusts the tested code, and the entries of the project run in the gate container. Thus pin the gate image by digest.
- A user who can make pods or Jobs in the namespace. That user can already put a pod behind the Service of a live version. The compare of the pod containers stops a pod with a different program, and it does not examine each field of the pod.
- A user who can write `status` of an AppVersion. That user can write a record by hand.

## Paused runs (#190 and #246)

A paused Agent uses the same gate. Its subject is the run identity and the output digest in the Agent status (#315). The approval source writes its record to the status of the Agent. A changed subject keeps the run paused. A `fail` verdict or an expired record stops the run. #190 owns the approval resource and its fields.

## Out of scope

- Signals during a canary. The spend for each call and the denied egress spans need data that the gate pod does not have. A later issue can add them as Nookku proof files with a bound (SPEC.md section 7.15), or as a separate check in the controller.
- A gate run in an outside CI job. Such a record needs a signature, so it uses the approval source.
- The model evaluation of Nookku. A gate run of version 1 uses no model judgment.

## Seam test

The seam test proves that Koodam reads the Nookku format of the pinned release.

- **Pin.** `internal/evidence/nookku.pin` pins Nookku 0.6.0 by its version, by the SHA-256 of its wheel and by the commit of its tag. The seam job and the kind test read this file. To use a new release, change the 3 values together. Nookku has the Apache-2.0 licence.
- **Report parser.** A Go unit test for the parser of the report: a `pass`, a `fail`, exit code 2, a report that is not JSON, an unknown key, a verdict that does not agree with the exit code, and a wrong subject.
- **Seam.** The CI job `seam` installs the pinned wheel, and `pip` refuses a wheel with a different SHA-256. It gets `conformance/gate/` of the pinned commit. A case there has the records of each test, and not a sealed gate folder. Thus `internal/evidence/testdata/seam_case.py` makes a sealed gate folder from a case with the functions of the pinned package. The test `TestSeam` then runs the fixed verify program on the folder and gives the report to the Go parser. Cases: a `pass`, a `fail`, a missing test, a persona set with one persona that fails, a second message that passes, a second message that is late, a `result.json` that changed after the seal, and a subject that does not match. Nookku 0.6.0 added the cases `persona_set`, `second_message_pass` and `second_message_late`, so `seam_case.py` writes the key `during` of a script message, the key `second_messages` of `findings.json` and the persona copy in `inputs/`. The expected reason of each case is in the test. If the pinned release writes a format version of `result.json` that the parser does not know, the test fails and names the version that the parser knows.
- **envtest.** An AppVersion with `spec.gate`: the Job spec (no token, read-only mounts, fixed argv), each state of the table from a fake pod status, and the weight 0 in effect until `Passed`.
- **kind.** `test/e2e/gate_test.go` builds a gate image from the pinned release, with one gate that passes and one that fails. The test checks the condition, the record, the weights in effect and the weights of the HTTPRoute. The gate image is in `test/e2e/testdata/gate`.

The approval seam uses the signed record protocol and vectors of Entelos tag
`v0.5.0`. The caller gives the expected `scope`, `function` and subject, and the
time of the check. The verifier refuses a decision with a different `scope`,
`function` or `snapshot_ref`. It also refuses a decision when the UTC date of
the check is before `from`, or at or after `until`. The decision must have the
`kind` `human`. A `model` or `policy` verdict, or a verdict without `kind`, is
refused (Entelos `KINDS.md`, rule 3). A disposition of `accept` gives `pass`. A
disposition of `reject` gives `fail`. The signed decision's `record_ref` gives
both the evidence ID and digest.

A valid record does not show that it is the current selection. A later reject,
a suspension or a new snapshot can replace it, and only the Host answers that.
The Entelos signed record format states this limit in its section "Use by an
outside consumer" ([#364](https://github.com/mohanraj00/koodam/issues/364)).

## Open questions

- The signed claim check. If the check is on, the Service of the version refuses a request with no claim, and the gate pod has no claim. [#354](https://github.com/mohanraj00/koodam/issues/354) tracks the decision.
- The cluster DNS. The egress rule of the gate pod names port 53 of the namespace `kube-system`. A cluster DNS at a different location, for example a node-local cache, is not in the rule. Then the gate fails closed.

## Build plan

Each PR stays at about 400 changed lines or fewer, not counting generated files.

1. The API: `spec.gate`, `status.evidence` and the condition. Level 1. Done in #342.
2. The gate Job, the report parser and the check, with the unit tests and envtest. Done in #342.
3. The weight 0 in effect in the App controller, and the NetworkPolicy rule. Done in #342.
4. The seam test with the pinned release, and the kind test. This is #343.
5. The doc in `docs/apps.md`. Done in #342.
