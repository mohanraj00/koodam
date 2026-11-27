# Release checklist

Do these steps before the owner approves a tag. [STANDARD.md](STANDARD.md) has the approval rule for a tag.

1. Set the line `The current release is vX.Y.Z.` in [README.md](../README.md) to the new tag.
2. Set each `VERSION=` line in the README and in `docs/` to the new version. `make test-tools` fails if one line is stale.
3. Check that the README, [test-keys.md](test-keys.md) and [nookku.md](nookku.md) say which parts are active and which are not.
4. Move the `Unreleased` section of [CHANGELOG.md](../CHANGELOG.md) under the new version.
5. Run `make check`.
6. Review [threat-model.md](threat-model.md). Check that each named test exists, change each open PR row to the test file on `main` when that PR merges, and set the review date.

## After the tag push

1. Open the summary of the `Image` workflow run of the tag. Copy the digest of the spend-api image.
2. Run `docker buildx imagetools inspect ghcr.io/mohanraj00/koodam-spend-api:X.Y.Z`. Make sure that the digest is the same as the digest in the summary.
