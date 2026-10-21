#!/usr/bin/python3
"""Root-owned finite remote admission authority. No production profile is shipped."""
import contextlib
import datetime
import fcntl
import hashlib
import json
import math
import os
import pathlib
import re
import stat
import subprocess
import sys
import time
import uuid

PROFILE = "/etc/gascity/remote-admission/profile.json"
MAX_FRAME = 262144
HEX32 = re.compile(r"[0-9a-f]{32}\Z")
HEX40 = re.compile(r"[0-9a-f]{40}\Z")
HEX64 = re.compile(r"[0-9a-f]{64}\Z")

class Refusal(Exception):
    """Public, content-free refusal code; input values are never logged."""

def require(condition, code):
    if not condition:
        raise Refusal(code)

def decode(raw):
    require(len(raw) <= MAX_FRAME, "frame_too_large")
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate_key")
            result[key] = value
        return result
    try:
        require(type(raw) is bytes, "bytes_required")
        def finite(text):
            value = float(text)
            require(math.isfinite(value), "nonfinite")
            return value
        value = json.loads(raw.decode("utf-8", "strict"), object_pairs_hook=pairs, parse_float=finite,
                           parse_constant=lambda _: (_ for _ in ()).throw(Refusal("nonfinite")))
    except (ValueError, UnicodeError, RecursionError):
        raise Refusal("invalid_json") from None
    require(type(value) is dict, "object_required")
    return value

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()

def keys(value, expected):
    require(type(value) is dict and set(value) == set(expected.split()), "schema")

def integer(value, minimum=1):
    require(type(value) is int and value >= minimum, "integer")

def secure_path(path, owner, directory=False):
    """Reject symlinks and writable ancestors before opening private authority files."""
    path = pathlib.Path(path)
    require(path.is_absolute(), "absolute_path")
    for parent in reversed([path, *path.parents]):
        info = parent.lstat()
        require(not stat.S_ISLNK(info.st_mode), "symlink")
        require(info.st_uid in (0, owner) and info.st_mode & 0o022 == 0, "unsafe_owner_mode")
    info = path.lstat()
    require(info.st_uid == owner, "owner")
    require(stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode), "file_type")
    require(info.st_mode & 0o077 == 0, "private_mode")
    if not directory:
        require(info.st_nlink == 1, "hardlink")
    return info

def read_private(path, owner):
    before = secure_path(path, owner)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        after = os.fstat(fd)
        require((before.st_dev, before.st_ino) == (after.st_dev, after.st_ino), "file_changed")
        raw = os.read(fd, MAX_FRAME + 1)
        require(len(raw) <= MAX_FRAME, "file_too_large")
        return raw
    finally:
        os.close(fd)

def atomic(path, value):
    """Publish data only after fsync; retain old ledger on any pre-rename failure."""
    path = pathlib.Path(path)
    temporary = path.with_name(path.name + ".new-" + uuid.uuid4().hex)
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        raw = value if isinstance(value, bytes) else canonical(value)
        with os.fdopen(fd, "wb", closefd=False) as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(fd)
        os.replace(temporary, path)
        parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        os.close(fd)


# Diagnostic evidence is derived state, never admission authority or public wire.
DIAGNOSTIC_LEAVES = {
    "unit-pre": "diagnostic-unit-pre.json",
    "unit-post": "diagnostic-unit-post.json",
    "unknown-transition": "diagnostic-first-unknown.json",
}
MAX_DIAGNOSTIC_BYTES = 1024
DIAGNOSTIC_REASONS = frozenset("""absolute_path arguments argv attempt attestation_failed binding_string bridge_timeout bridge_unknown bytes_required cancel_identity_changed cancel_scope_changed cancel_scope_unknown cancel_unknown cgroup_delegation cgroup_parent command_output consumed_budget consumption_ambiguous disk_threshold duplicate_key effective_cpu effective_file_limit effective_limit entry_denied entry_environment environment environment_keys environment_value file_changed file_too_large file_type frame_changed frame_too_large generation hardlink home_ancestor home_backing home_budget home_device home_mount home_mount_binding hook_cgroup hook_denied hook_sudo_denied identity identity_changed identity_mismatch identity_pin integer invalid_json invocation kernel_binding ledger_ambiguous ledger_attempt ledger_attestation ledger_consumed ledger_current ledger_history ledger_never_entered ledger_phase ledger_schema ledger_terminal lock_permissions lock_replaced loop_backing main_binding main_changed main_executable main_privilege memory_threshold missing_ledger mode model_home_env mount_path no_current nonfinite not_entered not_stageable object_required occupied oom_group operation output_bound output_filesystem owner path peer_changed peer_scope pending_binding pending_cgroup pending_changed pending_denied pending_progress pending_state policy_digest pressure_threshold principal_paths principal_separation private_mode process_credentials process_reused profile profile_binding profile_string prompt prompt_changed release_permissions release_pin release_type reservation_mismatch revoked root_required schema scope_changed scope_inconsistent scope_occupied scope_owner scope_symlink scope_tasks sha256 shared_device_mount shared_ipc snapshot_changed source_sha stage_changed stage_environment_keys start_unknown startup_changed sudo_principal symlink systemd_path terminal_history terminal_unproven threshold tmpfs_budget tmpfs_mount transport_principal unified_cgroup unit_prefix unit_unknown unknown unsafe_owner_mode version work_ancestor work_budget work_changed work_credentials work_device work_mount work_mount_options work_tmpfs""".split())
DIAGNOSTIC_ENUMS = {
    "load_state": ("loaded", "not-found", "error", "masked"),
    "active_state": ("activating", "active", "inactive", "failed", "deactivating"),
    "sub_state": ("start-pre", "start", "start-post", "running", "exited", "dead", "failed", "stop", "stop-post"),
    "result": ("success", "exit-code", "timeout", "signal", "resources", "protocol"),
    "exec_main_code": ("0", "1", "2", "3"),
}
DIAGNOSTIC_BOOLS = ("main_pid_zero", "exec_main_status_zero", "invocation_valid",
                    "unit_id_matches", "control_group_matches", "scope_empty")

def diagnostic_reason(error):
    """Only static refusal literals or fixed exception classes reach evidence."""
    if type(error) is Refusal:
        value = error.args[0] if len(error.args) == 1 else None
        return value if type(value) is str and value in DIAGNOSTIC_REASONS else "refusal_other"
    for kind, code in ((OSError, "os_error"), (KeyError, "key_error"),
                       (ValueError, "value_error"), (TypeError, "type_error"),
                       (subprocess.SubprocessError, "subprocess_error")):
        if isinstance(error, kind):
            return code
    return "unexpected_error"

