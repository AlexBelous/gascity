#!/usr/bin/env python3
"""Disposable synthetic proc trees. Never starts production helper or scans live proc."""
import array
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import unittest

EXE = Path(sys.argv.pop(1)).resolve()
LIMIT_EXE = Path(sys.argv.pop(1)).resolve()
BOOT = "01234567-89ab-cdef-0123-456789abcdef"
NONCE = "a" * 64
TOKEN = "never-output-raw-token"


def stat(pid, parent=1, start=100, flags=0):
    # Fields 3..22; comm contains spaces/parentheses to exercise kernel stat grammar.
    fields = ["0"] * 16
    fields[3] = str(flags)  # kernel stat field 9
    return f"{pid} (name (with) spaces) S {parent} {pid} " + " ".join(fields) + f" {start}\n"


class HelperTests(unittest.TestCase):
    def test_disposable_syscalls_denied(self):
        result = subprocess.run([str(EXE), "--sandbox-selftest"], capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b"")
        self.assertEqual(result.stderr, b"")

    def setUp(self):
        self.temp = Path(tempfile.mkdtemp(prefix="gc-observer-fixture-"))
        self.proc = self.temp / "proc"
        (self.proc / "sys/kernel/random").mkdir(parents=True)
        (self.proc / "sys/kernel/random/boot_id").write_text(BOOT + "\n")
        (self.proc / "fixture_namespace").write_text("pid:[12345]\n")
        self.pid = os.getpid()
        self.process(1, name="init")
        self.process(self.pid, start=555, name="controller")
        self.process(901, name="tmux: server")
        self.process(902, parent=901, name="worker", env=self.env())
        self.binding = self.temp / "binding.conf"
        self.binding.write_text(
            f"boot_id={BOOT}\ncontroller_pid={self.pid}\ncontroller_uid={os.getuid()}\n"
            "controller_start_ticks=555\ncontroller_source_revision=" + "b" * 40 + "\n"
            "controller_binary_sha256=" + "c" * 64 + "\npid_namespace_identity=pid:[12345]\n"
            "helper_binary_sha256=" + hashlib.sha256(EXE.read_bytes()).hexdigest() + "\n"
        )

    def tearDown(self):
        shutil.rmtree(self.temp)

    @staticmethod
    def env():
        return (
            "GC_SESSION_ID=controller-session-2\0GC_CITY_PATH=/fixture/city\0"
            "GC_TEMPLATE=fixture.clerk\0GC_RUNTIME_EPOCH=8\0"
            f"GC_INSTANCE_TOKEN={TOKEN}\0UNRELATED_SECRET=never-output-either\0"
        ).encode()

    def process(self, pid, parent=1, start=100, name="worker", env=b"", flags=0):
        p = self.proc / str(pid)
        p.mkdir(exist_ok=True)
        (p / "stat").write_text(stat(pid, parent, start, flags))
        (p / "comm").write_text(name + "\n")
        (p / "environ").write_bytes(env)

    def run_helper(self, request=None, extra=b"", rights=False, executable=EXE, close_peer=False, after_spawn=None):
        if executable != EXE:
            self.binding.write_text(self.binding.read_text().replace(
                hashlib.sha256(EXE.read_bytes()).hexdigest(),
                hashlib.sha256(executable.read_bytes()).hexdigest()))
        parent, child = socket.socketpair()
        # fd3 reserved in child, no inherited arbitrary descriptors.
        childfd = child.fileno()
        # Preserve fd3 through close_fds: use pass_fds including a temporary fd3
        # in the parent. This fixture uses only this process's private sockets.
        backup = None
        if childfd != 3:
            try:
                backup = os.dup(3)
            except OSError:
                pass
            os.dup2(childfd, 3)
        try:
            p = subprocess.Popen([str(executable), str(self.proc), str(self.binding)], pass_fds=(3,), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        finally:
            if childfd != 3:
                if backup is None:
                    os.close(3)
                else:
                    os.dup2(backup, 3)
                    os.close(backup)
        child.close()
        if after_spawn is not None:
            after_spawn()
        # parent originally used fd3 in many cases; move socket before fd replacement.
        if request is None:
            request = {"schema": "observe-host-processes/v1", "request_nonce": NONCE}
        payload = request if isinstance(request, bytes) else json.dumps(request).encode()
        wire = struct.pack("!I", len(payload)) + payload + extra
        try:
            if rights:
                with open(self.binding, "rb") as f:
                    parent.sendmsg([wire], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [f.fileno()]))])
            else:
                parent.sendall(wire)
            parent.shutdown(socket.SHUT_WR)
            if close_peer:
                parent.close()
                response = b""
            else:
                chunks = []
                while True:
                    chunk = parent.recv(65536)
                    if not chunk:
                        break
                    chunks.append(chunk)
                response = b"".join(chunks)
        except (ConnectionResetError, BrokenPipeError):
            response = b""
        finally:
            parent.close()
        stdout, stderr = p.communicate(timeout=12)
        self.assertEqual(stdout, b"")
        self.assertEqual(stderr, b"")
        if response:
            self.assertGreaterEqual(len(response), 4)
            size = struct.unpack("!I", response[:4])[0]
            self.assertEqual(len(response), size + 4)
            self.assertNotIn(TOKEN.encode(), response)
            self.assertNotIn(b"never-output-either", response)
            return p.returncode, json.loads(response[4:])
        return p.returncode, None

    def test_closed_peer_returns_failure_without_sigpipe_death(self):
        code, result = self.run_helper(close_peer=True)
        self.assertEqual(code, 2)
        self.assertIsNone(result)

    def test_complete_redacted_root_and_sha(self):
        code, result = self.run_helper()
        self.assertEqual(code, 0)
        self.assertTrue(result["complete"])
        self.assertEqual(result["scope"], "fixture_procfs")
        self.assertEqual(result["errors_total"], 0)
        self.assertEqual(result["enumerated_count_before"], 4)
        self.assertEqual(result["enumeration_digest_before"], result["enumeration_digest_after"])
        self.assertEqual(len(result["roots"]), 1)
        root = result["roots"][0]
        self.assertEqual(root["pid"], 902)
        self.assertEqual(root["start_ticks"], "100")
        self.assertTrue(root["parent_is_provider_infrastructure"])
        self.assertEqual(root["parent_name"], "tmux: server")
        self.assertEqual(root["instance_token_sha256"], hashlib.sha256(TOKEN.encode()).hexdigest())

    def test_exact_pid_limit_and_one_more_are_unknown(self):
        # Test-only limit=4 hits the end of the getdents chunk in the exact case.
        for extra in [False, True]:
            with self.subTest(extra=extra):
                if extra:
                    self.process(903)
                code, result = self.run_helper(executable=LIMIT_EXE)
                self.assertEqual(code, 1)
                self.assertFalse(result["complete"])
                self.assertTrue(any(e["reason"] == "limit_reached" for e in result["errors"]))

    def test_invalid_boot_namespace_cannot_inject_response(self):
        original = self.binding.read_text()
        for old, new in [(BOOT, 'x"malicious'), ("pid:[12345]", 'x"malicious')]:
            self.binding.write_text(original.replace(old, new))
            code, result = self.run_helper()
            self.assertEqual(code, 2)
            self.assertIsNone(result)

    def test_gc_child_is_not_second_root(self):
        self.process(903, parent=902, env=self.env())
        code, result = self.run_helper()
        self.assertEqual(code, 0)
        self.assertEqual([r["pid"] for r in result["roots"]], [902])

    def test_tmux_substring_is_not_excluded(self):
        (self.proc / "902/comm").write_text("tmux-worker\n")
        code, result = self.run_helper()
        self.assertEqual(code, 0)
        self.assertEqual(result["roots"][0]["name"], "tmux-worker")

    def test_missing_file_is_unknown_not_empty(self):
        (self.proc / "1/environ").unlink()
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertFalse(result["complete"])
        self.assertTrue(result["errors"])
        self.assertEqual(len(result["roots"]), 1)

    def test_kernel_flag_proves_no_user_environment_without_losing_pid_coverage(self):
        self.process(903, name="kworker", flags=0x00200000)
        (self.proc / "903/environ").unlink()
        code, result = self.run_helper()
        self.assertEqual(code, 0)
        self.assertTrue(result["complete"])
        self.assertEqual(result["enumerated_count_before"], 5)
        self.assertEqual(result["enumerated_count_after"], 5)
        self.assertEqual(result["enumeration_digest_before"], result["enumeration_digest_after"])
        self.assertEqual(result["errors_total"], 0)
        self.assertEqual([r["pid"] for r in result["roots"]], [902])
        # The same comm without the kernel flag cannot excuse unreadable env.
        (self.proc / "903/stat").write_text(stat(903))
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertTrue(any(e["pid"] == 903 and e["operation"] == "environ" for e in result["errors"]))

    def test_non_gc_process_title_environment_is_not_managed_identity(self):
        self.process(903, name="sshd", env=b"process-title\0=not-an-assignment\0")
        code, result = self.run_helper()
        self.assertEqual(code, 0)
        self.assertTrue(result["complete"])
        self.assertEqual(result["enumerated_count_before"], 5)
        self.assertEqual([r["pid"] for r in result["roots"]], [902])
        # Any GC_ entry reactivates strict parsing of the entire environment.
        (self.proc / "903/environ").write_bytes(self.env() + b"process-title\0")
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertTrue(any(e["pid"] == 903 and e["reason"] == "malformed_environment" for e in result["errors"]))

    def test_kernel_classification_requires_readable_strict_stat(self):
        self.process(903, name="kworker", flags=0x00200000)
        target = self.proc / "903/stat"
        for data in [stat(903, flags="not-decimal"), "903 (kworker) S 2 0\n"]:
            target.write_text(data)
            code, result = self.run_helper()
            self.assertEqual(code, 1)
            self.assertTrue(any(e["pid"] == 903 and e["operation"] == "stat" for e in result["errors"]))
        target.unlink()
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertTrue(any(e["pid"] == 903 and e["operation"] == "stat" for e in result["errors"]))

    def test_kernel_named_tmux_cannot_attribute_managed_child(self):
        self.process(901, name="tmux: server", flags=0x00200000)
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertEqual(result["roots"], [])
        self.assertTrue(any(e["reason"] == "parent_not_user_process" for e in result["errors"]))

    def test_user_esrch_is_not_excused_by_kernel_name_or_parent(self):
        self.process(2, parent=0, name="kthreadd", flags=0x00200000)
        self.process(903, parent=2, name="[kworker]", env=self.env())
        (self.proc / "903/environ.fixture-errno").write_text("3")
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertFalse(result["complete"])
        self.assertTrue(any(e["pid"] == 903 and e["operation"] == "environ" and e["errno"] == 3 for e in result["errors"]))

    def test_kernel_bit_change_between_stat_reads_is_unknown(self):
        self.process(903, name="kworker", flags=0x00200000)
        comm = self.proc / "903/comm"
        comm.unlink()
        os.mkfifo(comm)
        errors = []

        def transition_at_comm_read():
            try:
                # Opening the private FIFO proves the helper reached comm
                # after statA; EOF releases it to read statZ, without sleeps.
                with comm.open("wb") as output:
                    (self.proc / "903/stat").write_text(stat(903))
                    regular_comm = comm.with_name("comm.next")
                    regular_comm.write_text("kworker\n")
                    os.replace(regular_comm, comm)
                    output.write(b"kworker\n")
            except Exception as error:
                errors.append(error)

        writer = threading.Thread(target=transition_at_comm_read, daemon=True)
        code, result = self.run_helper(after_spawn=writer.start)
        writer.join(timeout=10)
        self.assertFalse(writer.is_alive())
        self.assertEqual(errors, [])
        self.assertEqual(code, 1)
        self.assertTrue(any(e["pid"] == 903 and e["reason"] == "incarnation_changed" for e in result["errors"]))

    def test_kernel_comm_change_does_not_change_incarnation_digest(self):
        for kernel in [True, False]:
            with self.subTest(kernel=kernel):
                self.process(903, name="worker-old", flags=0x00200000 if kernel else 0)
                comm = self.proc / "903/comm"
                comm.unlink()
                os.mkfifo(comm)
                errors = []

                def transition_at_comm_read():
                    try:
                        # The first scan holds this FIFO inode; the next sees
                        # the replacement. PID/start/stat flags remain exact.
                        with comm.open("wb") as output:
                            replacement = comm.with_name("comm.next")
                            replacement.write_text("worker-new\n")
                            os.replace(replacement, comm)
                            output.write(b"worker-old\n")
                    except Exception as error:
                        errors.append(error)

                writer = threading.Thread(target=transition_at_comm_read, daemon=True)
                code, result = self.run_helper(after_spawn=writer.start)
                writer.join(timeout=10)
                self.assertFalse(writer.is_alive())
                self.assertEqual(errors, [])
                self.assertEqual(code, 0 if kernel else 1)
                self.assertEqual(result["complete"], kernel)
                self.assertEqual(result["enumerated_count_before"], result["enumerated_count_after"])
                if kernel:
                    self.assertEqual(result["enumeration_digest_before"], result["enumeration_digest_after"])
                else:
                    self.assertTrue(any(e["reason"] == "coverage_changed" for e in result["errors"]))

    def test_kernel_start_or_count_change_still_denies_coverage(self):
        for mode in ["start", "count"]:
            with self.subTest(mode=mode):
                self.process(903, name="kworker", flags=0x00200000)
                comm = self.proc / "903/comm"
                comm.unlink()
                os.mkfifo(comm)
                errors = []

                def transition_at_comm_read():
                    try:
                        with comm.open("wb") as output:
                            if mode == "start":
                                (self.proc / "903/stat").write_text(stat(903, start=101, flags=0x00200000))
                            else:
                                self.process(904, name="new-kworker", flags=0x00200000)
                            replacement = comm.with_name("comm.next")
                            replacement.write_text("kworker\n")
                            os.replace(replacement, comm)
                            output.write(b"kworker\n")
                    except Exception as error:
                        errors.append(error)

                writer = threading.Thread(target=transition_at_comm_read, daemon=True)
                code, result = self.run_helper(after_spawn=writer.start)
                writer.join(timeout=10)
                self.assertFalse(writer.is_alive())
                self.assertEqual(errors, [])
                self.assertEqual(code, 1)
                self.assertFalse(result["complete"])
                reason = "incarnation_changed" if mode == "start" else "coverage_changed"
                self.assertTrue(any(e["reason"] == reason for e in result["errors"]))

    @unittest.skipIf(os.getuid() == 0, "permission fixture requires unprivileged test UID")
    def test_permission_denied_is_unknown(self):
        target = self.proc / "902/environ"
        target.chmod(0)
        try:
            code, result = self.run_helper()
            self.assertEqual(code, 1)
            self.assertFalse(result["complete"])
            self.assertTrue(any(e["operation"] == "environ" and e["errno"] == 13 for e in result["errors"]))
        finally:
            target.chmod(0o600)

    def test_error_list_is_bounded(self):
        for pid in range(10000, 10140):
            self.process(pid)
            (self.proc / str(pid) / "environ").unlink()
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertFalse(result["complete"])
        self.assertEqual(len(result["errors"]), 128)
        self.assertTrue(result["errors_truncated"])
        self.assertGreater(result["errors_total"], 128)

    def test_response_overflow_is_unknown(self):
        for pid in range(11000, 11800):
            env = self.env().replace(b"/fixture/city", b"\x01" * 4096)
            self.process(pid, env=env)
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertFalse(result["complete"])
        self.assertEqual(result["roots"], [])
        self.assertTrue(any(e["reason"] == "response_limit" for e in result["errors"]))

    def test_environment_overflow_unknown(self):
        (self.proc / "902/environ").write_bytes(b"a" * (16 * 1024 * 1024 + 1))
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertFalse(result["complete"])
        self.assertTrue(any(e["reason"] == "limit_reached" for e in result["errors"]))

    def test_duplicate_and_unterminated_gc_identity(self):
        for data in [self.env() + b"GC_TEMPLATE=other\0", self.env()[:-1], b"GC_BAD_ENTRY\0"]:
            with self.subTest(data_length=len(data)):
                (self.proc / "902/environ").write_bytes(data)
                code, result = self.run_helper()
                self.assertEqual(code, 1)
                self.assertFalse(result["complete"])

    def test_no_symlink_escape(self):
        (self.proc / "902/environ").unlink()
        (self.proc / "902/environ").symlink_to(self.binding)
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertFalse(result["complete"])

    def test_missing_parent_unknown(self):
        (self.proc / "902/stat").write_text(stat(902, parent=999))
        code, result = self.run_helper()
        self.assertEqual(code, 1)
        self.assertTrue(any(e["reason"] == "parent_unavailable" for e in result["errors"]))

    def test_bad_request_rejected(self):
        cases = [
            {"schema": "observe-host-processes/v1", "request_nonce": NONCE, "path": "/etc/shadow"},
            {"schema": "observe-host-processes/v1", "request_nonce": NONCE, "pid": 1},
            {"schema": "wrong", "request_nonce": NONCE},
            {"schema": "observe-host-processes/v1", "request_nonce": "A" * 64},
            b'{"schema":"observe-host-processes/v1","schema":"observe-host-processes/v1","request_nonce":"' + NONCE.encode() + b'"}',
            b"x" * 1025,
        ]
        for request in cases:
            with self.subTest(request=str(request)[:40]):
                code, result = self.run_helper(request)
                self.assertEqual(code, 2)
                self.assertIsNone(result)

    def test_additional_frame_and_ancillary_fd_rejected(self):
        for kwargs in [{"extra": b"\0\0\0\0"}, {"rights": True}]:
            code, result = self.run_helper(**kwargs)
            self.assertEqual(code, 2)
            self.assertIsNone(result)

    def test_wrong_binding_rejected(self):
        original = self.binding.read_text()
        for old, new in [(f"controller_pid={self.pid}", "controller_pid=98765"),
                         (f"controller_uid={os.getuid()}", "controller_uid=98765"),
                         ("controller_start_ticks=555", "controller_start_ticks=556"),
                         (BOOT, "ffffffff-ffff-ffff-ffff-ffffffffffff"),
                         ("pid:[12345]", "pid:[99999]")]:
            with self.subTest(changed=old):
                self.binding.write_text(original.replace(old, new))
                code, result = self.run_helper()
                self.assertEqual(code, 2)
                self.assertIsNone(result)


if __name__ == "__main__":
    unittest.main()
