#!/usr/bin/env python3
"""Verify the reviewed V3 build recipe without compiling or running a helper."""
from pathlib import Path
import subprocess
import unittest


class BuildV3Tests(unittest.TestCase):
    def test_v3_recipe_has_exact_entry_pin_and_hardening(self):
        result = subprocess.run(
            ['make', '-n', 'build-v3', 'SOURCE_REVISION=' + 'a'*40,
             'OUT=/fixture-source-only/helper-v3'],
            cwd=Path(__file__).parent, text=True, capture_output=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('observer-v3.c', result.stdout)
        self.assertIn('HELPER_SOURCE_REVISION=', result.stdout)
        self.assertIn('a'*40, result.stdout)
        self.assertIn('-static', result.stdout)
        self.assertIn('-fstack-protector-strong', result.stdout)
        self.assertIn('-D_FORTIFY_SOURCE=2', result.stdout)
        self.assertIn('-Wl,-z,relro,-z,now,-z,noexecstack', result.stdout)
        self.assertNotIn('-DGC_HELPER_TEST', result.stdout)
        self.assertNotIn(' observer.c ', result.stdout)

    def test_legacy_recipe_keeps_its_separate_entry(self):
        result = subprocess.run(
            ['make', '-n', 'build', 'SOURCE_REVISION=' + 'a'*40],
            cwd=Path(__file__).parent, text=True, capture_output=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(' observer.c ', result.stdout)
        self.assertNotIn('observer-v3.c', result.stdout)


if __name__ == '__main__':
    unittest.main()
