"""One native890 queue owner and one bounded VM invocation; no model on cloud."""
from dataclasses import dataclass
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import stat

MAX_FRAME = 65536
ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,255}\Z")
SHA = re.compile(r"[0-9a-f]{64}\Z")

class Hold(Exception):
    """No retry or lifecycle completion is justified by the available evidence."""


def require(condition, reason):
    if not condition:
        raise Hold(reason)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def decode(raw):
    require(len(raw) <= MAX_FRAME, "frame_size")
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate_json_key")
            result[key] = value
        return result
    try:
        return json.loads(raw, object_pairs_hook=unique)
    except (ValueError, UnicodeError) as error:
        raise Hold("invalid_json") from error


@dataclass(frozen=True, repr=False)
class Session:
    sid: str
    token: str
    template: str

    def __post_init__(self):
        require(ID.fullmatch(self.sid) is not None, "session_id")
        require(ID.fullmatch(self.template) is not None, "template")
        require(bool(self.token) and len(self.token) <= 512, "session_token_missing")

    def matches(self, row, allow_draining=False):
        metadata = row.get("metadata", {})
        require(row.get("id") == self.sid and row.get("status") != "closed", "session_closed")
        require(metadata.get("state") in (("active", "awake", "draining") if allow_draining else ("active", "awake")), "session_not_awake")
        require(metadata.get("template") == self.template, "session_template")
        require(hmac.compare_digest(metadata.get("instance_token", ""), self.token), "session_incarnation")
        require(re.fullmatch(r"[1-9][0-9]{0,19}", metadata.get("generation", "")) is not None, "session_generation")
        return metadata["generation"]