def diagnostic_native(raw):
    """Revalidate cached evidence; no private names, values or arbitrary strings."""
    if type(raw) is not dict:
        return None
    value = {key: raw.get(key) if type(raw.get(key)) is str and raw[key] in allowed else "other"
             for key, allowed in DIAGNOSTIC_ENUMS.items()}
    value.update({key: raw.get(key) if type(raw.get(key)) is bool else None
                  for key in DIAGNOSTIC_BOOLS})
    observed = raw.get("observed_monotonic_ns")
    value["observed_monotonic_ns"] = observed if type(observed) is int and 0 < observed < 2**64 else None
    return value

def capture_diagnostic(profile, kind, error, phase_before=None, native=None,
                       self_cgroup_match=None):
    """Best-effort first evidence: three fixed private leaves, no overwritten history."""
    try:
        require(kind in DIAGNOSTIC_LEAVES and os.geteuid() == profile.owner, "diagnostic")
        event = dict(schema_version=1, kind=kind, reason=diagnostic_reason(error),
                     pid=os.getpid(), monotonic_ns=time.monotonic_ns())
        if phase_before is not None:
            event.update(phase_before=phase_before if phase_before in ("consumed", "entered", "unknown") else "other",
                         phase_after="unknown")
        snapshot = diagnostic_native(native)
        if snapshot is not None:
            event["native"] = snapshot
        if type(self_cgroup_match) is bool:
            event["self_cgroup_matches"] = self_cgroup_match
        raw = canonical(event) + b"\n"
        require(len(raw) <= MAX_DIAGNOSTIC_BYTES, "diagnostic")
        directory = pathlib.Path(profile.data["state_dir"])
        before = secure_path(directory, profile.owner, True)
        parent = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            current = os.fstat(parent)
            require((current.st_dev, current.st_ino) == (before.st_dev, before.st_ino), "diagnostic")
            fd = os.open(DIAGNOSTIC_LEAVES[kind], os.O_WRONLY | os.O_CREAT | os.O_EXCL |
                         os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=parent)
            try:
                info = os.fstat(fd)
                require(stat.S_ISREG(info.st_mode) and info.st_uid == profile.owner
                        and info.st_nlink == 1 and stat.S_IMODE(info.st_mode) == 0o600, "diagnostic")
                require(os.write(fd, raw) == len(raw), "diagnostic")
                os.fsync(fd)
                os.fsync(parent)
            finally:
                os.close(fd)
        finally:
            os.close(parent)
        return True
    except Exception:
        # Diagnostics must never replace the original admission/refusal outcome.
        # Partial evidence is retained; there is no retry, alternate path or rotation.
        return False

def diagnose(authority, kind, error, phase_before=None):
    """Contain even diagnostic preparation failures after the original state save."""
    try:
        return capture_diagnostic(authority.profile, kind, error, phase_before,
                                  getattr(authority.host, "diagnostic_state", None),
                                  getattr(authority, "diagnostic_cgroup_match", None))
    except Exception:
        return False

