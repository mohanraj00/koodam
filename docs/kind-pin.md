# Pin of kind in CI

The e2e job `E2E on kind` in `.github/workflows/ci.yml` uses one pinned `kind` version and one pinned node image. The job sets both in its `env` block:

- `KIND_VERSION`: the `kind` release tag, for example `v0.33.0`.
- `KIND_NODE_IMAGE`: the node image with its digest, for example `kindest/node:v1.37.0@sha256:...`.

The job checks the `kind` binary against the `.sha256sum` file of the release. `make setup-test-e2e` passes `KIND_NODE_IMAGE` to `kind create cluster --image`. If the variable is empty, for example on a local run, `kind` uses its own default image.

## Update the pin

1. Open the release notes of the new `kind` release.
2. Copy the release tag to `KIND_VERSION`.
3. Copy the node image with its `@sha256` digest from the release notes to `KIND_NODE_IMAGE`. Always keep the digest. The release notes say that only the digest guarantees the correct image.
4. Open a PR. The `E2E on kind` job must pass.

The change is a level 1 change, because it changes `.github/`.
