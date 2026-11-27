# Entelos signed record vectors

`entelos-v0.5.0-signed-cases.json` comes from Entelos tag `v0.5.0`, path
`src/protocol/signed/vectors/signed-cases.json`. The source licence is Apache-2.0.

The source file has the private key seeds of the test keys in its top-level `keys` object. This
copy does not have that object. No other byte is changed.

| File | SHA-256 |
|---|---|
| The source file | `05498e56d1c1a9a3d11da45a7b7d56d1b4abdf407c824f7eb5169166a8a0b90b` |
| This copy | `f968adc4a59c69e9af17a20284c168b6980b5d4b4c02d53fbedb2eccf968754a` |

The strip step, with jq 1.7.1, on the source file:

```sh
jq --indent 1 'del(.keys)' signed-cases.json > entelos-v0.5.0-signed-cases.json
```

The source file has the jq format with an indent of 1, so the step removes only the lines of the
`keys` object. The `note` field of the source still says that the seeds are public. This copy
keeps the note without change.

`TestSignedVectors` pins the SHA-256 of this copy. `TestVectorsHaveNoSeeds` fails if the file
has the top-level `keys` object or a `seed` field.

# Fixture set of a signed approval that binds an outside request

The folder `v0.7.0` has a static fixture set that the Entelos project made with its tag `v0.7.0`,
commit `935059139a3e47237f0703c30c8aab8407a29aee`. The source licence is Apache-2.0. The set has
one accept chain and one reject chain for the same request. Each chain is a signed decision with
the kind `human`, the stored bytes of its snapshot record, and the bytes of its content manifest.
The manifest has one item with the id `request` and the SHA-256 of `request.json`.

The signing key is a public test key of the Entelos vectors. Use it only in tests. This folder
has no key seed: the generator of the set has the seed, and it is not here.

| File | SHA-256 |
|---|---|
| `request.json` | `9d56cb14b63cd272a4cac6c5962da33d289d660289a26ba31d21804a109628d4` |
| `manifest-accept.json` | `81d3ef45a68446079527825ae4511534b125ee3b0c8fe334184218661393198c` |
| `manifest-reject.json` | `0f41eb56ada3388dbcb53c32e81aa063c3eace36d13afeae1d2410d20590a57a` |
| `snapshot-accept.record` | `831e24da5a0bead452c8ae9484d378b2856a133f1731e1e19f0718d87750ece1` |
| `snapshot-reject.record` | `2504f5c90123556cb0cd3b9693732bb5c106be57df737b167e730e31fbc8bbf8` |
| `decision-accept.signed.json` | `66d9ec4a9db994ca745370a6a05185d0f67c1b07d48f244720d243616d1cab79` |
| `decision-reject.signed.json` | `44fe1c7b124f8521d95d6df9b7f05a9f40143c9de9e75ce2e72220351c2bf49a` |
| `keyset.json` | `b91f9c554afdd2820e923e87968416222300ff28769792051cfe698a2621a61b` |

The Entelos repository does not have these files. Its project made them on that commit with a
generator, which runs the release in process with a fixed clock. Thus the seam job cannot get
them from the tag, and the tests pin the SHA-256 of each file.

# The pin of the seam job

`../entelos.pin` pins the tag `v0.7.0`, its commit and the SHA-256 of the signed record vectors
of the tag (`src/protocol/signed/vectors/signed-cases.json`):
`05498e56d1c1a9a3d11da45a7b7d56d1b4abdf407c824f7eb5169166a8a0b90b`. These are the same bytes as at
the tag `v0.5.0`, so the copy above is also the copy of `v0.7.0`. The seam job of CI checks out
the tag, fails if the tag is not on the pinned commit, and compares the SHA-256 of the vectors
file. Then `TestSeam` checks that the copy is the source file with no `keys` object. `TestPin`
fails if this README does not name the values of the pin.

`TestFixtureChains` and `TestBindingRefusals` pin the SHA-256 of each file. `TestFixtureSet` fails
if the folder has a file with no pin or a file with a seed.
