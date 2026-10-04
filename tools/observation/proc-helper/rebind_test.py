#!/usr/bin/env python3
"""Pure restart/rebind fixtures; no /proc, privileges or published files."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('rebind', Path(__file__).with_name('rebind.py'))
binder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(binder)


class RebindTests(unittest.TestCase):
    def fixture(self):
        release = {'source_revision': 'a'*40, 'controller_binary_sha256': 'b'*64,
                   'controller_executable': '/fixture/gc', 'controller_uid': 1000,
                   'controller_user': 'dev', 'controller_gid': 1000,
                   'helper_uid': 62027, 'helper_gid': 62027, 'helper_binary_sha256': 'c'*64,
                   'policy_digest': 'd'*64, 'pid_namespace_identity': 'pid:[123]'}
        identity = {'pid': 42, 'uid': 1000, 'start_ticks': '17',
                    'boot_id': '01234567-0123-0123-0123-0123456789ab',
                    'namespace': 'pid:[123]', 'executable': '/fixture/gc', 'binary_sha256': 'b'*64}
        return release, identity

    def test_exact_and_restart(self):
        release, identity = self.fixture()
        old_binding, old_policy = binder.render(release, identity, 'c'*64)
        new = copy.deepcopy(identity)
        new['pid'] = 43
        new['start_ticks'] = '18'
        binding, policy = binder.render(release, new, 'c'*64)
        self.assertNotEqual(old_binding, binding)
        self.assertNotEqual(old_policy, policy)
        self.assertEqual(json.loads(policy)['caller_binding']['pid'], 43)
        self.assertEqual(json.loads(old_policy)['caller_binding']['pid'], 42)
        self.assertIn(b'controller_pid=43\n', binding)

    def test_mismatches_refuse(self):
        release, identity = self.fixture()
        for key, value in [('uid', 42), ('executable', '/wrong'), ('binary_sha256', 'f'*64),
                           ('namespace', 'pid:[456]'), ('pid', 0), ('start_ticks', '0'),
                           ('boot_id', 'wrong')]:
            with self.subTest(key=key):
                new = dict(identity, **{key: value})
                with self.assertRaises(ValueError):
                    binder.render(release, new, 'c'*64)
        with self.assertRaises(ValueError):
            binder.render(release, identity, 'e'*64)

    def test_separate_uid_and_exact_manifest(self):
        release, identity = self.fixture()
        for new in [dict(release, helper_uid=1000), dict(release, helper_uid=0),
                    dict(release, arbitrary_path='/tmp'), dict(release, source_revision='unknown')]:
            with self.assertRaises(ValueError):
                binder.render(new, identity, 'c'*64)


if __name__ == '__main__':
    unittest.main()