class Profile:
    def __init__(self, value, owner=0):
        keys(value, "schema_version profile_id city template binding source_sha manifest_sha256 transport_uid model_uid model_gid unit_prefix cgroup_parent state_dir helper_path helper_sha256 model_argv model_exe_sha256 environment environment_keys resources thresholds disk_path model_home transport_home working_directory home_mount max_consumed_attempts")
        require(type(value["schema_version"]) is int and value["schema_version"] == 1, "version")
        integer(value["max_consumed_attempts"])
        for field in ("profile_id", "city", "template"):
            require(type(value[field]) is str and 0 < len(value[field]) <= 256, "profile_string")
        keys(value["binding"], "selector endpoint resource_domain policy_digest")
        require(all(type(value["binding"][key]) is str and 0 < len(value["binding"][key]) <= 512 for key in ("selector", "endpoint", "resource_domain", "policy_digest")), "binding_string")
        require(HEX64.fullmatch(value["binding"]["policy_digest"]) is not None, "policy_digest")
        require(HEX40.fullmatch(value["source_sha"]) is not None, "source_sha")
        for field in ("manifest_sha256", "helper_sha256", "model_exe_sha256"):
            require(HEX64.fullmatch(value[field]) is not None, "sha256")
        for field in ("transport_uid", "model_uid", "model_gid"):
            integer(value[field])
        require(value["transport_uid"] != value["model_uid"], "principal_separation")
        require(re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", value["unit_prefix"]) is not None, "unit_prefix")
        require(re.fullmatch(r"(?:/[a-z0-9-]+\.slice)+", value["cgroup_parent"]) is not None, "cgroup_parent")
        for field in ("state_dir", "helper_path", "disk_path", "model_home", "transport_home", "working_directory"):
            require(type(value[field]) is str and value[field].startswith("/") and ".." not in pathlib.Path(value[field]).parts, "path")
        argv = value["model_argv"]
        require(type(argv) is list and len(argv) > 0 and argv[0].startswith("/") and all(type(x) is str and "\0" not in x for x in argv), "argv")
        allowed = {"HOME", "PATH", "CODEX_HOME", "LANG", "LC_ALL", "SHELL"}
        require(type(value["environment_keys"]) is list and set(value["environment_keys"]) <= allowed, "environment_keys")
        require(type(value["environment"]) is dict and set(value["environment"]) <= allowed, "environment")
        require(all(type(x) is str and "\0" not in x and "\n" not in x for x in value["environment"].values()), "environment_value")
        keys(value["resources"], "cpu_quota_percent memory_max_bytes memory_swap_max_bytes tasks_max runtime_max_seconds output_max_bytes tmp_max_bytes work_max_bytes")
        for field, number in value["resources"].items():
            integer(number, 0 if field == "memory_swap_max_bytes" else 1)
        keys(value["thresholds"], "min_mem_available_bytes max_memory_psi_full_avg10 max_io_psi_full_avg10 min_disk_available_bytes min_free_inodes")
        for number in value["thresholds"].values():
            require(type(number) in (float, int) and math.isfinite(number) and number >= 0, "threshold")
        require(value["environment"].get("HOME") == value["model_home"], "model_home_env")
        require(set(value["environment_keys"]) <= {"LANG", "LC_ALL"}, "stage_environment_keys")
        paths = [pathlib.Path(value[k]) for k in ("model_home", "transport_home", "working_directory", "state_dir")]
        require(all(a != b and a not in b.parents and b not in a.parents for i, a in enumerate(paths) for b in paths[i+1:]), "principal_paths")
        keys(value["home_mount"], "source device_major device_minor max_bytes backing_file backing_device backing_inode")
        for k in ("device_major", "device_minor", "backing_device"):
            integer(value["home_mount"][k], 0)
        for k in ("max_bytes", "backing_inode"):
            integer(value["home_mount"][k])
        for k in ("source", "backing_file"):
            require(type(value["home_mount"][k]) is str and re.fullmatch(r"/[A-Za-z0-9_./-]+", value["home_mount"][k]) is not None, "mount_path")
        self.data, self.owner = value, owner
        self.digest = hashlib.sha256(canonical(value)).hexdigest()

    def request(self, request):
        keys(request, "city session_id template runtime_handle generation instance_token attempt binding")
        for field in ("session_id", "runtime_handle", "instance_token"):
            require(type(request[field]) is str and 0 < len(request[field]) <= 512 and "\0" not in request[field], "identity")
        require(type(request["generation"]) is str and re.fullmatch(r"0|[1-9][0-9]{0,19}", request["generation"]) is not None, "generation")
        require(type(request["attempt"]) is str and HEX32.fullmatch(request["attempt"]) is not None, "attempt")
        require(request["city"] == self.data["city"] and request["template"] == self.data["template"] and request["binding"] == self.data["binding"], "profile_binding")
        return json.loads(canonical(request))

class Authority:
    def __init__(self, profile, host):
        self.profile, self.host = profile, host
        self.directory = pathlib.Path(profile.data["state_dir"])
        secure_path(self.directory, profile.owner, True)

    @contextlib.contextmanager
    def locked(self):
        path = self.directory / "lock"
        fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        try:
            info = os.fstat(fd)
            require(stat.S_ISREG(info.st_mode) and info.st_uid == self.profile.owner and info.st_nlink == 1 and info.st_mode & 0o077 == 0, "lock_permissions")
            fcntl.flock(fd, fcntl.LOCK_EX)
            current = path.lstat()
            require((info.st_dev, info.st_ino) == (current.st_dev, current.st_ino), "lock_replaced")
            ledger = self.directory / "ledger.json"
            require(ledger.exists(), "missing_ledger")
            state = decode(read_private(ledger, self.profile.owner))
            keys(state, "schema_version current attempts")
            require(type(state["schema_version"]) is int and state["schema_version"] == 1 and type(state["attempts"]) is dict, "ledger_schema")
            require(state["current"] is None or state["current"] in state["attempts"], "ledger_current")
            for attempt, row in state["attempts"].items():
                require(type(row) is dict and row.get("request", {}).get("attempt") == attempt and HEX32.fullmatch(attempt) is not None, "ledger_attempt")
                require(row.get("phase") in ("prepared", "staged", "consumed", "entered", "exited", "unknown") and type(row.get("ever_consumed")) is bool and type(row.get("revoked")) is bool, "ledger_phase")
                require(type(row.get("attested")) is bool and type(row.get("never_entered")) is bool, "ledger_history")
                if "startup_identity" in row:
                    keys(row["startup_identity"], "boot_id unit invocation_id cgroup_path cgroup_device cgroup_inode source_sha manifest_sha256 model_uid transport_uid")
                if row["phase"] in ("prepared", "staged"):
                    require(not row["ever_consumed"] and not row["attested"] and not row["never_entered"], "ledger_consumed")
                elif row["phase"] == "exited":
                    if row["ever_consumed"]:
                        require(row["attested"] and not row["never_entered"], "ledger_terminal")
                    else:
                        require(not row["attested"] and row["never_entered"] and row["revoked"], "ledger_never_entered")
                else:
                    require(row["ever_consumed"] and not row["never_entered"], "ledger_consumed")
                    if row["phase"] == "entered":
                        require(row["attested"], "ledger_attestation")
                    if row["phase"] == "consumed":
                        require(not row["attested"], "ledger_attestation")
            occupied = [attempt for attempt, row in state["attempts"].items() if row.get("phase") != "exited"]
            require(occupied == ([state["current"]] if state["current"] is not None else []), "ledger_ambiguous")
            yield state
        finally:
            os.close(fd)

    def save(self, state, row):
        row["revision"] += 1
        atomic(self.directory / "ledger.json", state)

    def receipt(self, row):
        phase = row["phase"]
        public = "prepared" if phase in ("prepared", "staged") else phase if phase in ("entered", "exited") else "unknown"
        return {"request": row["request"], "authority_receipt_id": row["receipt_id"], "journal_revision": row["revision"], "phase": public, "identity": row.get("identity", {})}

    def exact(self, state, request):
        request = self.profile.request(request)
        row = state["attempts"].get(request["attempt"])
        require(row is not None and row["request"] == request and row["profile_digest"] == self.profile.digest, "reservation_mismatch")
        return row

    def require_budget(self, state):
        # Retained consumed history is lifetime authority, independent of current seat.
        consumed = sum(row["ever_consumed"] is True for row in state["attempts"].values())
        require(consumed < self.profile.data["max_consumed_attempts"], "consumed_budget")

    def prepare(self, request):
        request = self.profile.request(request)
        with self.locked() as state:
            if request["attempt"] in state["attempts"]:
                return self.receipt(self.exact(state, request))
            require(state["current"] is None, "occupied")
            self.require_budget(state)
            self.host.resources()
            scope = self.host.scope()
            require(scope["empty"] is True, "scope_occupied")
            row = {"request": request, "receipt_id": uuid.uuid4().hex, "revision": 0, "phase": "prepared", "profile_digest": self.profile.digest, "scope": scope, "revoked": False, "identity": {"boot_id": scope["boot_id"], "unit": "", "invocation_id": "", "cgroup_path": "", "cgroup_device": 0, "cgroup_inode": 0, **{key: self.profile.data[key] for key in ("source_sha", "manifest_sha256", "model_uid", "transport_uid")}}, "attested": False, "ever_consumed": False, "never_entered": False}
            state["attempts"][request["attempt"]] = row
            state["current"] = request["attempt"]
            self.save(state, row)
            return self.receipt(row)

    def stage_prompt(self, request, prompt, environment):
        require(type(prompt) is str and len(prompt.encode()) <= MAX_FRAME // 2 and "\0" not in prompt, "prompt")
        require(type(environment) is dict and set(environment) <= set(self.profile.data["environment_keys"]), "environment")
        require(all(type(v) is str and "\0" not in v and "\n" not in v for v in environment.values()), "environment_value")
        digest = hashlib.sha256(canonical({"prompt": prompt, "environment": environment})).hexdigest()
        with self.locked() as state:
            row = self.exact(state, request)
            require(not row["revoked"] and row["phase"] in ("prepared", "staged"), "not_stageable")
            if row["phase"] == "staged":
                require(row["frame_sha"] == digest, "stage_changed")
                return self.receipt(row)
            atomic(self.directory / (request["attempt"] + ".prompt"), prompt.encode())
            row.update(phase="staged", frame_sha=digest, prompt_sha=hashlib.sha256(prompt.encode()).hexdigest(), environment=environment)
            self.save(state, row)
            return self.receipt(row)

    def enter(self, attempt, caller_uid):
        require(caller_uid == self.profile.data["transport_uid"], "transport_principal")
        with self.locked() as state:
            row = state["attempts"].get(attempt)
            require(row is not None and state["current"] == attempt and row["profile_digest"] == self.profile.digest and not row["revoked"] and row["phase"] == "staged", "entry_denied")
            raw = read_private(self.directory / (attempt + ".prompt"), self.profile.owner)
            require(hashlib.sha256(raw).hexdigest() == row["prompt_sha"], "prompt_changed")
            environment = row["environment"]
            require(type(environment) is dict and set(environment) <= set(self.profile.data["environment_keys"]) and all(type(value) is str and "\0" not in value and "\n" not in value for value in environment.values()), "entry_environment")
            require(hashlib.sha256(canonical({"prompt": raw.decode("utf-8", "strict"), "environment": environment})).hexdigest() == row["frame_sha"], "frame_changed")
            self.exact(state, row["request"])
            self.require_budget(state)
            self.host.resources()
            require(self.host.scope() == row["scope"], "scope_changed")
            row["phase"] = "consumed"
            row["ever_consumed"] = True
            self.save(state, row)
            try:
                self.host.start(row)
            except Exception as error:
                row["phase"] = "unknown"
                self.save(state, row)
                diagnose(self, "unknown-transition", error, "consumed")
                raise Refusal("start_unknown") from None
            return self.receipt(row)

    def refresh(self, state, row):
        if row["phase"] in ("prepared", "staged", "exited"):
            return
        previous_phase, failure = row["phase"], None
        try:
            proof = self.host.inspect(row)
            require(proof["scope"]["boot_id"] == row["scope"]["boot_id"] and proof["scope"]["device"] == row["scope"]["device"] and proof["scope"]["inode"] == row["scope"]["inode"], "scope_changed")
            if proof["phase"] == "pending":
                # Public unknown; only preserve the consumed seat for its own hooks.
                require(row["phase"] == "consumed" and row["ever_consumed"] and not row["attested"]
                        and not row["revoked"] and state["current"] == row["request"]["attempt"], "pending_denied")
                require(proof["scope"]["empty"] is False, "scope_inconsistent")
                self.host.validate_identity(row, proof["identity"])
                row["startup_identity"] = proof["identity"]
            elif proof["phase"] == "entered":
                require(proof["scope"]["empty"] is False, "scope_inconsistent")
                require(not row["revoked"], "revoked")
                identity = proof["identity"]
                self.host.validate_identity(row, identity)
                require(not row.get("attested") or row["identity"] == identity, "identity_changed")
                row.update(phase="entered", identity=identity, attested=True)
            elif proof["phase"] == "exited":
                require(row.get("attested") and proof["identity"] == row["identity"] and proof["scope"]["empty"] is True, "terminal_unproven")
                row["phase"] = "exited"
                row["empty_scope"] = proof["scope"]
                state["current"] = None
            else:
                raise Refusal("unknown")
        except (Refusal, OSError, KeyError, ValueError, subprocess.SubprocessError) as error:
            failure = error
            row["phase"] = "unknown"
        self.save(state, row)
        if failure is not None and previous_phase != "unknown":
            diagnose(self, "unknown-transition", failure, previous_phase)

    def observe(self, request):
        with self.locked() as state:
            row = self.exact(state, request)
            self.refresh(state, row)
            return self.receipt(row)

    def cancel(self, request):
        with self.locked() as state:
            row = self.exact(state, request)
            previous = row["phase"]
            row["revoked"] = True
            self.save(state, row)
            if previous in ("prepared", "staged"):
                scope = self.host.scope()
                require(scope == row["scope"] and scope["empty"] is True, "cancel_scope_unknown")
                require(row["ever_consumed"] is False, "consumption_ambiguous")
                row["empty_scope"] = scope
                row["never_entered"] = True
                row["phase"] = "exited"
                state["current"] = None
                self.save(state, row)
            elif previous != "exited":
                try:
                    proof = self.host.inspect(row)
                    require(all(proof["scope"][key] == row["scope"][key] for key in ("boot_id", "device", "inode")), "cancel_scope_changed")
                    if proof["phase"] == "entered":
                        self.host.validate_identity(row, proof["identity"])
                        require(not row.get("attested") or row["identity"] == proof["identity"], "cancel_identity_changed")
                        # Capture a raced start for cancellation without granting entered authority.
                        row.update(identity=proof["identity"], attested=True, phase="unknown")
                        self.save(state, row)
                        self.host.cancel(row)
                except (Refusal, OSError, KeyError, ValueError, subprocess.SubprocessError):
                    row["phase"] = "unknown"
                    self.save(state, row)
                self.refresh(state, row)
            return self.receipt(row)

    def queue_observe(self, frame):
        require(set(frame) in ({"schema_version", "profile_id", "peer"}, {"schema_version", "profile_id", "peer", "expected_identity"}), "schema")
        require(type(frame["schema_version"]) is int and frame["schema_version"] == 1 and frame["profile_id"] == self.profile.data["profile_id"], "profile")
        with self.locked() as state:
            require(state["current"] is not None, "no_current")
            row = state["attempts"][state["current"]]
            self.exact(state, row["request"])
            self.refresh(state, row)
            require(row["phase"] == "entered" and not row["revoked"], "not_entered")
            req = row["request"]
            identity = {"sid": req["session_id"], **{k: req[k] for k in ("template", "runtime_handle", "generation", "instance_token", "attempt")}}
            if "expected_identity" in frame:
                require(frame["expected_identity"] == identity, "identity_mismatch")
            peer = frame["peer"]
            keys(peer, "pid uid gid start cgroup")
            require(self.host.peer(peer["pid"]) == peer, "peer_changed")
            require(peer["uid"] == self.profile.data["model_uid"] and peer["gid"] == self.profile.data["model_gid"] and peer["cgroup"] == row["identity"]["cgroup_path"], "peer_scope")
            ident = row["identity"]
            return {"schema_version": 1, "profile_id": frame["profile_id"], "identity": identity, "observe": {"receipt_id": row["receipt_id"], "revision": str(row["revision"]), "boot_id": ident["boot_id"], "unit": ident["unit"], "cgroup": ident["cgroup_path"], "invocation_id": ident["invocation_id"], "model_uid": ident["model_uid"], "pid": peer["pid"], "start": peer["start"], "phase": "entered", "source_sha": ident["source_sha"], "manifest_sha": ident["manifest_sha256"], "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")}}

class LinuxHost:
    """Fixed systemd adapter; tests inject a fake and never execute this as root."""
    def __init__(self, profile):
        self.profile = profile
        self.p = profile.data

    def command(self, argv):
        result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.DEVNULL, timeout=15, check=True,
                                env={"PATH": "/usr/bin:/bin", "LANG": "C"})
        require(len(result.stdout) <= MAX_FRAME, "command_output")
        return result.stdout.decode("utf-8", "strict").strip()

    def boot(self):
        value = pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        return uuid.UUID(value).hex

    def home_mount(self):
        home = pathlib.Path(self.p["model_home"])
        for path, owner in ((home, self.p["model_uid"]), (pathlib.Path(self.p["transport_home"]), self.p["transport_uid"])):
            secure_path(path, owner, True)
            # All ancestors must be root owned: the two principals share no mutable parents.
            require(all(p.lstat().st_uid == 0 for p in path.parents), "home_ancestor")
        mount = self.p["home_mount"]
        info = home.stat()
        require((os.major(info.st_dev), os.minor(info.st_dev)) == (mount["device_major"], mount["device_minor"]), "home_device")
        matches = []
        for line in pathlib.Path("/proc/self/mountinfo").read_text().splitlines():
            left, right = line.split(" - ", 1)
            columns, extra = left.split(), right.split()
            if columns[4] == str(home):
                matches.append((columns, extra))
        require(len(matches) == 1, "home_mount")
        columns, extra = matches[0]
        require(columns[2] == str(mount["device_major"]) + ":" + str(mount["device_minor"]) and columns[3] == "/" and extra[0] == "ext4" and extra[1] == mount["source"] and "rw" in columns[5].split(","), "home_mount_binding")
        backing = pathlib.Path(mount["backing_file"])
        info = secure_path(backing, 0)
        require((info.st_dev, info.st_ino) == (mount["backing_device"], mount["backing_inode"]) and info.st_size <= mount["max_bytes"] and info.st_blocks * 512 >= info.st_size, "home_backing")
        kernel_backing = pathlib.Path("/sys/dev/block/" + columns[2] + "/loop/backing_file").read_text().strip()
        require("/" + kernel_backing.lstrip("/") == str(backing), "loop_backing")
        filesystem = os.statvfs(home)
        require(filesystem.f_blocks * filesystem.f_frsize <= mount["max_bytes"], "home_budget")

    def resources(self):
        self.check_resources(False)

    def unit_resources(self):
        # Only unit_hook calls this after validating the root ledger and exact cgroup.
        # A '+' privileged hook still sees the unit's model-owned tmpfs overlay.
        self.check_resources(True)

    def unit_workspace(self):
        work = pathlib.Path(self.p["working_directory"])
        before = secure_path(work, self.p["model_uid"], True)
        require(all(parent.lstat().st_uid == 0 for parent in work.parents), "work_ancestor")
        require((before.st_gid, stat.S_IMODE(before.st_mode)) == (self.p["model_gid"], 0o700), "work_credentials")

        def mount():
            entries = []
            for line in pathlib.Path("/proc/self/mountinfo").read_text().splitlines():
                left, right = line.split(" - ", 1)
                fields = left.split()
                if fields[4] == str(work):
                    entries.append((fields, right.split()))
            require(len(entries) == 1, "work_mount")
            fields, filesystem = entries[0]
            require(fields[3] == "/" and filesystem[0] == "tmpfs", "work_tmpfs")
            require({"rw", "nosuid", "nodev"} <= set(fields[5].split(",")), "work_mount_options")
            require(fields[2] == str(os.major(before.st_dev)) + ":" + str(os.minor(before.st_dev)), "work_device")
            return entries[0]

        mounted = mount()
        fd = os.open(work, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            opened = os.fstat(fd)
            require((opened.st_dev, opened.st_ino, opened.st_uid, opened.st_gid, opened.st_mode) ==
                    (before.st_dev, before.st_ino, before.st_uid, before.st_gid, before.st_mode), "work_changed")
            space = os.fstatvfs(fd)
            require(space.f_blocks * space.f_frsize <= self.p["resources"]["work_max_bytes"], "work_budget")
            after = work.lstat()
            require((after.st_dev, after.st_ino, after.st_uid, after.st_gid, after.st_mode) ==
                    (before.st_dev, before.st_ino, before.st_uid, before.st_gid, before.st_mode)
                    and mount() == mounted, "work_changed")
        finally:
            os.close(fd)

    def check_resources(self, in_unit):
        self.home_mount()
        if in_unit:
            self.unit_workspace()
        else:
            # Installer provisions this root-only host target. Never repair/chown it.
            work = pathlib.Path(self.p["working_directory"])
            secure_path(work, 0, True)
            require(all(parent.lstat().st_uid == 0 for parent in work.parents), "work_ancestor")
        values = {}
        for line in pathlib.Path("/proc/meminfo").read_text().splitlines():
            name, rest = line.split(":", 1)
            values[name] = int(rest.split()[0]) * 1024
        thresholds = self.p["thresholds"]
        require(values["MemAvailable"] >= thresholds["min_mem_available_bytes"], "memory_threshold")
        for resource in ("memory", "io"):
            lines = pathlib.Path("/proc/pressure/" + resource).read_text().splitlines()
            full = next(line for line in lines if line.startswith("full "))
            avg = float(dict(item.split("=") for item in full.split()[1:])["avg10"])
            require(avg <= thresholds["max_" + resource + "_psi_full_avg10"], "pressure_threshold")
        require(os.stat(self.p["disk_path"]).st_dev == os.stat(self.p["state_dir"]).st_dev, "output_filesystem")
        disk = os.statvfs(self.p["disk_path"])
        require(disk.f_bavail * disk.f_frsize >= thresholds["min_disk_available_bytes"] + self.p["resources"]["output_max_bytes"] and disk.f_favail >= thresholds["min_free_inodes"], "disk_threshold")

    def scope(self):
        path = pathlib.Path("/sys/fs/cgroup" + self.p["cgroup_parent"])
        info = path.lstat()
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0, "scope_owner")
        require(not stat.S_ISLNK(info.st_mode), "scope_symlink")
        events = dict(line.split() for line in (path / "cgroup.events").read_text().splitlines())
        empty = events.get("populated") == "0"
        # cgroup.events populated is recursive; independently reject any visible tasks.
        if empty:
            for directory, dirs, files in os.walk(path, followlinks=False):
                require(all(not pathlib.Path(directory, name).is_symlink() for name in dirs), "scope_symlink")
                require(not pathlib.Path(directory, "cgroup.procs").read_text().strip(), "scope_tasks")
        return {"boot_id": self.boot(), "device": info.st_dev, "inode": info.st_ino, "empty": empty}

    def unit(self, row):
        return self.p["unit_prefix"] + "-" + row["request"]["attempt"] + ".service"

    def cgroup(self, row):
        return self.p["cgroup_parent"] + "/" + self.unit(row)

    def pin(self, path, expected):
        path = pathlib.Path(path)
        for ancestor in [path, *path.parents]:
            info = ancestor.lstat()
            require(not stat.S_ISLNK(info.st_mode) and info.st_uid == 0 and info.st_mode & 0o022 == 0, "release_permissions")
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            info = os.fstat(fd)
            require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1, "release_type")
            digest = hashlib.sha256()
            while True:
                raw = os.read(fd, 1048576)
                if not raw:
                    break
                digest.update(raw)
            require(digest.hexdigest() == expected, "release_pin")
            return info
        finally:
            os.close(fd)

    def start(self, row):
        self.pin(self.p["helper_path"], self.p["helper_sha256"])
        self.pin(self.p["model_argv"][0], self.p["model_exe_sha256"])
        attempt = row["request"]["attempt"]
        # systemd property values have their own grammar; root-owned paths must be simple.
        for value in (self.p["state_dir"], self.p["helper_path"], self.p["model_home"], self.p["working_directory"]):
            require(re.fullmatch(r"/[A-Za-z0-9_./-]+", value) is not None, "systemd_path")
        prompt = self.p["state_dir"] + "/" + attempt + ".prompt"
        output = self.p["state_dir"] + "/" + attempt + ".output"
        fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        os.close(fd)
        r = self.p["resources"]
        properties = {
            "User": str(self.p["model_uid"]), "Group": str(self.p["model_gid"]),
            "Slice": self.p["cgroup_parent"].rsplit("/", 1)[1],
            "Restart": "no", "RemainAfterExit": "yes", "KillMode": "control-group", "OOMPolicy": "kill",
            "NoNewPrivileges": "yes", "ProtectSystem": "strict", "ProtectHome": "yes",
            "ProtectControlGroups": "yes", "Delegate": "no", "PrivateTmp": "no",
            "PrivateDevices": "yes", "PrivateIPC": "yes",
            "ReadWritePaths": self.p["state_dir"] + " " + self.p["model_home"],
            "WorkingDirectory": self.p["working_directory"],
            "TemporaryFileSystem": "/dev/shm:rw,nosuid,nodev,noexec,size=" + str(r["tmp_max_bytes"]) + ",mode=1777 /tmp:rw,nosuid,nodev,size=" + str(r["tmp_max_bytes"]) + ",mode=1777 /var/tmp:rw,nosuid,nodev,size=" + str(r["tmp_max_bytes"]) + ",mode=1777 " + self.p["working_directory"] + ":rw,nosuid,nodev,size=" + str(r["work_max_bytes"]) + ",mode=0700,uid=" + str(self.p["model_uid"]) + ",gid=" + str(self.p["model_gid"]),
            "LimitFSIZE": str(r["output_max_bytes"]),
            "RestrictSUIDSGID": "yes", "CapabilityBoundingSet": "", "AmbientCapabilities": "",
            "MemoryMax": str(r["memory_max_bytes"]), "MemorySwapMax": str(r["memory_swap_max_bytes"]),
            "CPUQuota": str(r["cpu_quota_percent"]) + "%", "TasksMax": str(r["tasks_max"]),
            "RuntimeMaxSec": str(r["runtime_max_seconds"]),
            "StandardInput": "file:" + prompt, "StandardOutput": "append:" + output,
            "StandardError": "append:" + output,
            "ExecStartPre": "+/usr/bin/python3 -I -B " + self.p["helper_path"] + " unit-pre " + attempt,
            "ExecStartPost": "+/usr/bin/python3 -I -B " + self.p["helper_path"] + " unit-post " + attempt,
        }
        argv = ["/usr/bin/systemd-run", "--quiet", "--no-block", "--service-type=exec", "--expand-environment=no", "--unit=" + self.unit(row)]
        for name, value in properties.items():
            argv += ["--property=" + name + "=" + value]
        environment = {**self.p["environment"], **row["environment"]}
        for name, value in environment.items():
            argv += ["--setenv=" + name + "=" + value]
        argv += ["--", *self.p["model_argv"]]
        self.command(argv)

    def show(self, unit):
        fields = ("Id", "LoadState", "ActiveState", "SubState", "MainPID", "InvocationID", "ControlGroup", "ExecMainCode", "ExecMainStatus", "Result")
        raw = self.command(["/usr/bin/systemctl", "show", unit, "--no-pager", "--property=" + ",".join(fields)])
        result = dict(line.split("=", 1) for line in raw.splitlines())
        try:
            # Cache only this already-read kernel view; never issue a diagnostic query.
            self.diagnostic_state = diagnostic_native(dict(
                load_state=result.get("LoadState"), active_state=result.get("ActiveState"),
                sub_state=result.get("SubState"), result=result.get("Result"),
                exec_main_code=result.get("ExecMainCode"), main_pid_zero=result.get("MainPID") == "0",
                exec_main_status_zero=result.get("ExecMainStatus") == "0",
                invocation_valid=type(result.get("InvocationID")) is str and HEX32.fullmatch(result["InvocationID"]) is not None,
                unit_id_matches=result.get("Id") == unit,
                control_group_matches=result.get("ControlGroup") == self.p["cgroup_parent"] + "/" + unit,
                scope_empty=getattr(self, "diagnostic_scope_empty", None),
                observed_monotonic_ns=time.monotonic_ns()))
        except Exception:
            self.diagnostic_state = None
        require(set(result) == set(fields) and result["Id"] == unit and result["LoadState"] == "loaded", "unit_unknown")
        return result

    def peer(self, pid):
        integer(pid)
        base = pathlib.Path("/proc") / str(pid)
        before = (base / "stat").read_text()
        start = before.rsplit(")", 1)[1].split()[19]
        status = dict(line.split(":", 1) for line in (base / "status").read_text().splitlines() if ":" in line)
        uids, gids = status["Uid"].split(), status["Gid"].split()
        require(len(set(uids)) == 1 and len(set(gids)) == 1, "process_credentials")
        cgroups = (base / "cgroup").read_text().strip().splitlines()
        require(len(cgroups) == 1 and cgroups[0].startswith("0::/"), "unified_cgroup")
        after = (base / "stat").read_text().rsplit(")", 1)[1].split()[19]
        require(start == after, "process_reused")
        return {"pid": pid, "uid": int(uids[0]), "gid": int(gids[0]), "start": start, "cgroup": cgroups[0][3:]}

    def effective_tmpfs(self, pid):
        mounts = []
        for line in pathlib.Path("/proc/" + str(pid) + "/mountinfo").read_text().splitlines():
            left, right = line.split(" - ", 1)
            mounts.append((left.split()[4], right.split()[0]))
        for name, budget, mode, uid, gid in (
            ("/tmp", self.p["resources"]["tmp_max_bytes"], 0o1777, 0, 0),
            ("/var/tmp", self.p["resources"]["tmp_max_bytes"], 0o1777, 0, 0),
            ("/dev/shm", self.p["resources"]["tmp_max_bytes"], 0o1777, 0, 0),
            (self.p["working_directory"], self.p["resources"]["work_max_bytes"], 0o700, self.p["model_uid"], self.p["model_gid"]),
        ):
            require([fstype for mount, fstype in mounts if mount == name] == ["tmpfs"], "tmpfs_mount")
            path = pathlib.Path("/proc/" + str(pid) + "/root" + name)
            info, space = path.stat(), os.statvfs(path)
            require((info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)) == (uid, gid, mode) and space.f_blocks * space.f_frsize <= budget, "tmpfs_budget")

        self.private_namespaces(pid)

    def private_namespaces(self, pid):
        # PID1 is the host reference even when a privileged hook shares some unit namespaces.
        ipc = os.stat("/proc/" + str(pid) + "/ns/ipc")
        host_ipc = os.stat("/proc/1/ns/ipc")
        require((ipc.st_dev, ipc.st_ino) != (host_ipc.st_dev, host_ipc.st_ino), "shared_ipc")
        for name in ("/dev", "/dev/shm"):
            model = os.stat("/proc/" + str(pid) + "/root" + name)
            host = os.stat("/proc/1/root" + name)
            require(model.st_dev != host.st_dev, "shared_device_mount")

    def validate_identity(self, row, identity):
        keys(identity, "boot_id unit invocation_id cgroup_path cgroup_device cgroup_inode source_sha manifest_sha256 model_uid transport_uid")
        require(identity["boot_id"] == self.boot() and identity["unit"] == self.unit(row) and identity["cgroup_path"] == self.cgroup(row), "kernel_binding")
        require(re.fullmatch(r"[0-9a-f]{32}", identity["invocation_id"]) is not None, "invocation")
        for field in ("source_sha", "manifest_sha256", "model_uid", "transport_uid"):
            require(identity[field] == self.p[field], "identity_pin")
        integer(identity["cgroup_inode"])
        integer(identity["cgroup_device"], 0)
        require("startup_identity" not in row or row["startup_identity"] == identity, "startup_changed")

    def pending(self, row, unit, scope):
        require(row["phase"] == "consumed" and row["ever_consumed"] and not row["attested"]
                and not row["revoked"], "pending_denied")
        require(unit["Id"] == self.unit(row) and unit["LoadState"] == "loaded"
                and unit["ActiveState"] == "activating" and unit["SubState"] == "start-pre"
                and unit["MainPID"] == "0" and unit["ExecMainCode"] == "0"
                and unit["ExecMainStatus"] == "0" and unit["Result"] == "success", "pending_state")
        require(HEX32.fullmatch(unit["InvocationID"]) is not None
                and unit["ControlGroup"] == self.cgroup(row) and scope["empty"] is False, "pending_binding")
        path = pathlib.Path("/sys/fs/cgroup" + self.cgroup(row))
        before = path.stat()
        require(stat.S_ISDIR(before.st_mode) and before.st_uid == 0 and before.st_mode & 0o022 == 0
                and before.st_ino > 0, "pending_cgroup")
        second, after, final_scope = self.show(self.unit(row)), path.stat(), self.scope()
        # Compare immutable identity, allowing only explicit forward startup states.
        require(all(second[k] == unit[k] for k in ("Id", "LoadState", "InvocationID", "ControlGroup")),
                "pending_changed")
        require((second["ActiveState"], second["SubState"]) in (
                    ("activating", "start-pre"), ("activating", "start"),
                    ("activating", "start-post"), ("active", "running"))
                and second["ExecMainCode"] == "0" and second["ExecMainStatus"] == "0"
                and second["Result"] == "success"
                and re.fullmatch(r"0|[1-9][0-9]*", second["MainPID"]) is not None
                and (second["SubState"] not in ("start-post", "running") or second["MainPID"] != "0"),
                "pending_progress")
        require((before.st_dev, before.st_ino, before.st_uid, before.st_mode) ==
                (after.st_dev, after.st_ino, after.st_uid, after.st_mode)
                and all(final_scope[k] == scope[k] for k in ("boot_id", "device", "inode"))
                and final_scope["empty"] is False, "pending_changed")
        identity = {"boot_id": scope["boot_id"], "unit": self.unit(row),
                    "invocation_id": unit["InvocationID"], "cgroup_path": self.cgroup(row),
                    "cgroup_device": before.st_dev, "cgroup_inode": before.st_ino,
                    **{k: self.p[k] for k in ("source_sha", "manifest_sha256", "model_uid", "transport_uid")}}
        self.validate_identity(row, identity)
        return {"phase": "pending", "identity": identity, "scope": scope}

    def inspect(self, row):
        self.diagnostic_state = None
        self.diagnostic_scope_empty = None
        scope = self.scope()
        self.diagnostic_scope_empty = scope["empty"]
        unit = self.show(self.unit(row))
        if unit["MainPID"] != "0":
            pid = int(unit["MainPID"])
            peer = self.peer(pid)
            require(peer["uid"] == self.p["model_uid"] and peer["gid"] == self.p["model_gid"] and peer["cgroup"] == self.cgroup(row) and unit["ControlGroup"] == peer["cgroup"], "main_binding")
            exe = self.pin(self.p["model_argv"][0], self.p["model_exe_sha256"])
            running = os.stat("/proc/" + str(pid) + "/exe")
            status = pathlib.Path("/proc/" + str(pid) + "/status").read_text()
            require("NoNewPrivs:\t1" in status and "CapEff:\t0000000000000000" in status, "main_privilege")
            require((exe.st_dev, exe.st_ino) == (running.st_dev, running.st_ino), "main_executable")
            process = {**peer, "exe_device": running.st_dev, "exe_inode": running.st_ino}
            require(not row.get("process") or row["process"] == process, "main_changed")
            row["process"] = process
            cgpath = pathlib.Path("/sys/fs/cgroup" + peer["cgroup"])
            cg = cgpath.stat()
            require(cg.st_uid == 0 and cg.st_mode & 0o022 == 0, "cgroup_delegation")
            for filename, field in (("memory.max", "memory_max_bytes"), ("memory.swap.max", "memory_swap_max_bytes"), ("pids.max", "tasks_max")):
                require(int((cgpath / filename).read_text()) == self.p["resources"][field], "effective_limit")
            quota, period = (cgpath / "cpu.max").read_text().split()
            require(int(quota) * 100 <= int(period) * self.p["resources"]["cpu_quota_percent"], "effective_cpu")
            self.effective_tmpfs(pid)
            require((cgpath / "memory.oom.group").read_text().strip() == "1", "oom_group")
            limits = pathlib.Path("/proc/" + str(pid) + "/limits").read_text().splitlines()
            filelimit = next(line for line in limits if line.startswith("Max file size"))
            require(int(filelimit[len("Max file size"):].split()[0]) <= self.p["resources"]["output_max_bytes"], "effective_file_limit")
            identity = {"boot_id": self.boot(), "unit": self.unit(row), "invocation_id": unit["InvocationID"], "cgroup_path": peer["cgroup"], "cgroup_device": cg.st_dev, "cgroup_inode": cg.st_ino, **{k: self.p[k] for k in ("source_sha", "manifest_sha256", "model_uid", "transport_uid")}}
            # Re-read after all kernel reads to reject concurrent exit/PID turnover.
            require(self.peer(pid) == peer and self.show(self.unit(row))["InvocationID"] == identity["invocation_id"], "snapshot_changed")
            return {"phase": "entered", "identity": identity, "scope": scope}
        if row["phase"] == "consumed" and not row["attested"] and not row["revoked"]:
            return self.pending(row, unit, scope)
        require(row.get("attested") and unit["InvocationID"] == row["identity"]["invocation_id"] and unit["ExecMainCode"] in ("1", "2", "3") and unit["ActiveState"] in ("active", "inactive", "failed") and unit["SubState"] in ("exited", "dead", "failed"), "terminal_history")
        return {"phase": "exited", "identity": row["identity"], "scope": scope}

    def bridge(self, authority, request):
        path = pathlib.Path(self.p["state_dir"]) / (request["attempt"] + ".output")
        secure_path(path, self.profile.owner)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
        started = time.monotonic()
        deadline = started + self.p["resources"]["runtime_max_seconds"] + 30
        try:
            while time.monotonic() < deadline:
                raw = os.read(fd, 65536)
                if raw:
                    sys.stdout.buffer.write(raw)
                    sys.stdout.buffer.flush()
                    continue
                receipt = authority.observe(request)
                if receipt["phase"] == "exited":
                    # Exit proof includes recursive empty: every writer is gone.
                    # Drain again after that proof to close the EOF/exit race.
                    remaining = self.p["resources"]["output_max_bytes"]
                    while remaining > 0:
                        final = os.read(fd, min(65536, remaining))
                        if not final:
                            break
                        sys.stdout.buffer.write(final)
                        sys.stdout.buffer.flush()
                        remaining -= len(final)
                    require(not os.read(fd, 1), "output_bound")
                    return
                # A consumed service can be pending its post-start attestation briefly.
                # Unknown always retains the authority seat; bridge loss never cancels it.
                if receipt["phase"] == "unknown" and time.monotonic() - started >= 15:
                    raise Refusal("bridge_unknown")
                time.sleep(0.2)
            raise Refusal("bridge_timeout")
        finally:
            os.close(fd)

    def cancel(self, row):
        # Check exact invocation before a unit-directed signal; never stop an unknown replacement.
        unit = self.show(self.unit(row))
        require(row.get("attested") and unit["InvocationID"] == row["identity"]["invocation_id"], "cancel_unknown")
        self.command(["/usr/bin/systemctl", "kill", "--kill-whom=all", "--signal=KILL", self.unit(row)])


