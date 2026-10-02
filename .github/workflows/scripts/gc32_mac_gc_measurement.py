#!/usr/bin/env python3
"""Diagnostic-only GC trace setup with exact Go child environment fidelity."""
import fcntl
import hashlib
import json
import os
import shlex
import shutil
import stat
import subprocess
import sys
from pathlib import Path


def child_debug(original):
    if original is None:
        return None
    return ','.join(part for part in original.split(',') if part.partition('=')[0] != 'gctrace')


def record_count(path):
    fd = os.open(path, os.O_RDWR | os.O_NOFOLLOW)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise ValueError('counter is not a regular file')
        fcntl.flock(fd, fcntl.LOCK_EX)
        raw = os.read(fd, 32)
        if not raw.strip().isdigit() or len(raw) > 8:
            raise ValueError('invalid counter')
        count = int(raw)
        if count >= 65536:
            raise ValueError('Go observation call cap reached')
        os.lseek(fd, 0, os.SEEK_SET)
        os.write(fd, str(count + 1).encode() + b'\n')
        os.ftruncate(fd, len(str(count + 1)) + 1)
    finally:
        os.close(fd)


def create_shim(diagnostics, real_go, environment):
    diagnostics = Path(diagnostics).resolve(strict=True)
    real_go = Path(real_go).resolve(strict=True)
    shim_dir = diagnostics / 'go-shim'
    shim_dir.mkdir(mode=0o700)
    count = diagnostics / 'go-shim-call-count.txt'
    count.write_text('0\n')
    count.chmod(0o600)
    # Shell exec preserves signal dispositions and fds; telemetry reads no stdin.
    script = ('#!/bin/sh\nset -eu\n' + shlex.quote(sys.executable)
              + ' -S ' + shlex.quote(str(Path(__file__).resolve()))
              + ' record-count ' + shlex.quote(str(count)) + '\n'
              + 'if [ "$GC32_CHILD_GO_DEBUG_PRESENT" = 1 ]; then\n'
              + '  GODEBUG="$GC32_CHILD_GO_DEBUG"; export GODEBUG\n'
              + 'else\n  unset GODEBUG\nfi\n'
              + 'exec ' + shlex.quote(str(real_go)) + ' "$@"\n')
    shim = shim_dir / 'go'
    shim.write_text(script)
    shim.chmod(0o700)
    env = dict(environment)
    original = env.get('GODEBUG')
    child = child_debug(original)
    env['GC32_CHILD_GO_DEBUG_PRESENT'] = '0' if child is None else '1'
    env['GC32_CHILD_GO_DEBUG'] = child or ''
    env['GODEBUG'] = (original + ',' if original else '') + 'gctrace=1'
    env['PATH'] = str(shim_dir) + os.pathsep + env['PATH']
    receipt = {'real_go': str(real_go), 'real_go_sha256': hashlib.sha256(real_go.read_bytes()).hexdigest(),
               'shim_sha256': hashlib.sha256(shim.read_bytes()).hexdigest(),
               'original_debug_present': original is not None,
               'original_debug_sha256': hashlib.sha256((original or '').encode()).hexdigest(),
               'child_debug_sha256': hashlib.sha256((child or '').encode()).hexdigest(),
               'parent_debug_sha256': hashlib.sha256(env['GODEBUG'].encode()).hexdigest(),
               'call_count_cap': 65536, 'counter_contains': 'one locked integer; no arguments/environment values',
               'signal_boundary': 'Shell exec passes dispositions/fds to real Go; extra bounded Python counter process before exec adds observational startup overhead.',
               'attribution_limit': 'Known active PATH Go launch paths intercepted; no universal subprocess sandbox claim. Unexpected trace resets or version/driver evidence invalidate parent-only interpretation.'}
    (diagnostics / 'gc-measurement-setup.json').write_text(json.dumps(receipt, indent=2) + '\n')
    return env


def preflight(real_go, linter, environment, diagnostics):
    driver = environment.get('GOPACKAGESDRIVER', '')
    if driver not in ('', 'off') or shutil.which('gopackagesdriver', path=environment['PATH']):
        raise ValueError('external packages driver invalidates attribution')
    real_go = Path(real_go).resolve(strict=True)
    go_env = json.loads(subprocess.check_output([str(real_go), 'env', '-json', 'GOVERSION', 'GOROOT', 'GOTOOLCHAIN'], env=environment, timeout=30))
    if go_env['GOVERSION'] != 'go1.26.6':
        raise ValueError('unexpected effective Go version')
    if real_go != (Path(go_env['GOROOT']) / 'bin/go').resolve(strict=True):
        raise ValueError('real Go does not match effective GOROOT')
    metadata = subprocess.check_output([str(real_go), 'version', '-m', linter], env=environment, text=True, timeout=30)
    if ('go1.26.6' not in metadata.splitlines()[0]
            or '\tdep\tgolang.org/x/tools\tv0.44.0\t' not in metadata
            or '\tdep\tgithub.com/ldez/grignotin\tv0.10.1\t' not in metadata):
        raise ValueError('unexpected linter build/dependency fingerprint')
    version = subprocess.check_output([linter, 'version'], env=environment, text=True, timeout=30)
    if 'version 2.12.0 built with go1.26.6' not in version:
        raise ValueError('unexpected linter version')
    Path(diagnostics, 'linter-build-metadata.txt').write_text(metadata)
    Path(diagnostics, 'real-go-environment.json').write_text(json.dumps(go_env, indent=2) + '\n')


def main():
    if sys.argv[1] == 'record-count':
        record_count(sys.argv[2])
        return
    if sys.argv[1] != 'prepare-run' or len(sys.argv) < 7 or sys.argv[5] != '--':
        raise ValueError('expected prepare-run DIAGNOSTICS REAL_GO LINTER -- COMMAND ...')
    diagnostics, real_go, linter = sys.argv[2:5]
    preflight(real_go, linter, os.environ, diagnostics)
    env = create_shim(diagnostics, real_go, os.environ)
    print("GC32_PARENT_TRACE_BEGIN: child gctrace excluded on reviewed PATH launch paths", file=sys.stderr, flush=True)
    os.execvpe(sys.argv[6], sys.argv[6:], env)


if __name__ == '__main__':
    main()
