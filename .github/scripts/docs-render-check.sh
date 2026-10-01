#!/usr/bin/env bash
# Baseline-aware Mint check. Only complete link reports qualify for asset/baseline
# exclusions. Tool/unknown failures return125; real new page links return1.
# Raw tool statuses and output are retained in an owned diagnostic directory.
set -euo pipefail
export GC_DOCS_BASE_REF="${1:-origin/main}"
exec python3 -S - <<'PY'
import datetime
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tarfile
import tempfile
import time

started = time.monotonic()
provided = os.environ.get("DOCS_CHECK_DIAGNOSTICS")
try:
    if provided:
        diagnostics = Path(provided)
        diagnostics.mkdir(parents=True, exist_ok=False)
    else:
        diagnostics = Path(tempfile.mkdtemp(prefix="gascity-docs-check-"))
except OSError as error:
    print(f"docs-render-check: refusing unavailable/reused diagnostics ({type(error).__name__})", file=sys.stderr)
    sys.exit(125)

print(f"docs-render-check: diagnostics={diagnostics}", file=sys.stderr)
base_ref = os.environ["GC_DOCS_BASE_REF"]
receipt = {"started_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
           "base_ref": base_ref, "resolved_mint_version": "unknown", "head_rc": None,
           "base_rc": None, "wrapper_rc": 125, "reason": "starting"}


def save():
    receipt["elapsed_seconds"] = time.monotonic() - started
    (diagnostics / "result.json").write_text(json.dumps(receipt, indent=2) + "\n")


def sha(ref):
    result = subprocess.run(["git", "rev-parse", "--verify", ref],
                            capture_output=True, text=True)
    return result.stdout.strip() if result.returncode == 0 else None


ansi = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")
header = re.compile(r"found (\d+) broken links in (\d+) files")
fatal = re.compile(r"^(?:npm (?:ERR!|error)(?:\s|$)|error(?:\s|:))", re.IGNORECASE)
assets = re.compile(r"\.(?:png|svg|jpg|jpeg|gif|ico|webp|woff2?|ttf|eot)$")


def classify(rc, stdout, stderr):
    lines = ansi.sub("", stdout).replace("\u00a0", " ").splitlines()
    errors = ansi.sub("", stderr).replace("\u00a0", " ").splitlines()
    if any(fatal.match(line.strip()) for line in lines + errors):
        return "tool-error", []
    headers = [(i, header.fullmatch(line.strip())) for i, line in enumerate(lines)]
    headers = [(i, match) for i, match in headers if match]
    if rc == 0:
        return ("unknown-incomplete", []) if headers else ("clean", [])
    if rc != 1:
        return "tool-error", []
    # npm's benign installation warnings are not BrokenLinksLog file headings.
    # Other stderr on a nonzero run is unclassified, never an asset/baseline PASS.
    if any(line.strip() and not re.match(r"^npm warn\s+", line.strip(), re.IGNORECASE)
           for line in errors):
        return "unknown-incomplete", []
    if len(headers) != 1:
        return "unknown-incomplete", []
    index, match = headers[0]
    expected_rows, expected_files = map(int, match.groups())
    files, links = [], []
    current_file_rows = 0
    for line in lines[index + 1:]:
        value = line.strip()
        if not value:
            continue
        row = re.fullmatch(r"(?:⎿|├─|└─)\s+(.+)", value)
        if row:
            if not files:
                return "unknown-incomplete", []
            links.append(row[1])
            current_file_rows += 1
        else:
            if files and current_file_rows == 0:
                return "unknown-incomplete", []
            # File headings are emitted without indentation by BrokenLinksLog.
            if line != line.lstrip():
                return "unknown-incomplete", []
            files.append(value)
            current_file_rows = 0
    if (expected_rows < 1 or expected_files < 1 or current_file_rows < 1
            or len(links) != expected_rows or len(files) != expected_files
            or len(set(files)) != expected_files):
        return "unknown-incomplete", []
    return "completed-report", sorted({link for link in links if not assets.search(link)})


def mint(root, label):
    out = diagnostics / f"{label}.stdout"
    err = diagnostics / f"{label}.stderr"
    command = shlex.split(os.environ.get("MINT_CMD", "npx --yes mint@latest"))
    with out.open("w") as stdout, err.open("w") as stderr:
        try:
            if not command:
                raise ValueError("empty Mint command")
            rc = subprocess.run(command + ["broken-links"], cwd=root,
                                stdout=stdout, stderr=stderr).returncode
        except (OSError, ValueError) as error:
            stderr.write(f"error {type(error).__name__} invoking Mint\n")
            rc = 127 if isinstance(error, FileNotFoundError) else 126
    (diagnostics / f"{label}.rc").write_text(f"{rc}\n")
    receipt[f"{label}_rc"] = rc
    kind, links = classify(rc, out.read_text(encoding="utf-8"), err.read_text(encoding="utf-8"))
    receipt[f"{label}_classification"] = kind
    (diagnostics / f"{label}.links.json").write_text(json.dumps(links) + "\n")
    save()
    return kind, links


def check():
    receipt.update(head_sha=sha("HEAD"), base_sha=sha(base_ref))
    save()
    if not Path("docs/docs.json").is_file():
        return 0, "no-docs"
    kind, head_links = mint(Path.cwd() / "docs", "head")
    if kind not in ("clean", "completed-report"):
        return 125, kind
    if kind == "clean":
        return 0, "clean"
    if not head_links:
        return 0, "completed-assets-only"

    archive = diagnostics / "base-docs.tar"
    with archive.open("wb") as out, (diagnostics / "archive.stderr").open("wb") as err:
        archive_rc = subprocess.run(["git", "archive", base_ref, "--", "docs"],
                                    stdout=out, stderr=err).returncode
    receipt["archive_rc"] = archive_rc
    new_links = head_links
    if archive_rc == 0:
        root = diagnostics / "base-docs"
        root.mkdir()
        with tarfile.open(archive) as tree:
            tree.extractall(root, filter="data")
        kind, base_links = mint(root / "docs", "base")
        if kind not in ("clean", "completed-report"):
            return 125, "baseline-" + kind
        new_links = sorted(set(head_links) - set(base_links))
        if not new_links:
            return 0, "baseline-existing"
    receipt["page_links"] = new_links
    print("docs-render-check: HEAD page links requiring attention:")
    for link in new_links:
        print(f"  {link}")
    if archive_rc != 0:
        print("docs-render-check: baseline unavailable; these links were not verified as net-new", file=sys.stderr)
        return 1, "baseline-unavailable"
    return 1, "net-new-pages"


try:
    status, reason = check()
except (OSError, ValueError, tarfile.TarError, subprocess.SubprocessError) as error:
    receipt["wrapper_error"] = type(error).__name__
    status, reason = 125, "wrapper-error"
receipt.update(wrapper_rc=status, reason=reason)
save()
print(f"docs-render-check: {reason} (rc{status})", file=sys.stderr)
sys.exit(status)
PY
