# Nookku preset (#83)

Status: the API validation, the test folder, the Python check, the copy of the pinned Nookku files
and the Claude Code env of an Agent. Koodam does not load the plugin in a harness at this stage.

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

## Python

Nookku needs Python 3.10 or later. The Koodam launcher needs Python 3.11 or later, so that the
child processes of Nookku do not import a package from the current folder (#478). Koodam adds no
Python to an image. The image of the harness container must have `python3` 3.11 or later on its
`PATH`:

- **Agent.** The image is `spec.image`.
- **Workspace.** The image is `spec.image`, or the image of the `workspace` container in
  `spec.podTemplate`. The default Workspace image of the chart has no Python. Thus a Workspace that
  selects `nookku` needs an image with Python.

If a harness selects `nookku`, the controller adds the init container `koodam-nookku-python`. It
uses the image, the `PATH`, the user and the group of the harness container. Thus it finds the
`python3` that the harness finds. It runs in the folder `/`, because the working folder of the
harness can be on a volume that the check does not mount. It gets no privilege and no capability,
and its root file system is read-only. It runs
`python3 -c 'import sys; sys.exit(sys.version_info < (3, 11))'`. If `python3` is not on the `PATH`,
or its version is earlier than 3.11, the container fails with this message:

```text
The nookku plugin needs python3 3.11 or later on the PATH of the image. The harness did not start.
```

The harness then does not start. An Agent run fails. A Workspace pod stays in its init step. To
correct a Workspace, change its image or remove `nookku`, then delete the pod. A StatefulSet does
not replace a pod that never became Ready. The check needs `sh` in the image. It gets no credential, no mount and no other env. It runs no Nookku
code.

The check gets the `PATH` of `env` of the harness container if it has a literal value, a key of a
ConfigMap or a field of the pod. These are the limits. In each case, the check uses the `PATH` of
the image:

- A `PATH` from a key of a Secret. The check must not get a credential.
- A `PATH` from a resource of a container, or a `PATH` from `envFrom`.
- A `$(NAME)` reference in a literal `PATH`. The check has no other env, so the reference stays
  as it is.

If the check finds a different `python3` than the harness, the hook of the plugin still fails
closed when it cannot start Nookku. The result is a late failure.

Each Nookku init container has fixed requests (10m CPU, 16Mi memory) and fixed limits (100m CPU,
64Mi memory). They do not come from the harness container, so a namespace with no `LimitRange`
also gets a limit. A `LimitRange` with a larger minimum refuses the pod.

This check prevents a fail-open hook. In Claude Code, a hook command that fails with an exit code
other than 2 does not block the harness. A hook that cannot start Nookku fails in this way.

## Nookku files

Koodam pins Nookku 0.6.0 in `internal/evidence/nookku.pin`, the same pin as the evidence gate. The
Nookku data image (`images/nookku`) has these files, and no shell and no Python:

- `/nookku/share/lib`: the content of the wheel. The build refuses a wheel with a different SHA-256.
- `/nookku/share/plugin`: the files of `plugins/nookku` at the tag commit. The build refuses a file
  with a SHA-256 that is not in `images/nookku/plugin.sha256`. The Nookku release has no plugin
  asset, so the files come from the tag commit. The test file `hooks/register.test.ts` is not in
  the image, because the plugin does not use it.
- `/nookku/bin/nookku`: the launcher. It runs `python3 -I -S`. Its fixed `sys.path` contains only
  the standard library and `/koodam/nookku/lib`. It needs no `pip`.

`nookku start` uses `python3 -m nookku` to start the bridge, the MCP stdio proxy and the chat page.
The launcher exports these variables for the child processes:

- `PYTHONPATH=/koodam/nookku/lib`: a child finds the pinned Nookku.
- `PYTHONSAFEPATH=1`: for `python3 -m nookku`, Python 3.11 or later does not add the current folder
  to `sys.path`.
- `PYTHONNOUSERSITE=1`: a child does not read the user site packages.

Python 3.10 ignores `PYTHONSAFEPATH`. Thus the launcher refuses to start on a Python earlier than
3.11, and the check `koodam-nookku-python` refuses the pod before the harness starts.

Build the image with `make docker-build-nookku`. The job `nookku` of `.github/workflows/image.yml`
builds the image with the same pin and pushes `ghcr.io/mohanraj00/koodam-nookku` on each release
tag. The tag push is the approval of the maintainer for each publication. The job summary shows the
digest. Give the image to the controller with `--nookku-image`, or with the chart value
`nookku.image`. The reference must have a digest (`@sha256:`), so that the files cannot change after
the review.

If a harness selects `nookku`, the controller adds the init container `koodam-nookku-files`. It
copies the files into an emptyDir, which the harness container mounts read-only at
`/koodam/nookku`. It copies the launcher to `/koodam/bin/nookku`. For an Agent run, the controller
puts `/koodam/bin` first on the `PATH` of the agent container, if `spec.env` has no `PATH`. If
`spec.env` has a `PATH`, that `PATH` must contain `/koodam/bin`. The default Workspace image links
`/usr/local/bin/nookku` to the launcher. SSH commands and login shells in this image have
`/usr/local/bin` on their `PATH`. The init container gets no credential and no env. It has the
restrictions of a helper.

If the controller has no `--nookku-image`, an Agent with `nookku` fails. A Workspace gets no Nookku
files, and its condition `Harnesses` is `False` with the reason `NoNookkuImage`.

## Claude Code env

For an Agent with Claude Code and `nookku`, the controller sets
`CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1`, if `spec.env` does not set it. Without it, the Nookku relay
refuses a Claude Code session that is not the interactive CLI (Nookku `docs/limits.md`). A
Workspace runs the interactive CLI, so it does not get the env.

## Later work

These parts are approved, but not built: the load of the plugin in Claude Code and Codex, and the
Codex trust writes. The default of `nookku.image` is empty, because the digest exists only after the
first tag push. A later change can pin the digest of a published image.