def unit_hook(authority, attempt, post):
    with authority.locked() as state:
        row = state["attempts"].get(attempt)
        require(row is not None and state["current"] == attempt and row["profile_digest"] == authority.profile.digest and row["phase"] in ("consumed", "entered") and not row["revoked"], "hook_denied")
        authority.diagnostic_cgroup_match = authority.host.peer(os.getpid())["cgroup"] == authority.host.cgroup(row)
        require(authority.diagnostic_cgroup_match, "hook_cgroup")
        if "startup_identity" in row:
            proof = authority.host.inspect(row)
            require(proof["phase"] in ("pending", "entered")
                    and proof["identity"] == row["startup_identity"], "startup_changed")
        authority.host.unit_resources()
        scope = authority.host.scope()
        require(all(scope[k] == row["scope"][k] for k in ("boot_id", "device", "inode")), "scope_changed")
        if post:
            authority.refresh(state, row)
            require(row["phase"] == "entered", "attestation_failed")


def run(argv, raw):
    require(os.geteuid() == 0, "root_required")
    profile = Profile(decode(read_private(PROFILE, 0)))
    authority = Authority(profile, LinuxHost(profile))
    return dispatch_command(authority, argv, raw)

def dispatch_command(authority, argv, raw):
    """Keep hook diagnostics behind a validated profile and private state boundary."""
    try:
        return run_command(authority, argv, raw)
    except (Refusal, OSError, KeyError, ValueError, TypeError, subprocess.SubprocessError) as error:
        if argv and type(argv[0]) is str and argv[0] in ("unit-pre", "unit-post"):
            diagnose(authority, argv[0], error)
        raise

