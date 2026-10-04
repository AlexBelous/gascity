#!/usr/bin/env python3
"""Reviewed operator step; read-only by default. Never invoked by ticks/CLI.

The root-owned release manifest supplies approved immutable pins. --publish
requires separate deployment authority; no service is started/restarted here.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

CONFIG = Path('/etc/gascity-observer')
HELPER = Path('/usr/local/libexec/gc-process-observer-helper')
SOCKET = '/run/gascity-observer/observe.sock'
UNIT = 'gascity-supervisor.service'


def sha(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        while chunk := f.read(1024 * 1024):
            h.update(chunk)
    return h.hexdigest()


def trusted(path):
    path = Path(path)
    for p in [path, *path.parents]:
        st = p.lstat()
        if st.st_uid != 0 or st.st_mode & 0o022 or stat.S_ISLNK(st.st_mode):
            raise ValueError('untrusted deployment ownership')
    if not path.is_file():
        raise ValueError('deployment file is not regular')


def render(release, identity, helper_sha):
    expected = {'source_revision', 'controller_binary_sha256', 'controller_executable',
                'controller_uid', 'controller_user', 'controller_gid', 'helper_uid',
                'helper_gid', 'helper_binary_sha256', 'policy_digest', 'pid_namespace_identity'}
    if set(release) != expected:
        raise ValueError('release manifest keys invalid')
    for field, size in [('source_revision', 40), ('controller_binary_sha256', 64),
                        ('helper_binary_sha256', 64), ('policy_digest', 64)]:
        if not re.fullmatch('[0-9a-f]{%d}' % size, release[field]):
            raise ValueError('release hash invalid')
    for field in ['controller_uid', 'controller_gid', 'helper_uid', 'helper_gid']:
        if type(release[field]) is not int or not 0 < release[field] < 2**32:
            raise ValueError('non-root static identities required')
    if release['controller_user'] != 'dev' or release['controller_uid'] != 1000 or release['controller_gid'] != 1000:
        raise ValueError('fixed controller account mismatch')
    if release['helper_uid'] != 62027 or release['helper_gid'] != 62027:
        raise ValueError('fixed proposed observer identity mismatch')
    if release['controller_uid'] == release['helper_uid']:
        raise ValueError('helper UID must be separate')
    if (identity['uid'] != release['controller_uid'] or identity['executable'] != release['controller_executable']
            or identity['binary_sha256'] != release['controller_binary_sha256']
            or identity['namespace'] != release['pid_namespace_identity']
            or helper_sha != release['helper_binary_sha256']):
        raise ValueError('actual release identity mismatch')
    if (type(identity['pid']) is not int or identity['pid'] <= 1 or
            not re.fullmatch('[1-9][0-9]*', identity['start_ticks']) or
            not re.fullmatch('[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}', identity['boot_id']) or
            not re.fullmatch(r'pid:\[[1-9][0-9]*\]', identity['namespace'])):
        raise ValueError('actual process identity invalid')
    caller = {'pid': identity['pid'], 'uid': identity['uid'], 'start_ticks': identity['start_ticks'],
              'boot_id': identity['boot_id'], 'controller_source_revision': release['source_revision'],
              'controller_binary_sha256': release['controller_binary_sha256']}
    binding = {'boot_id': identity['boot_id'], 'controller_pid': identity['pid'],
               'controller_uid': identity['uid'], 'controller_start_ticks': identity['start_ticks'],
               'controller_source_revision': release['source_revision'],
               'controller_binary_sha256': release['controller_binary_sha256'],
               'pid_namespace_identity': identity['namespace'], 'helper_binary_sha256': helper_sha}
    policy = {'socket_path': SOCKET, 'helper_uid': release['helper_uid'],
              'helper_source_revision': release['source_revision'], 'helper_binary_sha256': helper_sha,
              'policy_digest': release['policy_digest'], 'boot_id': identity['boot_id'],
              'pid_namespace_identity': identity['namespace'], 'caller_binding': caller}
    return ''.join(f'{k}={v}\n' for k, v in binding.items()).encode(), (json.dumps(policy, sort_keys=True) + '\n').encode()


def main_pid(release):
    # Fixed unit/verb. Neither a tick nor client can select an arbitrary target.
    result = subprocess.run(['/usr/sbin/runuser', '-u', release['controller_user'], '--',
                             '/usr/bin/env', f'XDG_RUNTIME_DIR=/run/user/{release["controller_uid"]}',
                             '/usr/bin/systemctl', '--user', 'show', UNIT, '-p', 'MainPID', '--value'],
                            check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
    value = result.stdout.decode().strip()
    if not re.fullmatch('[1-9][0-9]*', value) or int(value) <= 1:
        raise ValueError('fixed controller unit not running')
    return int(value)


def identity(pid):
    proc = Path('/proc') / str(pid)
    raw = (proc / 'stat').read_text()
    fields = raw[raw.rfind(')') + 2:].split()
    if len(fields) < 20:
        raise ValueError('process stat incomplete')
    executable = os.readlink(proc / 'exe')
    return {'pid': pid, 'uid': proc.stat().st_uid, 'start_ticks': fields[19],
            'boot_id': Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
            'namespace': os.readlink(proc / 'ns/pid'), 'executable': executable,
            'binary_sha256': sha(proc / 'exe')}


def publish_one(path, data, gid):
    if path.exists():
        trusted(path)
    fd, name = tempfile.mkstemp(prefix='.rebind-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as f:
            os.fchown(f.fileno(), 0, gid)
            os.fchmod(f.fileno(), 0o640)
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(name, path)
        if path.read_bytes() != data:
            raise ValueError('published bytes differ')
    finally:
        if os.path.exists(name):
            os.unlink(name)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--publish', action='store_true', help='separately approved root operator step')
    args = parser.parse_args()
    if os.geteuid() != 0 or os.uname().sysname != 'Linux':
        raise ValueError('fixed Linux root operator required')
    manifest = CONFIG / 'release.json'
    trusted(manifest)
    trusted(HELPER)
    if manifest.stat().st_size > 4096:
        raise ValueError('manifest oversized')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate manifest key')
            result[key] = value
        return result
    release = json.loads(manifest.read_bytes(), object_pairs_hook=unique)
    pid = main_pid(release)
    before = identity(pid)
    if before['namespace'] != os.readlink('/proc/1/ns/pid'):
        raise ValueError('controller is not in approved host PID namespace')
    binding, policy = render(release, before, sha(HELPER))
    if before != identity(pid) or pid != main_pid(release):
        raise ValueError('controller changed during binding proof')
    if args.publish:
        # Binding first: any mixed-generation interval refuses helper evidence.
        # The daemon latches only after exact caller verification and denies a
        # mismatched helper reply. An existing old daemon cannot follow a rebind.
        publish_one(CONFIG / 'binding.conf', binding, release['helper_gid'])
        publish_one(CONFIG / 'client.json', policy, release['controller_gid'])
        fd = os.open(CONFIG, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
        if before != identity(pid) or pid != main_pid(release):
            raise ValueError('controller changed; published binding remains denied, re-review required')
    print(json.dumps({'published': args.publish, 'caller_binding': json.loads(policy)['caller_binding'],
                      'binding_sha256': hashlib.sha256(binding).hexdigest(),
                      'policy_sha256': hashlib.sha256(policy).hexdigest(),
                      'helper_binary_sha256': release['helper_binary_sha256']}))


if __name__ == '__main__':
    main()
