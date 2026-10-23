#!/usr/bin/env python3
"""Record the Codex sandbox result on the isolated kind-koodam-72 cluster."""
import argparse
import json
import hashlib
import subprocess
from datetime import datetime, timezone
from pathlib import Path

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--kubeconfig", required=True)
p.add_argument("--image", required=True, help="Local Workspace image with verified Codex binaries")
p.add_argument("--output", required=True)
p.add_argument("--gvisor-bundle", required=True, help="The checksum-checked official gVisor archive")
a = p.parse_args()
k = ["kubectl", "--kubeconfig", a.kubeconfig, "--context", "kind-koodam-72"]
ns = "koodam-sandbox-72"


def run(args, **kwargs):
    return subprocess.run(k + args, text=True, capture_output=True, timeout=180, **kwargs)


def record(command, actual=None):
    try:
        r = subprocess.run(actual or command, text=True, capture_output=True, timeout=60)
        return {"command": command, "exit_code": r.returncode, "stdout": r.stdout, "stderr": r.stderr}
    except subprocess.TimeoutExpired:
        return {"command": command, "exit_code": None, "stdout": "", "stderr": "Timed out"}



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
    "debian_bwrap_version": ["/usr/bin/bwrap", "--version"],
    "binary_sha256": ["/usr/bin/sha256sum", "/koodam/bin/.codex-real", "/koodam/bin/bwrap",
                      "/koodam/bin/codex-code-mode-host"],
    "process_security": ["/bin/sh", "-c", "grep -E '^(NoNewPrivs|Seccomp|CapEff):' /proc/self/status"],
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
node = "koodam-72-control-plane"
environment = {}
for name, command in {
    "source_commit": ["git", "rev-parse", "HEAD"],
    "kind_node_image": ["docker", "inspect", node, "--format", "{{.Image}}"],
    "probe_image_id": ["docker", "image", "inspect", a.image, "--format", "{{.Id}}"],
    "workspace_image_id": ["docker", "image", "inspect", "koodam-workspace:72-sandbox",
                           "--format", "{{.Id}}"],
    "kernel": ["docker", "exec", node, "uname", "-a"],
    "kubernetes": ["docker", "exec", node, "kubelet", "--version"],
    "containerd": ["docker", "exec", node, "containerd", "--version"],
    "runc": ["docker", "exec", node, "runc", "--version"],
    "gvisor": ["docker", "exec", node, "runsc", "--version"],
    "gvisor_config": ["docker", "exec", node, "cat", "/etc/containerd/runsc.toml"],
}.items():
    environment[name] = record(command)
for name in ["user/max_user_namespaces", "kernel/unprivileged_userns_clone",
             "kernel/apparmor_restrict_unprivileged_userns"]:
    environment[name] = record(["docker", "exec", node, "cat", "/proc/sys/" + name])
environment["probe_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
environment["gvisor_bundle_sha512"] = hashlib.sha512(Path(a.gvisor_bundle).read_bytes()).hexdigest()
environment["namespace_labels"] = json.loads(run(["get", "namespace", ns, "-o", "json"],
                                                check=True).stdout)["metadata"]["labels"]
environment["pod_specs"] = {}
results = {"date_utc": datetime.now(timezone.utc).isoformat(), "image": a.image,
           "environment": environment, "probes": {}}
for runtime in ["runc", "gvisor"]:
    pod = json.loads(run(["-n", ns, "get", "pod", runtime, "-o", "json"], check=True).stdout)
    environment["pod_specs"][runtime] = pod["spec"]
    run(["-n", ns, "wait", "--for=condition=Ready", "pod/" + runtime, "--timeout=30s"], check=True)
    results["probes"][runtime] = {}
    for name, command in probes.items():
        # No host environment or login files reach Codex. The home is an emptyDir.
        env = ["/usr/bin/env", "-i", "HOME=/home/dev", "CODEX_HOME=/home/dev/.codex",
               "PATH=/koodam/bin:/usr/local/bin:/usr/bin:/bin"]
        actual = k + ["-n", ns, "exec", runtime, "--", "/bin/sh", "-c",
                      'cd /home/dev; test ! -d project || cd project; exec "$@"', "sh"] + env + command
        result = record(command, actual)
        results["probes"][runtime][name] = result
        print(runtime, name, result["exit_code"], flush=True)
# Keep commands and complete pod specs on one line each for a compact evidence file.
lines = ["{"]
for section in ["date_utc", "image"]:
    lines.append("  " + json.dumps(section) + ": " + json.dumps(results[section]) + ",")
for section, values in [("environment", environment), ("probes", results["probes"])]:
    lines.append("  " + json.dumps(section) + ": {")
    for index, (name, value) in enumerate(values.items()):
        lines.append("    " + json.dumps(name) + ": " + json.dumps(value) +
                     ("," if index < len(values) - 1 else ""))
    lines.append("  }" + ("," if section == "environment" else ""))
lines.append("}")
Path(a.output).write_text("\n".join(lines) + "\n")
# Exit 0 means that recording finished. Read each probe exit code for the result.
