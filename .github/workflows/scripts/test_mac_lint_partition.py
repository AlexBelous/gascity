import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest
from unittest import mock

import mac_lint_partition


class MacLintPartitionTests(unittest.TestCase):
    def invoke(self, finding=False, warning=False, bad_selector=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".golangci.yml").write_text("version: 2\n")
            diagnostics = root / "diagnostics"
            diagnostics.mkdir()

            def fake_run(command, stdout=None, stderr=None, **_kwargs):
                if command[0] == "pinned-lint":
                    if command[1] == "formatters":
                        enabled = mac_lint_partition.FORMATTERS
                    elif any(value.startswith("--enable-only=") for value in command):
                        enabled = mac_lint_partition.HEAVY
                    elif "--default=none" in command:
                        enabled = mac_lint_partition.OTHER
                    else:
                        enabled = mac_lint_partition.HEAVY + mac_lint_partition.OTHER
                    if bad_selector and "--default=none" in command:
                        enabled = mac_lint_partition.HEAVY
                    json.dump({"Enabled": [{"name": name} for name in enabled]}, stdout)
                    return subprocess.CompletedProcess(command, 0)

                flags = shlex.split(command[-1].removeprefix("LINT_FLAGS="))
                output = Path(next(value.split("=", 1)[1] for value in flags
                                   if value.startswith("--output.json.path=")))
                stage = output.parent.name
                enabled = (mac_lint_partition.HEAVY if stage == "heavy" else
                           mac_lint_partition.OTHER + mac_lint_partition.FORMATTERS)
                report = {"Linters": [{"Name": name, "Enabled": True}
                                      for name in enabled + ("typecheck",)],
                          "Warnings": ["analysis warning"] if warning and stage == "remaining" else []}
                issues = [{"FromLinter": "gocritic"}] if finding and stage == "remaining" else []
                output.write_text(json.dumps({"Issues": issues, "Report": report}))
                return subprocess.CompletedProcess(command, 2 if issues else 0)

            previous = Path.cwd()
            os.chdir(root)
            try:
                with mock.patch.object(mac_lint_partition.subprocess, "run", side_effect=fake_run):
                    result = mac_lint_partition.run(diagnostics, "pinned-lint")
            finally:
                os.chdir(previous)
            return result, json.loads((diagnostics / "partition.json").read_text())

    def test_all_checks_complete_with_one_shared_budget(self):
        result, receipt = self.invoke()
        self.assertEqual(result, 0)
        self.assertTrue(receipt["complete"])
        self.assertTrue(receipt["lint_acceptance"])
        self.assertEqual(receipt["completed_checks"], sorted(mac_lint_partition.ALL))
        self.assertEqual([stage["returncode"] for stage in receipt["stages"]], [0, 0])
        self.assertEqual(receipt["budget_seconds"], 2700)

    def test_lint_finding_is_not_reported_as_success(self):
        result, receipt = self.invoke(finding=True)
        self.assertEqual(result, 2)
        self.assertFalse(receipt["lint_acceptance"])
        self.assertEqual(receipt["stages"][1]["analysis"]["issues"], 1)

    def test_tool_warning_is_not_reported_as_success(self):
        result, receipt = self.invoke(warning=True)
        self.assertEqual(result, 125)
        self.assertFalse(receipt["lint_acceptance"])
        self.assertEqual(receipt["stages"][1]["analysis"]["warnings"], 1)

    def test_selector_mismatch_fails_before_analysis(self):
        result, receipt = self.invoke(bad_selector=True)
        self.assertEqual(result, 125)
        self.assertFalse(receipt["lint_acceptance"])
        self.assertEqual(receipt["stages"], [])

    def test_incomplete_analysis_report_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "result.json"
            path.write_text(json.dumps({"Issues": [], "Report": {"Linters": []}}))
            with self.assertRaises(ValueError):
                mac_lint_partition.validate_result(path, mac_lint_partition.HEAVY, 0)


if __name__ == "__main__":
    unittest.main()
