# Run identity and durable output

An external system, for example a workflow engine, can bind one Agent run to the file that the run wrote. The Agent status gives two values for this: the run identity and the digest of one output file.

## Run identity

`status.runID` is the identity of the run. It is the UID of the Agent.

- It does not change after a controller restart.
- It is the same for each pod of the run.
- The agent container has it in the env variable `KOODAM_RUN_ID`, so the run can write it into its output.

One Agent is one run. To run the same work again, create a new Agent. The new Agent has a new run identity.

## Output digest

Set `spec.output.path` to the absolute path of one file in the agent container. The file must be in a volume of `spec.podTemplate` that the agent container mounts. If that mount has a `subPathExpr`, the expression can use only a variable with a literal value or a field of the pod. The digest container gets only those variables.

When the run ends, `status.output` has these fields:

| Field | Value |
|---|---|
| `path` | The path that the run used. |
| `sha256` | The SHA-256 digest of the file, as 64 lower-case hex characters. |
| `size` | The size of the file in bytes. |
| `reason` | Why there is no digest: `NotFound`, `NotRegularFile`, `ReadError` or `NoResult`. |

The digest container reads the file only in the output volume. If a part of the path in the volume is a link, the reason is `NotRegularFile`. A mount can also be the file itself, with a `subPath`.

The controller never reads the file. A sidecar container of the run pod, from the Koodam image, computes the digest after the agent container ends. It has a read-only mount of the output volume only, and it writes only the digest and the size to its termination message. The controller reads this message after it blocks the key of the run.

Limits:

- The cluster must have sidecar containers (an init container with `restartPolicy: Always`).
- The sidecar runs as the user of the Koodam image, not as the user of the agent container. Thus the file and its directories must permit a read by other users, for example the mode `0644`. If not, the reason is `ReadError`.
- The sidecar computes the digest in the termination grace period of the pod. A file that is too large for this time gets the reason `NoResult`.
- The digest is of the file at the end of the run. The run wrote the file, so the digest proves which file the run left, not that the content is correct.

## Durable output on a PVC

An `emptyDir` volume goes with the pod. To keep the output, mount a PersistentVolumeClaim with `spec.podTemplate`:

```yaml
apiVersion: core.koodam.dev/v1alpha1
kind: Agent
metadata:
  name: report-run
spec:
  image: registry.example.com/report:v1
  command: ["sh", "-c", "make-report > /out/report.txt"]
  model: my-model
  budget: "1"
  output:
    path: /out/report.txt
  podTemplate:
    spec:
      volumes:
      - name: out
        persistentVolumeClaim:
          claimName: report-output
      containers:
      - name: agent
        volumeMounts:
        - name: out
          mountPath: /out
```

Use a different file name or a different sub-path for each run, for example with `KOODAM_RUN_ID` in the name, so that a later run does not replace the file.

## Durable output on object storage

Let the run write the file to a volume as above, and then upload it to object storage as the last step of the command. The digest in the status is the digest of the local file. The egress of the upload goes through the egress proxy if the proxy is on.

## Check the digest

Read the values from the status, and compare them with the file in storage:

```sh
kubectl get agent report-run -o jsonpath='{.status.runID} {.status.output.sha256} {.status.output.size}{"\n"}'
sha256sum report.txt
```

The file in storage is the file of the run if the two digests are equal.

## Start a run

Koodam does not start a run from a queue or a schedule. Until the schedule issue (#129) and the durable run issue (#191) ship, an external system creates the Agent, for example a CronJob, a workflow engine or a CI job.
