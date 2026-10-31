# Nookku preset (#83)

Status: API validation only. No controller reads the plugin. Koodam installs nothing and mounts
nothing for it at this stage.

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

## Later work

These parts are approved for a later step. They are not done:

- the mount of `nookku-tests` at `/koodam/nookku-tests` in the Agent pod;
- the directory `/home/dev/nookku-tests` on the home PVC of a Workspace, and
  `NOOKKU_TESTS_DIR` in the login shell.

These parts are not approved: the Python and Nookku dependencies, the images, the plugin pin or
artifact, the install, and the Codex trust writes.
