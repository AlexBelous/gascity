"""Controlled analyzer-partition diagnostic; never a required quality-gate substitute."""

import argparse
import hashlib
import json
import math
import shlex
from pathlib import Path
import subprocess
import sys
import time

HEAVY = ("staticcheck", "unused", "unparam")
OTHER = ("errcheck", "errorlint", "gocritic", "govet", "ineffassign",
         "misspell", "revive", "unconvert")
FORMATTERS = ("gofumpt", "goimports")
ALL = frozenset(HEAVY + OTHER + FORMATTERS)
STAGES = (
    ("heavy", HEAVY, ("--enable-only=" + ",".join(HEAVY),)),
    ("remaining", OTHER + FORMATTERS,
     ("--default=none", "--enable=" + ",".join(OTHER),
      "--disable=" + ",".join(HEAVY))),
)


def names(data):
    enabled = data.get("Enabled")
    if not isinstance(enabled, list):
        raise ValueError("missing enabled-selector receipt")
    values = [row["name"] for row in enabled]
    if len(values) != len(set(values)):
        raise ValueError("duplicate selector name")
    return frozenset(values)


def validate_result(path, expected, rc):
    data = json.loads(Path(path).read_text())
    issues, report = data.get("Issues"), data.get("Report")
    if not isinstance(issues, list) or not isinstance(report, dict):
        raise ValueError("incomplete analysis JSON")
    rows = report.get("Linters")
    if not isinstance(rows, list) or report.get("Error"):
        raise ValueError("missing analyzer receipt or tool error")
    enabled = [row["Name"] for row in rows if row.get("Enabled", False)]
    if len(enabled) != len(set(enabled)):
        raise ValueError("duplicate enabled analyzer")
    # typecheck is golangci's internal error reporter, not one of the13 checks.
    if frozenset(enabled) - {"typecheck"} != frozenset(expected):
        raise ValueError("actual analyzer set differs from requested partition")
    if rc == 0 and issues:
        raise ValueError("zero exit with nonempty findings")
    return {"enabled": sorted(enabled), "issues": len(issues),
            "warnings": len(report.get("Warnings", []))}


def run(diagnostics, lint_tool):
    diagnostics = Path(diagnostics)
    receipt_path = diagnostics / "partition.json"
    if receipt_path.exists():
        raise ValueError("refusing to replace earlier partition evidence")
    started = time.monotonic()
    deadline = started + 2700
    receipt = {"kind": "DIAGNOSTIC_ONLY", "complete": False,
               "quality_acceptance": False, "collection_complete": False, "budget_seconds": 2700,
               "scope_each_stage": "./...", "expected_union": sorted(ALL),
               "config_sha256": hashlib.sha256(Path(".golangci.yml").read_bytes()).hexdigest(),
               "stages": [], "status": "preflight"}

    def save():
        receipt["elapsed_seconds"] = time.monotonic() - started
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n")

    def selectors(label, command, expected):
        with (diagnostics / (label + ".json")).open("x") as out, \
             (diagnostics / (label + ".stderr")).open("x") as err:
            result = subprocess.run([lint_tool, *command, "--json"],
                                    stdout=out, stderr=err, timeout=30, check=False)
        if result.returncode or names(json.loads((diagnostics / (label + ".json")).read_text())) != frozenset(expected):
            raise ValueError("selector preflight mismatch: " + label)

    save()
    try:
        selectors("original-linters", ["linters"], HEAVY + OTHER)
        selectors("original-formatters", ["formatters"], FORMATTERS)
        for label, expected, flags in STAGES:
            selectors(label + "-linters", ["linters", *flags],
                      tuple(name for name in expected if name not in FORMATTERS))
        final_rc = 0
        completed = set()
        for label, expected, flags in STAGES:
            remaining = deadline - time.monotonic()
            if remaining < 1:
                receipt["status"] = "deadline-before-stage"
                save()
                return 124
            stage = diagnostics / label
            stage.mkdir(mode=0o700)
            output = stage / "results.json"
            lint_flags = ["--concurrency=2", "--timeout=" + str(math.floor(remaining)) + "s",
                          "--verbose", "--show-stats", *flags,
                          "--output.json.path=" + str(output),
                          "--cpu-profile-path=" + str(stage / "cpu.pprof"),
                          "--mem-profile-path=" + str(stage / "memory.pprof")]
            command = ["make", "lint", "LINT_GOMEMLIMIT=3GiB",
                       "LINT_FLAGS=" + shlex.join(lint_flags)]
            item = {"name": label, "expected": sorted(expected), "command": command,
                    "started_seconds": time.monotonic() - started, "state": "running"}
            receipt["stages"].append(item)
            receipt["status"] = "running"
            save()
            print("partition: starting " + label, flush=True)
            with (stage / "stdout.log").open("x") as out, \
                 (stage / "stderr.log").open("x") as err:
                result = subprocess.run(command, stdout=out, stderr=err, check=False)
            rc = result.returncode if result.returncode >= 0 else 128 - result.returncode
            item.update(returncode=rc, ended_seconds=time.monotonic() - started,
                        state="completed")
            save()
            # GNU make returns2 when its single lint recipe returns1. Preserve
            # make's real failure; only complete valid analysis with findings may
            # continue to collect the other partition. Unknown errors stop.
            if rc not in (0, 2):
                receipt["status"] = "stage-tool-failure"
                save()
                return rc
            item["analysis"] = validate_result(output, expected, rc)
            if rc == 2 and item["analysis"]["issues"] == 0:
                receipt["status"] = "stage-tool-failure"
                save()
                return rc
            # make2 also masks a native timeout emitted after JSON printing.
            # Never claim completed analyzer coverage from that receipt.
            warnings = item["analysis"]["warnings"]
            item["analysis_completion"] = ("unknown-warnings" if warnings else
                                          "accepted-zero-exit" if rc == 0 else
                                          "unknown-make-failure")
            if rc == 0 and not warnings:
                completed.update(expected)
            # Analyzer internal errors can be warnings even with native exit0.
            # Preserve them and fail this diagnostic's completeness receipt.
            final_rc = max(final_rc, 125 if warnings else rc)
            save()
            print("partition: completed " + label + " rc=" + str(rc), flush=True)
        if time.monotonic() >= deadline:
            receipt["status"] = "total-budget-exceeded"
            save()
            return 124
        if final_rc == 0 and completed != ALL:
            raise ValueError("incomplete analyzer union")
        if hashlib.sha256(Path(".golangci.yml").read_bytes()).hexdigest() != receipt["config_sha256"]:
            raise ValueError("configuration changed")
        receipt.update(complete=final_rc == 0, collection_complete=True,
                       completed_checks=sorted(completed),
                       status="diagnostic-complete" if final_rc == 0 else "collection-complete-with-failure",
                       returncode=final_rc)
        save()
        return final_rc
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        receipt.update(status="invalid-or-incomplete", error_type=type(error).__name__, returncode=125)
        save()
        print("partition diagnostic invalid: " + str(error), file=sys.stderr, flush=True)
        return 125


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("diagnostics")
    parser.add_argument("lint_tool")
    args = parser.parse_args()
    sys.exit(run(args.diagnostics, args.lint_tool))
