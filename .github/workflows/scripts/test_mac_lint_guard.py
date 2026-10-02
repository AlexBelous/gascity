import json
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

import mac_lint_guard


def running(pid):
    result = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)],
                            capture_output=True, text=True)
    return bool(result.stdout.strip()) and not result.stdout.strip().startswith("Z")


class MacLintGuardTests(unittest.TestCase):
    def invoke(self, code, path, budget=2):
        return mac_lint_guard.run_guard([sys.executable, "-c", code], path,
                                       budget, grace_seconds=0.2, interval_seconds=0.1)

    def test_success_and_failure_keep_actual_status(self):
        with tempfile.TemporaryDirectory() as root:
            for expected in (0, 7):
                path = Path(root) / str(expected)
                self.assertEqual(self.invoke(f"raise SystemExit({expected})", path), expected)
                receipt = json.loads((path / "process-budget.json").read_text())
                self.assertEqual(receipt["child_returncode"], expected)
                self.assertFalse(receipt["deadline_fired"])
                self.assertTrue(receipt["cleanup_verified"])
                self.assertTrue((path / "owned-process-samples.jsonl").exists())

    def test_deadline_stays_failed_when_quit_handler_exits_zero(self):
        with tempfile.TemporaryDirectory() as root:
            code = "import signal,time; signal.signal(signal.SIGQUIT,lambda *_:exit(0)); time.sleep(30)"
            self.assertEqual(self.invoke(code, Path(root), budget=0.3), 124)
            receipt = json.loads((Path(root) / "process-budget.json").read_text())
            self.assertTrue(receipt["deadline_fired"])
            self.assertEqual(receipt["returncode"], 124)
            self.assertEqual(receipt["child_returncode"], 0)
            self.assertLess(receipt["elapsed_seconds"], 3)
            self.assertTrue(receipt["cleanup_verified"])

    def test_hung_descendant_reaped_and_other_group_survives(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            marker = root / "descendant.pid"
            other = subprocess.Popen([sys.executable, "-c", "import time;time.sleep(30)"],
                                     start_new_session=True)
            try:
                nested = ("import signal,time; signal.signal(signal.SIGQUIT,signal.SIG_IGN); "
                          "signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(30)")
                code = ("import subprocess,sys,time,pathlib,signal; "
                        "signal.signal(signal.SIGQUIT,lambda *_:exit(0)); "
                        f"p=subprocess.Popen([sys.executable,'-c',{nested!r}]); "
                        f"pathlib.Path({str(marker)!r}).write_text(str(p.pid)); time.sleep(30)")
                self.assertEqual(self.invoke(code, root / "diagnostics", budget=0.3), 124)
                self.assertFalse(running(int(marker.read_text())))
                self.assertIsNone(other.poll())
                samples = [json.loads(line) for line in
                           (root / "diagnostics" / "owned-process-samples.jsonl").read_text().splitlines()]
                self.assertTrue(samples)
                for sample in samples:
                    for row in sample.get("processes", []):
                        self.assertNotEqual(row["pid"], other.pid)
                        self.assertNotIn("argv", row)
            finally:
                other.terminate()
                other.wait(timeout=3)

    def test_normal_root_exit_does_not_leave_running_descendants(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            marker = root / "descendant.pid"
            code = ("import subprocess,sys,pathlib; "
                    "p=subprocess.Popen([sys.executable,'-c','import time;time.sleep(30)']); "
                    f"pathlib.Path({str(marker)!r}).write_text(str(p.pid))")
            self.assertEqual(self.invoke(code, root / "diagnostics"), 125)
            self.assertFalse(running(int(marker.read_text())))

    def test_sigterm_cleans_only_launched_group_and_records_signal(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            diagnostics = root / "diagnostics"
            marker = root / "child.pid"
            code = f"import os,pathlib,time;pathlib.Path({str(marker)!r}).write_text(str(os.getpid()));time.sleep(30)"
            guard = subprocess.Popen([sys.executable, str(Path(mac_lint_guard.__file__)),
                                      "--diagnostics", str(diagnostics), "--timeout-seconds", "30",
                                      "--grace-seconds", "0.2", "--", sys.executable, "-c", code])
            try:
                until = time.monotonic() + 3
                while not marker.exists() and time.monotonic() < until:
                    time.sleep(0.01)
                self.assertTrue(marker.exists())
                guard.send_signal(signal.SIGTERM)
                self.assertEqual(guard.wait(timeout=3), 143)
                self.assertFalse(running(int(marker.read_text())))
                receipt = json.loads((diagnostics / "process-budget.json").read_text())
                self.assertEqual(receipt["interrupted_signal"], signal.SIGTERM)
            finally:
                if guard.poll() is None:
                    guard.terminate()
                    guard.wait(timeout=3)

    def test_invalid_budget_and_reused_evidence_fail_before_launch(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            with self.assertRaises(ValueError):
                self.invoke("raise SystemExit(0)", root, budget=0)
            (root / "process-budget.json").write_text("original")
            with self.assertRaises(ValueError):
                self.invoke("raise SystemExit(0)", root)
            self.assertEqual((root / "process-budget.json").read_text(), "original")

    def test_failed_observer_cannot_prevent_hung_group_cleanup(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            marker = root / "child.pid"
            code = ("import os,pathlib,signal,time; "
                    "signal.signal(signal.SIGQUIT,signal.SIG_IGN); "
                    "signal.signal(signal.SIGTERM,signal.SIG_IGN); "
                    f"pathlib.Path({str(marker)!r}).write_text(str(os.getpid())); time.sleep(30)")
            with mock.patch.object(mac_lint_guard, "owned_processes",
                                   side_effect=subprocess.TimeoutExpired("ps", 2)):
                self.assertEqual(self.invoke(code, root / "diagnostics", budget=0.3), 124)
            self.assertFalse(running(int(marker.read_text())))
            samples = [json.loads(line) for line in
                       (root / "diagnostics" / "owned-process-samples.jsonl").read_text().splitlines()]
            self.assertTrue(any(s.get("observer_error") == "TimeoutExpired" for s in samples))

    def test_launch_failure_is_durable_and_nonzero(self):
        with tempfile.TemporaryDirectory() as root:
            self.assertEqual(mac_lint_guard.run_guard(
                [str(Path(root) / "missing-executable")], root, 1), 125)
            receipt = json.loads((Path(root) / "process-budget.json").read_text())
            self.assertEqual(receipt["guard_error"], "FileNotFoundError")
            self.assertEqual(receipt["returncode"], 125)

    def test_failed_cleanup_probe_still_kills_launched_group(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            marker = root / "child.pid"
            nested = ("import signal,time; signal.signal(signal.SIGQUIT,signal.SIG_IGN); "
                      "signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(30)")
            code = ("import subprocess,sys,pathlib,signal,time; "
                    "signal.signal(signal.SIGQUIT,signal.SIG_IGN); "
                    "signal.signal(signal.SIGTERM,signal.SIG_IGN); "
                    f"p=subprocess.Popen([sys.executable,'-c',{nested!r}]); "
                    f"pathlib.Path({str(marker)!r}).write_text(str(p.pid)); time.sleep(30)")
            probe = mac_lint_guard.group_running
            failed = False

            def fail_once(pgid):
                nonlocal failed
                if not failed:
                    failed = True
                    raise subprocess.TimeoutExpired("ps", 2)
                return probe(pgid)

            other = subprocess.Popen([sys.executable, "-c", "import time;time.sleep(30)"],
                                     start_new_session=True)
            try:
                with mock.patch.object(mac_lint_guard, "group_running", side_effect=fail_once):
                    self.assertEqual(self.invoke(code, root / "diagnostics", budget=0.3), 124)
                receipt = json.loads((root / "diagnostics" / "process-budget.json").read_text())
                self.assertFalse(running(receipt["pid"]))
                self.assertFalse(running(int(marker.read_text())))
                self.assertIsNone(other.poll())
                self.assertEqual(receipt["guard_error"], "TimeoutExpired")
                self.assertIn("SIGKILL", receipt["signals"])
                self.assertTrue(receipt["cleanup_verified"])
                self.assertLess(receipt["elapsed_seconds"], 3)
            finally:
                other.terminate()
                other.wait(timeout=3)


if __name__ == "__main__":
    unittest.main()
