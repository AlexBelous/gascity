"""Bound only the process group we launch; keep lint failures and live evidence."""

import argparse
import json
import math
import os
from pathlib import Path
import resource
import signal
import subprocess
import sys
import time


def group_running(pgid):
    denied = None
    try:
        os.killpg(pgid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError as error:
        denied = error
    # Darwin returns EPERM for an unreaped zombie-only group. Do not equate
    # EPERM with disappearance: prove that no live member remains via ps.
    live = any(not row["state"].startswith("Z") for row in owned_processes(pgid))
    if denied is not None and live:
        raise denied
    return live


def owned_processes(pgid):
    # comm is the executable name, never argv or environment. Do not save other groups.
    result = subprocess.run(
        ["ps", "-axo", "pid=,ppid=,pgid=,pcpu=,rss=,stat=,comm="],
        capture_output=True, text=True, timeout=2, check=True,
    )
    rows = []
    for line in result.stdout.splitlines():
        fields = line.split(maxsplit=6)
        if len(fields) == 7 and fields[2] == str(pgid):
            rows.append({
                "pid": int(fields[0]), "ppid": int(fields[1]), "pgid": int(fields[2]),
                "cpu_percent": fields[3], "rss_kib": fields[4], "state": fields[5],
                "executable": Path(fields[6]).name,
            })
    return rows


def run_guard(command, diagnostics, timeout_seconds, grace_seconds=5, interval_seconds=30):
    for value in (timeout_seconds, grace_seconds, interval_seconds):
        if not math.isfinite(value) or value <= 0:
            raise ValueError("budgets must be finite positive seconds")
    diagnostics = Path(diagnostics)
    diagnostics.mkdir(parents=True, exist_ok=True)
    receipt_path = diagnostics / "process-budget.json"
    samples_path = diagnostics / "owned-process-samples.jsonl"
    if receipt_path.exists() or samples_path.exists():
        raise ValueError("refusing to overwrite evidence from an earlier invocation")

    started = time.monotonic()
    receipt = {"timeout_seconds": timeout_seconds, "grace_seconds": grace_seconds,
               "deadline_fired": False, "interrupted_signal": None, "signals": []}
    child = None
    interrupted = None
    previous_handlers = {}
    next_sample = started
    next_native_sample = started + 300
    status = 125

    def save_receipt():
        receipt["elapsed_seconds"] = time.monotonic() - started
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")

    def interrupt(signum, _frame):
        nonlocal interrupted
        interrupted = signum

    def send(signum):
        if child.pid <= 1 or child.pid == os.getpgrp():
            raise RuntimeError("refusing to signal the guard's own process group")
        if child.poll() is None and os.getpgid(child.pid) != child.pid:
            raise RuntimeError("launched process no longer owns its isolated group")
        try:
            os.killpg(child.pid, signum)
            receipt["signals"].append(signal.Signals(signum).name)
        except ProcessLookupError:
            pass
        except PermissionError:
            if group_running(child.pid):
                raise

    def drain(first_signal):
        must_kill = True
        try:
            send(first_signal)
            until = time.monotonic() + grace_seconds
            while time.monotonic() < until:
                child.poll()  # Reap the group leader even if descendants remain.
                if not group_running(child.pid):
                    must_kill = False
                    break
                time.sleep(0.05)
        finally:
            # A failed observation must not prevent escalation of our launch group.
            if must_kill:
                send(signal.SIGKILL)
            child.wait(timeout=grace_seconds)

    try:
        for signum in (signal.SIGINT, signal.SIGTERM):
            previous_handlers[signum] = signal.signal(signum, interrupt)
        child = subprocess.Popen(command, start_new_session=True,
                                 preexec_fn=lambda: resource.setrlimit(resource.RLIMIT_CORE, (0, 0)))
        receipt.update(pid=child.pid, pgid=child.pid, start_new_session=True, core_dumps_disabled=True)
        save_receipt()  # Evidence exists before expensive analysis or an outer cancel.
        with samples_path.open("x", encoding="utf-8") as samples:
            while child.poll() is None:
                now = time.monotonic()
                if interrupted is not None:
                    receipt["interrupted_signal"] = interrupted
                    status = 128 + interrupted
                    save_receipt()
                    drain(signal.SIGTERM)
                    break
                if now - started >= timeout_seconds:
                    # Latch failure before QUIT: even a handler exiting zero cannot turn it green.
                    receipt["deadline_fired"] = True
                    status = 124
                    save_receipt()
                    print("lint guard: deadline exceeded; dumping own Go stacks", flush=True)
                    drain(signal.SIGQUIT)
                    break
                if now >= next_sample:
                    observation = {"elapsed_seconds": now - started}
                    try:
                        rows = owned_processes(child.pid)
                        observation["processes"] = rows
                        if sys.platform == "darwin" and now >= next_native_sample:
                            next_native_sample = now + 300
                            target = next((r["pid"] for r in rows
                                           if r["executable"] == "golangci-lint"), None)
                            if target is not None and os.getpgid(target) == child.pid:
                                # sample briefly suspends its target; one 1s observation per 300s.
                                path = diagnostics / f"native-sample-{int(now-started)}-{target}.txt"
                                observed = subprocess.run(
                                    ["/usr/bin/sample", str(target), "1", "1", "-file", str(path)],
                                    capture_output=True, text=True, timeout=3,
                                )
                                observation["native_sample"] = {"pid": target,
                                    "returncode": observed.returncode, "file": path.name}
                    except (OSError, subprocess.SubprocessError) as error:
                        observation["observer_error"] = type(error).__name__
                    samples.write(json.dumps(observation) + "\n")
                    samples.flush()
                    next_sample = now + interval_seconds
                    continue  # Recheck deadline after bounded observers (at most 5s).
                time.sleep(min(0.1, next_sample - now, timeout_seconds - (now - started)))
            else:
                if interrupted is not None:
                    receipt["interrupted_signal"] = interrupted
                    status = 128 + interrupted
                elif time.monotonic() - started >= timeout_seconds:
                    receipt["deadline_fired"] = True
                    status = 124
                else:
                    status = child.returncode if child.returncode >= 0 else 128 - child.returncode
    except (OSError, subprocess.SubprocessError, RuntimeError) as error:
        receipt["guard_error"] = type(error).__name__
        if status == 0:
            status = 125
    finally:
        try:
            if child is not None:
                child.poll()
                try:
                    try:
                        remaining = group_running(child.pid)
                    except (OSError, subprocess.SubprocessError) as error:
                        receipt["cleanup_probe_error"] = type(error).__name__
                        remaining = True  # Unknown is not proof of successful cleanup.
                    if remaining:
                        receipt["remaining_group_after_command"] = True
                        if status == 0:
                            status = 125  # A successful root with live descendants is not clean.
                        drain(signal.SIGTERM)
                except (OSError, subprocess.SubprocessError, RuntimeError) as error:
                    receipt["cleanup_error"] = type(error).__name__
                    if status == 0:
                        status = 125
                receipt["child_returncode"] = child.poll()
                try:
                    live = [row["pid"] for row in owned_processes(child.pid)
                            if not row["state"].startswith("Z")]
                    receipt["live_members_after_cleanup"] = live
                    receipt["cleanup_verified"] = not live and child.returncode is not None
                except (OSError, subprocess.SubprocessError) as error:
                    receipt["cleanup_verification_error"] = type(error).__name__
                    receipt["cleanup_verified"] = False
                if not receipt["cleanup_verified"] and status == 0:
                    status = 125
            receipt["returncode"] = status
            save_receipt()
        finally:
            for signum, handler in previous_handlers.items():
                signal.signal(signum, handler)
    return status


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--diagnostics", required=True, type=Path)
    parser.add_argument("--timeout-seconds", type=float, default=2700)
    parser.add_argument("--grace-seconds", type=float, default=5)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("a command after -- is required")
    return run_guard(command, args.diagnostics, args.timeout_seconds, args.grace_seconds)


if __name__ == "__main__":
    sys.exit(main())