class OncePermit:
    """Retained consumed-before-work receipt; restart never replays any phase."""
    def __init__(self, directory):
        self.directory = Path(directory)

    def reserve(self, receipt):
        descriptor = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            info = os.fstat(descriptor)
            require(info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, "state_directory")
            try:
                child = os.open("once.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
            except OSError as error:
                raise Hold("permit_already_consumed_or_unknown") from error
            with os.fdopen(child, "w") as stream:
                stream.write(canonical(receipt))
                stream.flush()
                os.fsync(stream.fileno())
            os.fsync(descriptor)
        finally:
            os.close(descriptor)


class NativeQueue:
    """Only fixed native890 CLI verbs; the inherited runtime is the actual owner."""
    def __init__(self, invoke, session, fixture, artifact):
        require(ID.fullmatch(fixture) is not None, "fixture_id")
        require(type(artifact) is dict and set(artifact) == {"commit", "branch", "work_dir"}, "artifact_schema")
        require(type(artifact["commit"]) is str and re.fullmatch(r"[0-9a-f]{40}", artifact["commit"]), "artifact_commit")
        require(type(artifact["branch"]) is str and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_./-]{0,200}", artifact["branch"]) and ".." not in artifact["branch"], "artifact_branch")
        require(type(artifact["work_dir"]) is str and Path(artifact["work_dir"]).is_absolute(), "artifact_work_dir")
        self.artifact = dict(artifact)
        self.invoke = invoke
        self.session = session
        self.fixture = fixture
        self.assignee = None
        self.lease_id = None
        self.generation = None
        self.pin_check = lambda: None

    def gc(self, *args, payload=None, allowed=(0,)):
        code, raw = self.invoke("gc", list(args), payload, allowed)
        require(code in allowed and len(raw) <= MAX_FRAME, "native_command_failed")
        return code, raw

    def lease(self, *args):
        code, raw = self.invoke("lease", list(args), None, (0,))
        require(code == 0, "editor_lease_failed")
        return raw

    def editor_owned(self):
        self.lease("check-lease", self.fixture)
        status = decode(self.lease("status"))
        require(type(status) is dict and isinstance(status.get("leases"), list), "editor_status_shape")
        holders = [item for item in status["leases"] if item.get("bd") == self.fixture]
        require(len(holders) == 1, "editor_status_missing")
        holder = holders[0]
        require(holder.get("session_id") == self.session.sid and holder.get("holder_host") == os.uname().nodename.split(".")[0], "editor_identity")
        require(self.lease_id and holder.get("lease_id") == self.lease_id and holder.get("stale") is False, "editor_stale_or_changed")

    def show(self, identity, comments=False):
        args = ["bd", "show", identity, "--json"]
        if comments:
            args.append("--include-comments")
        _, raw = self.gc(*args)
        rows = decode(raw)
        require(isinstance(rows, list) and len(rows) == 1 and isinstance(rows[0], dict), "canonical_row_shape")
        require(rows[0].get("id") == identity, "canonical_row_id")
        return rows[0]

    def owned(self, closed=False):
        row = self.show(self.fixture, comments=True)
        require(row.get("status") == ("closed" if closed else "in_progress"), "work_status")
        require(self.assignee and row.get("assignee") == self.assignee, "work_assignee")
        require(row.get("metadata", {}).get("gc.session_id") == self.session.sid, "work_session_stamp")
        require(self.session.matches(self.show(self.session.sid), allow_draining=closed) == self.generation, "session_generation_changed")
        if not closed:
            _, current = self.gc("hook", "current", "--id-only")
            require(current.decode().strip() == self.fixture, "current_claim_mismatch")
        return row

    def claim(self):
        self.pin_check()
        self.generation = self.session.matches(self.show(self.session.sid))
        _, discovered = self.gc("hook", "--json")
        candidates = decode(discovered)
        require(type(candidates) is list and len(candidates) == 1 and type(candidates[0]) is dict and candidates[0].get("id") == self.fixture, "fixture_discovery_scope")
        lease = decode(self.lease("claim", self.fixture, "--reason", "issue208 exact fixture coordinator", "--hold", "15m", "--nowait"))
        require(type(lease) is dict and lease.get("bd") == self.fixture and isinstance(lease.get("lease_id"), str) and lease["lease_id"], "editor_claim_receipt")
        self.lease_id = lease["lease_id"]
        self.editor_owned()
        _, raw = self.gc("hook", "--claim", "--json")
        claim = decode(raw)
        require(isinstance(claim, dict) and claim.get("action") == "work" and claim.get("reason") == "claimed", "claim_not_work")
        require(claim.get("bead_id") == self.fixture and isinstance(claim.get("assignee"), str) and claim["assignee"], "claim_fixture_mismatch")
        self.assignee = claim["assignee"]
        self.owned()

    def write(self, *args, payload=None):
        self.pin_check()
        self.owned()
        self.editor_owned()
        return self.gc(*args, payload=payload)

    def comment(self, text):
        require(len(text.encode()) <= 8192 and self.session.token not in text, "comment_size_or_secret")
        self.write("bd", "comment", self.fixture, "--stdin", payload=text.encode())
        comments = self.owned().get("comments")
        require(isinstance(comments, list) and any(isinstance(item, dict) and item.get("text") == text for item in comments), "comment_readback")

    def accepted(self, receipt):
        text = canonical(receipt)
        digest = hashlib.sha256(text.encode()).hexdigest()
        self.comment(text)
        self.write("bd", "update", self.fixture, "--set-metadata", "repairer.accepted_sha256=" + digest)
        require(self.owned().get("metadata", {}).get("repairer.accepted_sha256") == digest, "accepted_metadata_readback")
        return {"receipt": receipt, "comment_sha256": digest}

    def pre_run(self, accepted):
        self.pin_check()
        row = self.owned()
        digest = accepted["comment_sha256"]
        text = canonical(accepted["receipt"])
        require(hashlib.sha256(text.encode()).hexdigest() == digest, "local_accepted_digest")
        require(row.get("metadata", {}).get("repairer.accepted_sha256") == digest, "accepted_changed_before_run")
        require(any(type(item) is dict and item.get("text") == text for item in row.get("comments", [])), "accepted_comment_changed_before_run")
        self.editor_owned()

    def finish(self, result):
        require(type(result) is dict and set(result) == {"state", "evidence_sha256", "summary"}, "result_shape")
        require(result["state"] == "resolved" and isinstance(result["evidence_sha256"], str) and SHA.fullmatch(result["evidence_sha256"]), "result_not_verified")
        summary = result["summary"]
        require(isinstance(summary, str) and 0 < len(summary.encode()) <= 4096 and all(ord(c) >= 32 for c in summary), "result_summary")
        text = canonical(result)
        self.comment(text)
        metadata = {"gc.work_outcome": "shipped", "gc.work_commit": self.artifact["commit"], "gc.work_branch": self.artifact["branch"], "gc.work_dir": self.artifact["work_dir"]}
        arguments = ["bd", "update", self.fixture]
        for key, value in metadata.items():
            arguments.extend(("--set-metadata", key + "=" + value))
        self.write(*arguments)
        actual = self.owned().get("metadata", {})
        require(all(actual.get(key) == value for key, value in metadata.items()), "artifact_readback")
        self.write("bd", "close", self.fixture, "--reason-file", "-", payload=text.encode())
        self.owned(closed=True)
        _, raw = self.gc("hook", "--claim", "--json", allowed=(1,))
        idle = decode(raw)
        require(isinstance(idle, dict) and idle.get("action") == "drain" and idle.get("reason") in ("no_work", "drain_pending"), "idle_not_proven")
        self.owned(closed=True)
        self.gc("runtime", "drain-ack", "--json")


def coordinate(queue, permit, transport, accepted):
    """One attempt. A caller receiving Hold must remain alive and request review."""
    require(type(accepted) is dict and accepted.get("incident_id") == queue.fixture and accepted.get("owner_session") == queue.session.sid, "accepted_identity")
    require(queue.session.token not in canonical(accepted), "accepted_secret")
    permit.reserve({"incident_id": queue.fixture, "owner_session": queue.session.sid, "state": "consumed_before_claim"})
    queue.claim()
    accepted = {**accepted, "generation": queue.generation, "incarnation_sha256": hashlib.sha256(queue.session.token.encode()).hexdigest()}
    receipt = queue.accepted(accepted)
    transport.await_ready(receipt)
    queue.pre_run(receipt)
    # No exception handler may issue a second run: timeout means unknown.
    result = transport.run_once()
    queue.finish(result)
    return "resolved"


class NativeCLI:
    """Bound native stdout on disk, suppress diagnostics that could contain env data."""
    def __init__(self, gc_binary, lease_binary, city, environment):
        import shutil
        self.gc_binary = gc_binary
        self.lease_binary = lease_binary
        self.city = city
        self.environment = dict(environment)
        sid = environment.get("GC_SESSION_ID", "")
        require(sid and environment.get("GC_INSTANCE_TOKEN"), "native_runtime_identity_missing")
        require(not environment.get("CLAUDE_SESSION_ID") or environment["CLAUDE_SESSION_ID"] == sid, "conflicting_editor_identity")
        require(environment.get("GC_CITY_PATH") == city, "native_city_mismatch")
        require(not any(environment.get(key) for key in ("GT_BD_STATE", "GT_BD_REMOTE", "GT_PROCESS_NAMES")), "editor_environment_override")
        for binary in (gc_binary, lease_binary):
            require(Path(binary).is_absolute() and shutil.which(binary) == binary, "native_binary_path")

    def __call__(self, tool, args, payload=None, allowed=(0,)):
        require(tool in ("gc", "lease"), "native_tool")
        command = ([self.gc_binary, "--city", self.city] if tool == "gc" else [self.lease_binary]) + args
        return bounded_process(command, self.environment, payload, 25, allowed)


def bounded_process(command, environment, payload, timeout, allowed):
    """Each child has a bounded stdout file; timeout never retries a mutation."""
    import resource
    import subprocess
    import tempfile
    def cap_file():
        resource.setrlimit(resource.RLIMIT_FSIZE, (MAX_FRAME, MAX_FRAME))
    with tempfile.TemporaryFile() as output:
        try:
            result = subprocess.run(command, **({"input": payload} if payload is not None else {"stdin": subprocess.DEVNULL}), stdout=output, stderr=subprocess.DEVNULL,
                                    timeout=timeout, env=environment, check=False, preexec_fn=cap_file)
        except (OSError, subprocess.TimeoutExpired) as error:
            raise Hold("command_unknown") from error
        output.seek(0)
        raw = output.read(MAX_FRAME + 1)
    require(result.returncode in allowed and len(raw) < MAX_FRAME, "command_refused_or_output_limit")
    return result.returncode, raw


SSH_COMMAND = ["/usr/bin/ssh", "-F", "/dev/null", "-p", "22", "-i", "/home/dev/.ssh/id_ed25519",
               "-o", "UserKnownHostsFile=/home/dev/.ssh/repairer-limited-208-probe-3ff1fe77cc0d44ce8e100ff87dae8d3f/known_hosts",
               "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostKeyAlias=repairer-vm328699-limited-208",
               "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "StrictHostKeyChecking=yes",
               "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "BatchMode=yes",
               "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ControlPath=none",
               "-o", "ControlMaster=no", "-o", "ConnectTimeout=5", "-o", "RequestTTY=no",
               "repairer-transport@100.101.187.36"]
SSH_ENVIRONMENT = {"HOME": "/home/dev", "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}
WIRE_FIELDS = {"schema_version", "state", "incident_id", "owner_session", "generation", "incarnation_sha256",
               "accepted_comment_sha256", "evidence_sha256", "summary"}
IDENTITY_FIELDS = ("owner_session", "generation", "incarnation_sha256", "accepted_comment_sha256")


class SSHTransport:
    """Fixed endpoint/verbs and empty stdin; run is never repeated after uncertainty."""
    def __init__(self, invoke=bounded_process, clock=None, sleep=None):
        import time
        self.invoke = invoke
        self.clock = clock or time.monotonic
        self.sleep = sleep or time.sleep
        self.expected = None
        self.ran = False

    def request(self, verb, timeout=15):
        require(verb in ("status", "run"), "transport_verb")
        _, raw = self.invoke([*SSH_COMMAND, "repairer-limited208 " + verb], dict(SSH_ENVIRONMENT), b"", 60 if verb == "run" else timeout, (0,))
        frame = decode(raw)
        require(type(frame) is dict and set(frame) == WIRE_FIELDS and type(frame["schema_version"]) is int and frame["schema_version"] == 1, "transport_schema")
        require(frame["state"] in ("pending_profile", "ready", "running", "resolved", "hold"), "transport_state")
        require(frame["incident_id"] == self.expected["incident_id"], "transport_incident")
        summary = frame["summary"]
        require(type(summary) is str and 0 < len(summary.encode()) <= 4096 and all(ord(c) >= 32 for c in summary), "transport_summary")
        if frame["state"] == "pending_profile":
            require(all(frame[key] is None for key in (*IDENTITY_FIELDS, "evidence_sha256")), "pending_identity")
        else:
            require(all(frame[key] == self.expected[key] for key in IDENTITY_FIELDS), "transport_identity")
            require((type(frame["evidence_sha256"]) is str and SHA.fullmatch(frame["evidence_sha256"])) if frame["state"] == "resolved" else frame["evidence_sha256"] is None, "transport_evidence")
        return frame

    def await_ready(self, accepted):
        receipt = accepted["receipt"]
        self.expected = {key: receipt[key] for key in ("incident_id", "owner_session", "generation", "incarnation_sha256")}
        self.expected["accepted_comment_sha256"] = accepted["comment_sha256"]
        deadline = self.clock() + 120
        while self.clock() < deadline:
            frame = self.request("status", min(15, deadline - self.clock()))
            require(self.clock() < deadline, "profile_readiness_deadline")
            if frame["state"] == "ready":
                return
            require(frame["state"] == "pending_profile", "profile_not_ready")
            self.sleep(min(2, max(0, deadline - self.clock())))
        raise Hold("profile_readiness_deadline")

    def run_once(self):
        require(self.expected is not None and not self.ran, "run_already_consumed")
        self.ran = True
        deadline = self.clock() + 240
        try:
            frame = self.request("run")
        except Hold:
            # Lost/invalid RUN reply can only be reconciled by read-only STATUS.
            frame = None
        while self.clock() < deadline:
            if frame is not None:
                require(frame["state"] in ("running", "resolved"), "run_held_or_unknown")
                if frame["state"] == "resolved":
                    return {key: frame[key] for key in ("state", "evidence_sha256", "summary")}
            self.sleep(min(2, max(0, deadline - self.clock())))
            if self.clock() >= deadline:
                break
            frame = self.request("status")
        raise Hold("run_reconciliation_deadline")


def private_json(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid in (0, os.getuid()) and stat.S_IMODE(info.st_mode) == 0o600, "private_profile")
        return decode(stream.read(MAX_FRAME + 1))


def main():
    import sys
    import time
    city = "/home/dev/Projects/gascity-cloud"
    try:
        require(len(sys.argv) == 1 and os.getuid() == 1000, "coordinator_invocation")
        profile = private_json(city + "/.gc/runtime/repairer-limited208/profile.json")
        require(type(profile) is dict and set(profile) == {"schema_version", "fixture_id", "gc_binary", "state_dir", "artifact", "accepted_base"} and profile["schema_version"] == 1, "profile_schema")
        require(profile["gc_binary"] == "/home/dev/.local/share/gascity/tools/gascity-890c59dc0a1cd9be4149113b32a63f34eca7a189-codex-submit142/gc", "native_binary_pin")
        require(profile["state_dir"] == city + "/.gc/runtime/repairer-limited208", "profile_state_path")
        session = Session(os.environ.get("GC_SESSION_ID", ""), os.environ.get("GC_INSTANCE_TOKEN", ""), "repairer-coordinator")
        require(os.environ.get("GC_TEMPLATE") == session.template, "runtime_template")
        environment = dict(os.environ)
        environment["GC_WORK_RECORD_ENFORCE"] = "1"
        cli = NativeCLI(profile["gc_binary"], "/home/dev/.local/bin/gt-bd", city, environment)
        queue = NativeQueue(cli, session, profile["fixture_id"], profile["artifact"])
        base = profile["accepted_base"]
        require(type(base) is dict and set(base) == {"executor_host", "target_host", "resource", "scope", "source_sha", "sop_pins", "next_checkpoint"}, "accepted_base_schema")
        require(base["executor_host"] == "vm328699" and base["source_sha"] == profile["artifact"]["commit"], "source_executor_pin")
        expected_sops = {"/home/dev/Projects/gascity-kb/common/infrastructure/repairer-operations.md", "/home/dev/Projects/gascity-kb/common/infrastructure/agent-repair-handoff.md", "/home/dev/Projects/gascity-kb/roles/repairer.md"}
        require(type(base["sop_pins"]) is dict and set(base["sop_pins"]) == expected_sops, "sop_pin_paths")
        def verify_sops():
            for path, digest in base["sop_pins"].items():
                require(type(digest) is str and SHA.fullmatch(digest) and hashlib.sha256(Path(path).read_bytes()).hexdigest() == digest, "sop_pin_mismatch")
        verify_sops()
        queue.pin_check = verify_sops
        accepted = {**base, "incident_id": queue.fixture, "owner_session": session.sid, "generation_source": "native_session"}
        coordinate(queue, OncePermit(profile["state_dir"]), SSHTransport(), accepted)
        return 0
    except Exception as error:
        reason = str(error) if type(error) is Hold else "coordinator_unknown"
        # Do not print arbitrary exception/CLI/model data or let the old controller
        # interpret a failed coordinator exit as permission to retry the model.
        print(canonical({"state": "hold", "reason": reason}), flush=True)
        while True:
            time.sleep(60)


if __name__ == "__main__":
    raise SystemExit(main())
