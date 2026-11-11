# Nookku preset (#83)

Status: the API validation and the test folder. Koodam installs no Nookku file at this stage.

## The field

`plugins` is an optional set in `HarnessSpec`. The only value is `nookku`. You can set it on:

- `Agent.spec.harness.plugins`
- each item of `Workspace.spec.harnesses[].plugins`

If you omit the field or set an empty list, the object is the same as before. The API server
refuses an unknown value and a duplicate value.

## The Agent test volume

If an Agent selects `nookku`, `spec.podTemplate.spec.volumes` must have a volume with these
properties:

- the name is `nookku-tests`;
- the source is `persistentVolumeClaim`;
- `claimName` is not empty.

You own the PVC. The API server checks only the declaration. It does not look up the PVC, and it
does not check its owner. An Agent with no plugins needs no volume. The CRD rule is in the Agent
spec. The API server checks it on create and on update.

```yaml
spec:
  harness:
    name: codex
    plugins: [nookku]
  podTemplate:
    spec:
      volumes:
        - name: nookku-tests
          persistentVolumeClaim:
            claimName: my-nookku-tests
```

A Workspace that selects `nookku` needs no test volume declaration.

## The test folder

Nookku writes its tests to the folder in `NOOKKU_TESTS_DIR`. Koodam gives this folder to the pod,
so that the tests stay after the pod stops.

- **Agent.** The controller mounts the volume `nookku-tests` in the agent container at
  `/koodam/nookku-tests`, and sets `NOOKKU_TESTS_DIR=/koodam/nookku-tests`. The init containers
  do not mount it. Koodam sets this env, so the Agent cannot set `NOOKKU_TESTS_DIR` in
  `spec.env` or in the main container of the pod template. The Agent also cannot mount a volume
  at `/koodam/nookku-tests` or in it. If it does, the Agent fails with a fixed message and
  Koodam makes no gateway key.
- **Workspace.** If a harness of the Workspace has `nookku`, the controller mounts the empty
  read-only folder `/etc/koodam/nookku`. When the pod starts, the image makes
  `/home/dev/nookku-tests` on the home PVC, with mode `0700`. A login shell exports
  `NOOKKU_TESTS_DIR=/home/dev/nookku-tests`. If you already exported `NOOKKU_TESTS_DIR`, the login
  shell does not change it. The folder needs the Workspace image of this release.

If you remove `nookku` from a Workspace, the folder and its tests stay on the home PVC. A new
login shell does not export `NOOKKU_TESTS_DIR`.

## Later work

These parts are not approved: the Python and Nookku dependencies, the images, the plugin pin or
artifact, the install, and the Codex trust writes.