def run_command(authority, argv, raw):
    require(len(argv) in (1, 2), "arguments")
    mode = argv[0]
    if mode in ("enter", "unit-pre", "unit-post"):
        require(len(argv) == 2 and HEX32.fullmatch(argv[1]) is not None and not raw, "arguments")
        if mode == "enter":
            caller = os.environ.get("SUDO_UID", "")
            require(re.fullmatch(r"[1-9][0-9]*", caller) is not None, "sudo_principal")
            receipt = authority.enter(argv[1], int(caller))
            authority.host.bridge(authority, receipt["request"])
            return None
        require("SUDO_UID" not in os.environ, "hook_sudo_denied")
        unit_hook(authority, argv[1], mode == "unit-post")
        return {"schema_version": 1, "ok": True}
    require(len(argv) == 1, "arguments")
    frame = decode(raw)
    if mode == "queue-observe":
        return authority.queue_observe(frame)
    require(mode == "rpc", "mode")
    return dispatch(authority, frame)


def dispatch(authority, frame):
    """Dispatch only a trusted authority object; CLI always authenticates root first."""
    op = frame.get("op")
    expected = "schema_version op request prompt environment" if op == "stage_prompt" else "schema_version op request"
    keys(frame, expected)
    require(type(frame["schema_version"]) is int and frame["schema_version"] == 1, "version")
    require(op in ("prepare", "stage_prompt", "observe", "cancel"), "operation")
    if op == "stage_prompt":
        receipt = authority.stage_prompt(frame["request"], frame["prompt"], frame["environment"])
    else:
        receipt = getattr(authority, op)(frame["request"])
    with authority.locked() as state:
        row = authority.exact(state, frame["request"])
        scope = row.get("empty_scope", row["scope"])
        empty = row["phase"] == "exited" and "empty_scope" in row and authority.host.scope() == scope and state["current"] is None
        never = empty and row.get("never_entered") is True and row.get("ever_consumed") is False and row["revoked"] is True
    return {"schema_version": 1, "receipt": receipt, "scope_empty": empty, "never_entered": never, "scope_identity": {**{k: scope[k] for k in ("boot_id", "device", "inode")}, "cgroup_path": authority.profile.data["cgroup_parent"]}}

if __name__ == "__main__":
    try:
        # Entry has no caller stdin. Root systemd hooks likewise read no model prompt.
        mode = sys.argv[1] if len(sys.argv) > 1 else ""
        raw = b"" if mode in ("enter", "unit-pre", "unit-post") else sys.stdin.buffer.read(MAX_FRAME + 1)
        result = run(sys.argv[1:], raw)
        if result is not None:
            sys.stdout.buffer.write(canonical(result) + b"\n")
    except (Refusal, OSError, KeyError, ValueError, TypeError, subprocess.SubprocessError):
        # Never serialize input, exception strings, paths, prompt, tokens or private identity.
        sys.stdout.buffer.write(b'{"schema_version":1,"error":"refused"}\n')
        sys.exit(1)
