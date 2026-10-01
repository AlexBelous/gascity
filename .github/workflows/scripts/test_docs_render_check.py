"""Exercise the actual baseline wrapper with primary Mint output and an owned tool."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(os.environ.get("DOCS_CHECK_SCRIPT_UNDER_TEST",
    str(Path(__file__).resolve().parents[3] / ".github/scripts/docs-render-check.sh")))


def report(*links, tree=False):
    marker = "└─" if tree else "\u00a0⎿\u00a0"
    return f"found {len(links)} broken links in 1 files\npage.mdx\n" + "".join(
        f"  {marker} {link}\n" for link in links)


class DocsRenderCheckTests(unittest.TestCase):
    def run_case(self, head, base=None, require_docs_root=False):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        docs = root / "docs"
        docs.mkdir()
        (docs / "docs.json").write_text("{}")
        (docs / "fixture-side").write_text("base")
        env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                   GIT_AUTHOR_NAME="Fixture", GIT_COMMITTER_NAME="Fixture",
                   GIT_AUTHOR_EMAIL="fixture@example.invalid",
                   GIT_COMMITTER_EMAIL="fixture@example.invalid", TMPDIR=str(root))

        def git(*args):
            return subprocess.run(["git", "-c", "core.hooksPath=/dev/null", *args],
                cwd=root, env=env, capture_output=True, text=True, check=True, timeout=5)

        git("init", "--quiet")
        git("add", "docs/docs.json", "docs/fixture-side")
        git("worktree", "list", "--porcelain")
        git("commit", "--quiet", "-m", "fixture baseline", "--", "docs/docs.json", "docs/fixture-side")
        (docs / "fixture-side").write_text("head")
        scenario = root / "scenario.json"
        scenario.write_text(json.dumps({"head": head, "base": base or {"rc": 0, "out": "clean\n"}}))
        tool = root / "fake-mint"
        tool.write_text(f"#!{sys.executable}\n" +
            "import json,os,pathlib,sys\n"
            "cwd=pathlib.Path.cwd()\n"
            "with pathlib.Path(os.environ['MINT_CWD_LOG']).open('a') as log: log.write(str(cwd)+'\\n')\n"
            "if os.environ.get('MINT_EXPECT_DOCS_ROOT')=='1' and not pathlib.Path('docs.json').is_file():\n"
            " print('erro wrong docroot',file=sys.stderr); sys.exit(1)\n"
            "sidefile=pathlib.Path('fixture-side') if pathlib.Path('docs.json').is_file() else pathlib.Path('docs/fixture-side')\n"
            "side=sidefile.read_text()\n"
            "case=json.loads(pathlib.Path(os.environ['MINT_FIXTURE']).read_text())[side]\n"
            "print(case.get('out',''),end='')\n"
            "print(case.get('err',''),end='',file=sys.stderr)\n"
            "sys.exit(case['rc'])\n")
        tool.chmod(0o755)
        diagnostics = root / "diagnostics"
        env.update(MINT_CMD=str(tool), MINT_FIXTURE=str(scenario),
                   DOCS_CHECK_DIAGNOSTICS=str(diagnostics), MINT_CWD_LOG=str(root / "cwd-calls.jsonl"),
                   MINT_EXPECT_DOCS_ROOT="1" if require_docs_root else "0")
        result = subprocess.run(["bash", str(SCRIPT), "HEAD"], cwd=root, env=env,
                                capture_output=True, text=True, timeout=10)
        return result, diagnostics

    def receipt(self, diagnostics):
        return json.loads((diagnostics / "result.json").read_text())

    def test_head_and_baseline_use_the_configured_docs_root(self):
        case = {"rc": 1, "out": report("/same/page")}
        result, diagnostics = self.run_case(case, case, require_docs_root=True)
        calls = (diagnostics.parent / "cwd-calls.jsonl").read_text().splitlines()
        self.assertEqual(calls, [str((diagnostics.parent / "docs").resolve()),
                                 str((diagnostics / "base-docs" / "docs").resolve())])
        self.assertEqual(result.returncode, 0)
        self.assertEqual(self.receipt(diagnostics)["reason"], "baseline-existing")
        self.assertEqual((diagnostics / "head.rc").read_text().strip(), "1")
        self.assertEqual((diagnostics / "base.rc").read_text().strip(), "1")

    def test_real_normal_report_is_not_silently_green(self):
        result, diagnostics = self.run_case({"rc": 1, "out": report("/new/page")})
        self.assertEqual(result.returncode, 1)
        self.assertEqual(self.receipt(diagnostics)["reason"], "net-new-pages")
        self.assertIn("/new/page", result.stdout)

    def test_page_containing_error_is_still_a_completed_report(self):
        result, diagnostics = self.run_case({"rc": 1, "out": report("/new/error-handling")})
        self.assertEqual(result.returncode, 1)
        self.assertEqual(self.receipt(diagnostics)["reason"], "net-new-pages")

    def test_npm_error_is_tool_failure_and_raw_status_survives(self):
        error = "npm error A complete log is in: /home/runner/.npm/_logs/new-debug-0.log\n"
        result, diagnostics = self.run_case({"rc": 17, "out": "banner\n", "err": error})
        self.assertEqual(result.returncode, 125)
        self.assertEqual(self.receipt(diagnostics)["reason"], "tool-error")
        self.assertEqual((diagnostics / "head.rc").read_text().strip(), "17")
        self.assertEqual((diagnostics / "head.stdout").read_text(), "banner\n")
        self.assertEqual((diagnostics / "head.stderr").read_text(), error)
        self.assertNotIn("newly broken", result.stdout)
        self.assertFalse((diagnostics / "base.rc").exists())

    def test_completed_ansi_tree_report_can_match_verified_baseline(self):
        output = "\x1b[31m" + report("/same/page", tree=True) + "\x1b[0m"
        result, diagnostics = self.run_case({"rc": 1, "out": output}, {"rc": 1, "out": output})
        self.assertEqual(result.returncode, 0)
        self.assertEqual(self.receipt(diagnostics)["reason"], "baseline-existing")

    def test_baseline_tool_failure_cannot_be_existing_page_debt(self):
        result, diagnostics = self.run_case({"rc": 1, "out": report("/same/page")},
            {"rc": 127, "err": "npm error failure /tmp/same/page\n"})
        self.assertEqual(result.returncode, 125)
        self.assertEqual(self.receipt(diagnostics)["reason"], "baseline-tool-error")
        self.assertEqual((diagnostics / "base.rc").read_text().strip(), "127")

    def test_only_completed_asset_reports_are_excluded(self):
        result, diagnostics = self.run_case({"rc": 1, "out": report("/logo.svg")})
        self.assertEqual(result.returncode, 0)
        self.assertEqual(self.receipt(diagnostics)["reason"], "completed-assets-only")
        result, diagnostics = self.run_case({"rc": 1, "err": "error cannot open /logo.svg\n"})
        self.assertEqual(result.returncode, 125)
        self.assertEqual(self.receipt(diagnostics)["reason"], "tool-error")

    def test_incomplete_or_unknown_failure_stays_red(self):
        for output in ("found 2 broken links in 1 files\npage.mdx\n  ⎿ /one\n",
                       "found 1 broken links in 1 files\npage.mdx\n  /unmarked\n",
                       "unknown failure /page\n"):
            with self.subTest(output=output):
                result, diagnostics = self.run_case({"rc": 1, "out": output})
                self.assertEqual(result.returncode, 125)
                self.assertEqual(self.receipt(diagnostics)["reason"], "unknown-incomplete")

    def test_explicit_error_overrides_report_or_zero_status(self):
        for rc in (0, 1):
            result, diagnostics = self.run_case({"rc": rc, "out": report("/page"),
                                                 "err": "npm error failure\n"})
            self.assertEqual(result.returncode, 125)
            self.assertEqual(self.receipt(diagnostics)["reason"], "tool-error")

    def test_completed_asset_report_allows_benign_npm_warning(self):
        result, diagnostics = self.run_case({"rc": 1, "out": report("/logo.svg"),
            "err": "npm warn deprecated fixture: harmless warning\n"})
        self.assertEqual(result.returncode, 0)
        self.assertEqual(self.receipt(diagnostics)["reason"], "completed-assets-only")

    def test_verified_baseline_report_allows_benign_npm_warning(self):
        case = {"rc": 1, "out": report("/same/page"),
                "err": "npm WARN deprecated fixture: harmless warning\n"}
        result, diagnostics = self.run_case(case, case)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(self.receipt(diagnostics)["reason"], "baseline-existing")

    def test_unknown_stderr_does_not_make_an_asset_failure_green(self):
        result, diagnostics = self.run_case({"rc": 1, "out": report("/logo.svg"),
            "err": "TypeError: unclassified failure\n    at fixture:10:3\n"})
        self.assertEqual(result.returncode, 125)
        self.assertEqual(self.receipt(diagnostics)["reason"], "unknown-incomplete")


if __name__ == "__main__":
    unittest.main()
