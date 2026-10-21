#!/usr/bin/python3 -I
"""Fixed finite canary glue. Root publication and native acceptance are separate gates."""
import fcntl
import hashlib
import importlib.util
import json
import os
import pathlib
import re
import socket
import stat
import struct
import subprocess
import sys
import time

ROOT_ENTRY = "/usr/local/libexec/gascity/repairer-limited208"
CONFIG = pathlib.Path("/etc/gascity/repairer-limited208")
ACTION = pathlib.Path("/var/lib/gc-repairer/limited208-action")
SOCKET = "/run/gc-repairer-limited208/action.sock"
UNIT = "gc-repairer-action-208-fixture.service"
UNIT_FILE = pathlib.Path("/etc/systemd/system") / UNIT
HELPER_SHA = "a09c1bdf80805e6e0585ff6b355123250b86ca39efff0107bb07fec528434ddd"
MAX = 262144
HEX = re.compile(r"[0-9a-f]{64}\Z")

class Refusal(Exception):
    """Only fixed public summaries leave this module."""

def need(ok, reason):
    if not ok:
        raise Refusal(reason)

def sha(raw):
    return hashlib.sha256(raw).hexdigest()

def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()

def read(path):
    path = pathlib.Path(path)
    for ancestor in reversed([*path.parents, path]):
        info = ancestor.lstat()
        need(not stat.S_ISLNK(info.st_mode) and info.st_uid == 0
             and info.st_mode & 0o022 == 0, "protected_path")
    before = path.lstat()
    need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1
         and before.st_mode & 0o077 == 0 and before.st_size <= MAX, "private_file")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    try:
        info = os.fstat(fd)
        need((info.st_dev, info.st_ino, info.st_size) == (before.st_dev, before.st_ino, before.st_size), "file_changed")
        raw = os.read(fd, MAX + 1)
        need(len(raw) == before.st_size, "file_changed")
        return raw
    finally:
        os.close(fd)

def exclusive(path, value):
    # The directory is independently protected before any state operation.
    path = pathlib.Path(path)
    check_directory(path.parent)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        raw = encoded(value)
        need(len(raw) <= MAX, "receipt_size")
        with os.fdopen(fd, "wb", closefd=False) as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(fd)
        parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        os.close(fd)

def check_directory(path):
    for parent in reversed([pathlib.Path(path), *pathlib.Path(path).parents]):
        info = parent.lstat()
        need(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and info.st_mode & 0o022 == 0, "state_directory")
    need(pathlib.Path(path).stat().st_mode & 0o077 == 0, "state_directory")

def dispatch(command, stdin):
    need(stdin == b"" and command in ("repairer-limited208 status", "repairer-limited208 run"), "command")
    subprocess.run(["/usr/bin/sudo", "-n", ROOT_ENTRY, command.split(" ")[1]],
                   stdin=subprocess.DEVNULL, check=True, env={"PATH": "/usr/bin:/bin", "LANG": "C"})

def projection(state, incident, accepted, evidence=None):
    need(state in ("pending_profile", "ready", "running", "resolved", "hold"), "state")
    need(evidence is None if state != "resolved" else type(evidence) is str and HEX.fullmatch(evidence), "evidence")
    a = accepted or {}
    return dict(schema_version=1, state=state, incident_id=incident,
                owner_session=a.get("owner_session"), generation=a.get("generation"),
                incarnation_sha256=a.get("incarnation_sha256"),
                accepted_comment_sha256=a.get("accepted_comment_sha256"),
                evidence_sha256=evidence, summary={
                    "pending_profile": "Awaiting independently published native Accepted witness.",
                    "ready": "Exact finite canary staged; one RUN permitted.",
                    "running": "Attested execution or owned natural-cycle reconciliation in progress.",
                    "resolved": "Fixed action natural cycle and model terminal attested.",
                    "hold": "Retained state requires reconciliation; no RUN retry."
                }[state])

