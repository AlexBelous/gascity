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
                   'policy_digest': 'd'*64, 'pid_namespace_identity': 'pid:[123]',
                   'kernel_release':'6.8.0-fixture','kernel_proof_profile':'linux6.8-pidfd-flags0-no-esrch-filters/v1'}
        identity = {'pid': 42, 'uid': 1000, 'start_ticks': '17',
                    'boot_id': '01234567-0123-0123-0123-0123456789ab',
                    'namespace': 'pid:[123]', 'executable': '/fixture/gc', 'binary_sha256': 'b'*64,'kernel_release':'6.8.0-fixture'}
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
                           ('boot_id', 'wrong'),('kernel_release','6.9.0-wrong')]:
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

    def test_kernel_profile_fields_required(self):
        release, identity = self.fixture()
        for key in ['kernel_release', 'kernel_proof_profile']:
            with self.subTest(key=key):
                new = dict(release)
                del new[key]
                with self.assertRaises(ValueError):
                    binder.render(new, identity, 'c'*64)

    def test_unknown_kernel_profile_refused(self):
        release, identity = self.fixture()
        with self.assertRaises(ValueError):
            binder.render(dict(release, kernel_proof_profile='linux6.8-unknown/v1'), identity, 'c'*64)

    def test_kernel_field_types_refused(self):
        release, identity = self.fixture()
        for key in ['kernel_release', 'kernel_proof_profile']:
            for value in [None, 68, True, [], {}]:
                with self.subTest(key=key, value=value):
                    with self.assertRaises(ValueError):
                        binder.render(dict(release, **{key: value}), identity, 'c'*64)
        for value in [None, 68, True, [], {}]:
            with self.subTest(identity=value):
                with self.assertRaises(ValueError):
                    binder.render(release, dict(identity, kernel_release=value), 'c'*64)

    def test_exact_v3_selector_is_rendered(self):
        release, identity = self.fixture()
        release['evidence_schema'] = 'host-process-evidence/v3'
        binding, policy = binder.render(release, identity, 'c'*64)
        self.assertEqual(json.loads(policy)['evidence_schema'], 'host-process-evidence/v3')
        self.assertEqual(json.loads(policy)['caller_binding']['pid'], identity['pid'])
        self.assertIn(b'controller_source_revision=' + b'a'*40 + b'\n', binding)

    def test_v3_selector_cannot_bypass_release_proof(self):
        release, identity = self.fixture()
        release['evidence_schema'] = 'host-process-evidence/v3'
        for key, value in [('uid', 42), ('executable', '/wrong'), ('binary_sha256', 'f'*64),
                           ('namespace', 'pid:[456]'), ('start_ticks', '0')]:
            with self.subTest(key=key):
                with self.assertRaises(ValueError):
                    binder.render(release, dict(identity, **{key: value}), 'c'*64)
        with self.assertRaises(ValueError):
            binder.render(release, identity, 'e'*64)

    def test_unknown_or_nonstring_selector_refused(self):
        release, identity = self.fixture()
        for value in ['host-process-evidence/v2', 'host-process-evidence/v4', '', None, True, 3]:
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    binder.render(dict(release, evidence_schema=value), identity, 'c'*64)

    def test_v3_restart_rebind_preserves_exact_selector(self):
        release, identity = self.fixture()
        release['evidence_schema'] = 'host-process-evidence/v3'
        _, old_policy = binder.render(release, identity, 'c'*64)
        _, new_policy = binder.render(release, dict(identity, pid=43, start_ticks='18'), 'c'*64)
        old, new = json.loads(old_policy), json.loads(new_policy)
        self.assertEqual(old['evidence_schema'], new['evidence_schema'])
        self.assertNotEqual(old['caller_binding'], new['caller_binding'])
        self.assertEqual(new['caller_binding']['pid'], 43)

    def test_legacy_manifest_does_not_grant_v3_selector(self):
        release, identity = self.fixture()
        _, policy = binder.render(release, identity, 'c'*64)
        self.assertNotIn('evidence_schema', json.loads(policy))


if __name__ == '__main__':
    unittest.main()
