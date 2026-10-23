#!/usr/bin/env python3
"""Record the Codex sandbox result on the isolated kind-koodam-72 cluster."""
import argparse
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--kubeconfig", required=True)
p.add_argument("--image", required=True, help="Local Workspace image with verified Codex binaries")
p.add_argument("--output", required=True)
a = p.parse_args()
k = ["kubectl", "--kubeconfig", a.kubeconfig, "--context", "kind-koodam-72"]
ns = "koodam-sandbox-72"


def run(args, **kwargs):
    return subprocess.run(k + args, text=True, capture_output=True, timeout=180, **kwargs)


# These are the Workspace security settings. No Secret or host folder is mounted.
security = {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000,
            "seccompProfile": {"type": "RuntimeDefault"}}
items = [{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": ns, "labels": {
    "pod-security.kubernetes.io/enforce": "restricted",
    "pod-security.kubernetes.io/enforce-version": "latest"}}},
    {"apiVersion": "node.k8s.io/v1", "kind": "RuntimeClass",
     "metadata": {"name": "gvisor-72"}, "handler": "runsc"}]
for runtime in ["runc", "gvisor"]:
    spec = {"restartPolicy": "Never", "automountServiceAccountToken": False,
            "enableServiceLinks": False, "securityContext": dict(security, fsGroup=1000),
            "containers": [{"name": "workspace", "image": a.image, "imagePullPolicy": "Never",
                            "command": ["/bin/sleep", "3600"],
                            "securityContext": dict(security, allowPrivilegeEscalation=False,
                                                    capabilities={"drop": ["ALL"]}),
                            "volumeMounts": [{"name": "home", "mountPath": "/home/dev"}]}],
            "volumes": [{"name": "home", "emptyDir": {}}]}
    if runtime == "gvisor":
        spec["runtimeClassName"] = "gvisor-72"
    items.append({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": runtime, "namespace": ns},
                  "spec": spec})
run(["apply", "-f", "-"], input=json.dumps({"apiVersion": "v1", "kind": "List", "items": items}),
    check=True)
run(["-n", ns, "wait", "--for=condition=Ready", "pod", "--all", "--timeout=120s"], check=True)

probes = {
    "runtime": ["/bin/sh", "-c", "uname -a; dmesg | head -5"],
    "prepare": ["/bin/sh", "-c", "mkdir -p /home/dev/project /home/dev/.codex; "
                "printf fixture > /home/dev/project/input; printf outside > /home/dev/outside; "
                "rm -f /home/dev/project/output"],
    "version": ["/koodam/bin/.codex-real", "--version"],
    "release_bwrap": ["/koodam/bin/bwrap", "--unshare-user", "--ro-bind", "/", "/", "--", "/bin/true"],
    "debian_bwrap": ["/usr/bin/bwrap", "--unshare-user", "--ro-bind", "/", "/", "--", "/bin/true"],
    "release_bwrap_network": ["/koodam/bin/bwrap", "--unshare-user", "--unshare-net",
                              "--ro-bind", "/", "/", "--", "/bin/true"],
    "control": ["/bin/sh", "-c", "cat input; printf CONTROL_OK"],
    "codex_read": ["/koodam/bin/.codex-real", "sandbox", "--", "/bin/sh", "-c",
                   "cat input; printf SANDBOX_OK"],
    "codex_write": ["/koodam/bin/.codex-real", "sandbox", "-c", 'sandbox_mode="workspace-write"',
                    "--", "/bin/sh", "-c", "printf inside > output; "
                    "if printf changed > /home/dev/outside; then exit 9; else printf OUTSIDE_DENIED; fi"],
    "after": ["/bin/sh", "-c", 'test ! -e output && test "$(cat /home/dev/outside)" = outside '
              '&& printf FILES_UNCHANGED'],
}
results = {"date_utc": datetime.now(timezone.utc).isoformat(), "image": a.image, "probes": {}}
for runtime in ["runc", "gvisor"]:
    results["probes"][runtime] = {}
    for name, command in probes.items():
        # No host environment or login files reach Codex. The home is an emptyDir.
        env = ["/usr/bin/env", "-i", "HOME=/home/dev", "CODEX_HOME=/home/dev/.codex",
               "PATH=/koodam/bin:/usr/local/bin:/usr/bin:/bin"]
        result = run(["-n", ns, "exec", runtime, "--", "/bin/sh", "-c",
                      'cd /home/dev; test ! -d project || cd project; exec "$@"', "sh"] + env + command)
        results["probes"][runtime][name] = {"command": command, "exit_code": result.returncode,
                                              "stdout": result.stdout, "stderr": result.stderr}
        print(runtime, name, result.returncode, flush=True)
Path(a.output).write_text(json.dumps(results, indent=2) + "\n")
# Exit 0 means that recording finished. Read each probe exit code for the result.