def validate_owner(request, accepted, incident):
    need(accepted["incident_id"] == incident and accepted["owner_session"] == request["session_id"]
         and accepted["generation"] == request["generation"]
         and accepted["incarnation_sha256"] == sha(request["instance_token"].encode())
         and accepted["executor_host"] == accepted["target_host"] == "vm328699"
         and accepted["resource"] == UNIT, "accepted_binding")

def recovered(before, after):
    try:
        return (all(after[k] == before[k] for k in ("invocation", "boot", "started"))
                and after["cgroup"] in ("", before["cgroup"])
                and after["state"] == "inactive" and after["result"] == "success"
                and after["pid"] == 0 and after["code"] == "1" and after["status"] == "0"
                and int(after["exited"]) - int(after["started"]) >= 89_000_000)
    except (KeyError, ValueError, TypeError):
        return False

def guard_module():
    path = pathlib.Path(__file__).with_name("helper.py")
    raw = read(path)
    need(sha(raw) == HELPER_SHA, "helper_pin")
    spec = importlib.util.spec_from_file_location("limited208_guard", path)
    module = importlib.util.module_from_spec(spec)
    # Protected root-owned immutable path; checked before executing imported code.
    spec.loader.exec_module(module)
    return module

class Canary:
    def __init__(self, guard):
        self.g = guard
        self.manifest = guard.decode(read(CONFIG / "manifest.json"))
        guard.keys(self.manifest, "schema_version incident_id source_sha")
        need(self.manifest["schema_version"] == 1, "manifest")
        self.incident, self.accepted = self.manifest["incident_id"], None
        self.context = None
        try:
            context_raw = read(CONFIG / "context.json")
        except FileNotFoundError:
            return
        c = guard.decode(context_raw)
        guard.keys(c, "request accepted_comment native_witness profile_sha256 prompt_sha256 sop_pins fixture_unit_sha256")
        profile_raw = read(pathlib.Path(guard.PROFILE))
        need(sha(profile_raw) == c["profile_sha256"], "profile_pin")
        self.profile = guard.Profile(guard.decode(profile_raw), owner=0)
        p = self.profile.data
        need(p["source_sha"] == self.manifest["source_sha"] and p["model_uid"] == 988
             and p["model_gid"] == 982 and p["transport_uid"] == 987
             and p["max_consumed_attempts"] == 1
             and p["helper_path"] == str(pathlib.Path(__file__).with_name("helper.py"))
             and p["helper_sha256"] == HELPER_SHA
             and p["resources"] == dict(cpu_quota_percent=100, memory_max_bytes=2*1024**3,
                memory_swap_max_bytes=0, tasks_max=128, runtime_max_seconds=120,
                output_max_bytes=1024**2, tmp_max_bytes=64*1024**2, work_max_bytes=64*1024**2), "finite_profile")
        self.authority = guard.Authority(self.profile, guard.LinuxHost(self.profile))
        request = self.profile.request(c["request"])
        a = guard.decode(c["accepted_comment"].encode())
        validate_owner(request, a, self.incident)
        sops = {"/home/dev/Projects/gascity-kb/common/infrastructure/repairer-operations.md",
                "/home/dev/Projects/gascity-kb/common/infrastructure/agent-repair-handoff.md",
                "/home/dev/Projects/gascity-kb/roles/repairer.md"}
        need(type(c["sop_pins"]) is dict and set(c["sop_pins"]) == sops
             and all(type(v) is str and HEX.fullmatch(v) for v in c["sop_pins"].values())
             and a["source_sha"] == p["source_sha"] and a["sop_pins"] == c["sop_pins"]
             and a["generation_source"] == "native_session", "accepted_pins")
        a["accepted_comment_sha256"] = sha(c["accepted_comment"].encode())
        witness = c["native_witness"]
        guard.keys(witness, "incident_id owner_session generation incarnation_sha256 accepted_comment_sha256")
        need(all(witness[k] == a[k] for k in witness), "native_witness")
        self.context, self.accepted, self.request = c, a, request

    def row(self):
        with self.authority.locked() as state:
            row = self.authority.exact(state, self.request)
            return dict(row)

    def persisted_terminal(self, row):
        raw = durable_read(ACTION / "terminal.json")
        receipt = self.g.decode(raw)
        need(set(receipt) == {"schema_version", "context_sha256", "identity", "empty_scope", "model_native", "action"}
             and receipt["schema_version"] == 1
             and receipt["context_sha256"] == sha(read(CONFIG / "context.json"))
             and row["phase"] == "exited" and row["attested"] and not row["revoked"]
             and receipt["identity"] == row["identity"]
             and receipt["empty_scope"] == row["empty_scope"]
             and receipt["empty_scope"]["empty"] is True
             and self.authority.host.scope() == receipt["empty_scope"], "terminal_receipt")
        view = receipt["model_native"]
        need(view["MainPID"] == "0" and view["InvocationID"] == row["identity"]["invocation_id"]
             and view["ExecMainCode"] == "1" and view["ExecMainStatus"] == "0"
             and view["Result"] == "success", "model_terminal_receipt")
        action = receipt["action"]
        need(action["sensor"] == "recovered" and action["intent"]["context_sha256"] == receipt["context_sha256"]
             and action["intent"]["before"]["state"] == "inactive" and action["intent"]["before"]["pid"] == 0
             and action["active"]["context_sha256"] == receipt["context_sha256"]
             and recovered(action["active"]["proof"], action["terminal"])
             and fixture_empty(), "action_terminal_receipt")
        return sha(raw)

    def status(self):
        if self.context is None:
            return projection("pending_profile", self.incident, None)
        self.authority.observe(self.request)
        row = self.row()
        if os.path.lexists(ACTION / "terminal.json"):
            return projection("resolved", self.incident, self.accepted, self.persisted_terminal(row))
        if row["phase"] == "staged" and not row["ever_consumed"]:
            need(not os.path.lexists(CONFIG / "run-intent.json"), "run_intent")
            need(sha(read(self.authority.directory / (self.request["attempt"] + ".prompt"))) == self.context["prompt_sha256"], "prompt_pin")
            self.authority.host.resources()
            need(self.authority.host.scope() == row["scope"], "scope_changed")
            return projection("ready", self.incident, self.accepted)
        if row["phase"] == "entered" and row["attested"] and not row["revoked"]:
            return projection("running", self.incident, self.accepted)
        if row["phase"] == "exited" and row["attested"] and not row["revoked"] and self.terminal_ok(row):
            evidence = self.action_complete()
            if evidence == "pending":
                return projection("running", self.incident, self.accepted)
            if evidence:
                receipt = dict(schema_version=1, context_sha256=sha(read(CONFIG / "context.json")),
                               identity=row["identity"], empty_scope=row["empty_scope"],
                               model_native=self.model_terminal_view, action=evidence)
                try:
                    exclusive(ACTION / "terminal.json", receipt)
                except FileExistsError:
                    pass
                return projection("resolved", self.incident, self.accepted, self.persisted_terminal(row))
        return projection("hold", self.incident, self.accepted)

    def run(self):
        need(self.context is not None and self.status()["state"] == "ready", "not_ready")
        socket_ready(self.authority.host, self.authority.host.unit(self.row()))
        exclusive(CONFIG / "run-intent.json", {"context_sha256": sha(read(CONFIG / "context.json"))})
        self.authority.enter(self.request["attempt"], 987)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            state = self.status()
            if state["state"] == "running":
                return state
            time.sleep(.15)
        return projection("hold", self.incident, self.accepted)

    def terminal_ok(self, row):
        view = self.authority.host.show(row["identity"]["unit"])
        self.model_terminal_view = view
        return (view["MainPID"] == "0" and view["InvocationID"] == row["identity"]["invocation_id"]
                and view["ExecMainCode"] == "1" and view["ExecMainStatus"] == "0"
                and view["Result"] == "success"
                and row.get("empty_scope", {}).get("empty") is True
                and self.authority.host.scope() == row["empty_scope"])

    def action_complete(self):
        check_directory(ACTION)
        active = self.g.decode(read(ACTION / "active.json"))
        need(active["context_sha256"] == sha(read(CONFIG / "context.json")), "action_context")
        intent = self.g.decode(read(ACTION / "intent.json"))
        need(intent["context_sha256"] == active["context_sha256"] and intent["before"]["state"] == "inactive"
             and intent["before"]["pid"] == 0, "action_intent")
        before, after = active["proof"], fixture_view(self.authority.host)
        if after == before:
            need(time.monotonic() * 1_000_000 <= int(before["started"]) + 120_000_000, "cycle_deadline")
            return "pending"
        if not recovered(before, after):
            return None
        need(fixture_empty(), "sensor_recovery")
        return {"intent": intent, "active": active, "terminal": after, "sensor": "recovered"}

    def authorize(self, peer):
        need(self.authority.host.peer(peer["pid"]) == peer, "peer_changed")
        frame = {"schema_version": 1, "profile_id": self.profile.data["profile_id"], "peer": peer}
        observed = self.authority.queue_observe(frame)
        identity = observed["identity"]
        need(all(identity[k] == self.request[k] for k in ("generation", "instance_token", "attempt"))
             and identity["sid"] == self.request["session_id"], "action_identity")

    def action(self, peer, op):
        need(op in ("status", "restart"), "action_op")
        self.authorize(peer)
        if op == "status":
            view = fixture_view(self.authority.host)
            return dict(entered=True, unit=UNIT, state=view["state"], main_pid=view["pid"],
                        invocation_id=view["invocation"], result=view.get("result"))
        check_directory(ACTION)
        lock = os.open(ACTION / "lock", os.O_RDWR | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            info = os.fstat(lock)
            need(stat.S_ISREG(info.st_mode) and info.st_uid == 0 and info.st_nlink == 1
                 and info.st_mode & 0o077 == 0, "action_lock")
            fcntl.flock(lock, fcntl.LOCK_EX)
            self.authorize(peer)
            need(not os.path.lexists(ACTION / "intent.json") and not os.path.lexists(ACTION / "active.json"), "action_spent")
            need(sha(protected_unit()) == self.context["fixture_unit_sha256"], "fixture_unit")
            effective_unit(self.authority.host)
            before = fixture_view(self.authority.host)
            need(before["state"] == "inactive" and before["pid"] == 0 and fixture_empty(), "fixture_not_inactive")
            exclusive(ACTION / "intent.json", {"context_sha256": sha(read(CONFIG / "context.json")), "before": before})
            self.authorize(peer)
            subprocess.run(["/usr/bin/systemctl", "restart", UNIT], stdin=subprocess.DEVNULL,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True,
                           timeout=5, env={"PATH": "/usr/bin:/bin", "LANG": "C"})
            active = fixture_view(self.authority.host)
            need(active["state"] == "active" and active["pid"] > 0
                 and re.fullmatch("[0-9a-f]{32}", active["invocation"]), "action_postcheck")
            time.sleep(5)
            need(fixture_view(self.authority.host) == active, "action_unstable")
            exclusive(ACTION / "active.json", {"context_sha256": sha(read(CONFIG / "context.json")), "proof": active})
            return {"restarted": True}
        finally:
            os.close(lock)

def protected_unit():
    # Unit fragment is public root-owned0644; private-file reader intentionally rejects that mode.
    before = UNIT_FILE.lstat()
    need(stat.S_ISREG(before.st_mode) and before.st_uid == 0 and before.st_nlink == 1
         and before.st_mode & 0o022 == 0, "unit_file")
    for parent in UNIT_FILE.parents:
        info = parent.lstat()
        need(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and info.st_mode & 0o022 == 0, "unit_parent")
    fd = os.open(UNIT_FILE, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        need((os.fstat(fd).st_dev, os.fstat(fd).st_ino) == (before.st_dev, before.st_ino), "unit_changed")
        raw = os.read(fd, 8193)
        need(len(raw) <= 8192, "unit_size")
        return raw
    finally:
        os.close(fd)

def durable_read(path):
    raw = read(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        os.fsync(fd)
        parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        os.close(fd)
    return raw

def fixture_empty():
    path = pathlib.Path("/sys/fs/cgroup/system.slice") / UNIT
    if not path.exists():
        return True
    for directory, _, _ in os.walk(path):
        info = pathlib.Path(directory).lstat()
        need(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and info.st_mode & 0o022 == 0, "fixture_scope")
        if pathlib.Path(directory, "cgroup.procs").read_text().strip():
            return False
    return True

def effective_unit(host):
    fields = "DropInPaths Transient NeedDaemonReload ExecCondition ExecStartPre ExecStartPost ExecStop ExecStopPost ExecReload ExecStart Type Slice DynamicUser Restart RemainAfterExit"
    raw = host.command(["/usr/bin/systemctl", "show", UNIT, "--property=" + fields.replace(" ", ",")])
    view = dict(line.split("=", 1) for line in raw.splitlines())
    validate_effective(view)

def validate_effective(view):
    empty = ("DropInPaths", "ExecCondition", "ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "ExecReload")
    need(all(view[k] == "" for k in empty) and view["Transient"] == view["NeedDaemonReload"] == "no"
         and view["Type"] == "exec" and view["Slice"] == "system.slice"
         and view["DynamicUser"] == "yes" and view["Restart"] == view["RemainAfterExit"] == "no", "effective_unit")
    start = view["ExecStart"]
    need(start.startswith("{ path=/usr/bin/sleep ; argv[]=/usr/bin/sleep 90 ; ignore_errors=no ; ")
         and start.endswith(" }") and start.count("path=") == start.count("argv[]=") == 1, "effective_start")

def fixture_view(host):
    fields = "Id LoadState ActiveState Result MainPID InvocationID ControlGroup ExecMainCode ExecMainStatus ExecMainStartTimestampMonotonic ExecMainExitTimestampMonotonic FragmentPath DynamicUser Restart"
    raw = host.command(["/usr/bin/systemctl", "show", UNIT, "--property=" + fields.replace(" ", ",")])
    view = dict(line.split("=", 1) for line in raw.splitlines())
    need(set(view) == set(fields.split()) and view["Id"] == UNIT and view["LoadState"] == "loaded"
         and view["FragmentPath"] == str(UNIT_FILE) and view["DynamicUser"] == "yes"
         and view["Restart"] == "no", "fixture_view")
    pid = int(view["MainPID"])
    need(pid >= 0, "fixture_pid")
    peer = host.peer(pid) if pid else {"start": ""}
    if pid:
        need(peer["cgroup"] == view["ControlGroup"]
             and os.readlink("/proc/" + str(pid) + "/exe") == "/usr/bin/sleep"
             and pathlib.Path("/proc/" + str(pid) + "/cmdline").read_bytes() == b"/usr/bin/sleep\x0090\x00", "fixture_process")
    need(view["ControlGroup"] in ("", "/system.slice/" + UNIT), "fixture_cgroup")
    return dict(state=view["ActiveState"], result=view["Result"], pid=pid, start=peer["start"],
                invocation=view["InvocationID"], cgroup=view["ControlGroup"], boot=host.boot(),
                code=view["ExecMainCode"], status=view["ExecMainStatus"],
                started=view["ExecMainStartTimestampMonotonic"], exited=view["ExecMainExitTimestampMonotonic"])

def socket_parent(parent):
    for ancestor in pathlib.Path(parent).parents:
        info = ancestor.lstat()
        need(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and info.st_mode & 0o022 == 0, "socket_ancestor")
    info = pathlib.Path(parent).lstat()
    need(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and info.st_gid == 982
         and stat.S_IMODE(info.st_mode) in (0o710, 0o750), "socket_directory")

def socket_ready(host, model_unit):
    socket_parent(pathlib.Path(SOCKET).parent)
    info = pathlib.Path(SOCKET).lstat()
    need(stat.S_ISSOCK(info.st_mode) and (info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)) == (0, 982, 0o660), "socket_mode")
    unit = "gc-repairer-limited208-action.service"
    raw = host.command(["/usr/bin/systemctl", "show", unit, "--property=ActiveState,MainPID,ControlGroup,After,Wants,Requires"])
    view = dict(line.split("=", 1) for line in raw.splitlines())
    need(set(view) == {"ActiveState", "MainPID", "ControlGroup", "After", "Wants", "Requires"} and view["ActiveState"] == "active"
         and view["ControlGroup"] == "/system.slice/" + unit, "socket_service")
    need({UNIT, model_unit} <= set(view["After"].split())
         and not {UNIT, model_unit} & (set(view["Wants"].split()) | set(view["Requires"].split())), "retention_reference")
    pid = int(view["MainPID"])
    peer = host.peer(pid)
    need(peer["uid"] == peer["gid"] == 0 and peer["cgroup"] == view["ControlGroup"], "socket_process")
    matches = [line.split()[6] for line in pathlib.Path("/proc/net/unix").read_text().splitlines()[1:]
               if len(line.split()) == 8 and line.split()[7] == SOCKET]
    need(len(matches) == 1 and any(os.readlink(fd) == "socket:[" + matches[0] + "]"
         for fd in pathlib.Path("/proc/" + str(pid) + "/fd").iterdir()), "socket_fd")
    need(host.peer(pid) == peer, "socket_process_changed")

def serve(canary):
    # Directory and socket installed by a separately reviewed root unit; no path recreation.
    parent = pathlib.Path(SOCKET).parent
    socket_parent(parent)
    need(not os.path.lexists(SOCKET), "socket_exists")
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as server:
        server.bind(SOCKET)
        os.chown(SOCKET, 0, 982)
        os.chmod(SOCKET, 0o660)
        server.listen(1)
        deadline = time.monotonic() + 300
        server.settimeout(1)
        while time.monotonic() < deadline:
            try:
                conn, _ = server.accept()
            except TimeoutError:
                continue
            with conn:
                conn.settimeout(2)
                pid, uid, gid = struct.unpack("3i", conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
                try:
                    need((uid, gid) == (988, 982), "socket_peer")
                    raw = conn.recv(257)
                    need(len(raw) <= 256, "socket_frame")
                    request = canary.g.decode(raw)
                    canary.g.keys(request, "op")
                    result = canary.action(canary.authority.host.peer(pid), request["op"])
                except Exception:
                    result = {"hold": True}
                conn.sendall(encoded(result))

def main():
    if os.geteuid() != 0:
        dispatch(os.environ.get("SSH_ORIGINAL_COMMAND", ""), sys.stdin.buffer.read(1))
        return
    need(len(sys.argv) == 2 and sys.argv[1] in ("status", "run", "serve"), "mode")
    mode = sys.argv[1]
    need(mode == "serve" or os.environ.get("SUDO_UID") == "987", "sudo_principal")
    if mode != "serve":
        need(sys.stdin.buffer.read(1) == b"", "stdin")
    g = guard_module()
    canary = Canary(g)
    if mode == "serve":
        need(canary.context is not None, "no_context")
        serve(canary)
        return
    try:
        result = canary.run() if mode == "run" else canary.status()
    except Exception:
        result = projection("hold", canary.incident, canary.accepted)
    print(encoded(result).decode())

if __name__ == "__main__":
    try:
        main()
    except Exception:
        # No context/identity/private paths/exception detail crosses the boundary.
        sys.exit(1)
