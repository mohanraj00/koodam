#!/usr/bin/env bash
# Copyright 2026 Mohan Raj.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# migrate-storage.sh moves the stored objects of the group core.koodam.dev to v1beta1 (#511).
#
# After the upgrade to v0.7.0, the API server writes each new or changed object as v1beta1. An
# object that nobody changes stays as v1alpha1 in the storage. This script replaces each object
# with no change, which makes the API server write it as v1beta1. Then it sets
# status.storedVersions of each CRD to v1beta1 only.
#
# Run it one time after the upgrade, as a cluster admin. A second run makes no change. The
# script needs kubectl 1.24 or later. Set KUBECTL to use a different command, for example
# KUBECTL="kubectl --context staging".
#
# The conversion webhook must be ready, because the API server converts each v1alpha1 object of
# an Agent, a Workspace and a TestKey with it.
set -euo pipefail

GROUP=core.koodam.dev
STORAGE=v1beta1
RESOURCES=(agents apps appversions approvalrecords holds profiles spendbudgets tenantquotas testkeys workspaces)
# A replace fails with a conflict if a controller changed the object after the read. That change
# also wrote the object as v1beta1. The script reads all the objects again and tries again.
TRIES=5

k() {
  # shellcheck disable=SC2086 # KUBECTL can have arguments.
  ${KUBECTL:-kubectl} "$@"
}

migrated=0
for resource in "${RESOURCES[@]}"; do
  crd="$resource.$GROUP"
  # The App CRDs are not in each install.
  if [ -z "$(k get crd --ignore-not-found -o name "$crd")" ]; then
    echo "$crd: not installed, no change"
    continue
  fi
  storage="$(k get crd "$crd" -o jsonpath='{.spec.versions[?(@.storage==true)].name}')"
  if [ "$storage" != "$STORAGE" ]; then
    echo "$crd: the storage version is $storage, not $STORAGE. Upgrade koodam first." >&2
    exit 1
  fi
  for try in $(seq 1 "$TRIES"); do
    if [ -z "$(k get "$resource.$STORAGE.$GROUP" --all-namespaces -o name)" ]; then
      break
    fi
    if k get "$resource.$STORAGE.$GROUP" --all-namespaces -o json | k replace -f - >/dev/null; then
      break
    fi
    if [ "$try" = "$TRIES" ]; then
      echo "$crd: could not replace each object in $TRIES tries. No CRD status changed for it." >&2
      exit 1
    fi
    sleep 2
  done
  k patch crd "$crd" --subresource=status --type=merge -p "{\"status\":{\"storedVersions\":[\"$STORAGE\"]}}" >/dev/null
  echo "$crd: the stored objects are $STORAGE"
  migrated=$((migrated + 1))
done
echo "Done: $migrated CRDs have only $STORAGE in status.storedVersions."
