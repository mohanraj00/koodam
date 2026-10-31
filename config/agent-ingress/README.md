# Agent ingress (not active)

This directory is for the traced ingress of #147. The feature is disabled. No supported command installs a file from this directory.

- The API has the optional field `spec.ingressPort`, from 1 to 65535, with no default. You cannot add, remove or change it after create.
- `make manifests` runs the generator `hack/agent-ingress-crd` after controller-gen and `hack/crd-optional`. It makes two variants of the Agent CRD. The default variant in `config/crd/bases` has the disabled gate, which refuses `spec.ingressPort`, and the annotation `koodam.dev/agent-ingress-mode=disabled`. The enabled variant in `crd/bases` of this directory does not have the gate, and it has the annotation `koodam.dev/agent-ingress-mode=enabled`. All other parts of the two variants are the same. Do not edit the two files. Run `make manifests`.
- The enabled variant has no admission webhook yet. Thus it does not refuse a pod template port with the reserved name `http`. It is not a standalone install. Do not apply it to a cluster.
- Later #147 changes add the validator and the protected installer. Do not use this directory before these changes are merged.
- The default Kustomize, Helm and `make install`, `make deploy` and `make build-installer` paths use only the default Agent CRD. No default command can enable the ingress.
