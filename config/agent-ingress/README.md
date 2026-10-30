# Agent ingress (not active)

This directory is for the traced ingress of #147. The feature is disabled. No supported command installs a file from this directory.

- The first #147 change adds only the generator `hack/agent-ingress-crd` and its tests. The API does not have `spec.ingressPort` yet, and no make target runs the generator. This directory has no generated file.
- The generator makes two variants of the Agent CRD. The default variant has the disabled gate, which refuses `spec.ingressPort`, and the annotation `koodam.dev/agent-ingress-mode=disabled`. The enabled variant does not have the gate, and it has the annotation `koodam.dev/agent-ingress-mode=enabled`. All other parts of the two variants are the same.
- Later #147 changes add the API field, run the generator in `make manifests` and add the supported generated outputs. Then come the validator and the protected installer. The enabled variant is not a standalone install. Do not use it before these changes are merged.
- The default Kustomize, Helm and `make install`, `make deploy` and `make build-installer` paths use only the default Agent CRD. No default command can enable the ingress.
