"""Controlled own-process M/R proof suite; no helper RPC or production scan.

Run on Linux as an unprivileged owner. Optional frozen67 baseline directory
contains only archived helper sources; no checkout, branch or runtime build.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess

p = argparse.ArgumentParser()
p.add_argument('--out', type=Path, required=True)
p.add_argument('--baseline', type=Path)
a = p.parse_args()
a.out.mkdir(parents=True, exist_ok=True)
source = Path(__file__).parent.resolve()
receipt = {'scope': 'controlled own-process C fixtures only; synthetic ENV/provider', 'commands': [], 'hashes': {}}

def run(cmd, name):
    r = subprocess.run([str(x) for x in cmd], capture_output=True, timeout=30)
    (a.out / (name + '.stdout')).write_bytes(r.stdout)
    (a.out / (name + '.stderr')).write_bytes(r.stderr)
    receipt['commands'].append({'name': name, 'command': [str(x) for x in cmd], 'rc': r.returncode})
    if r.returncode:
        raise RuntimeError(name + ': ' + r.stderr.decode(errors='replace')[-2000:])

try:
    if a.baseline:
        # Caller provides a private immutable archive, never a working checkout.
        shutil.copyfile(source / 'census-mr-kernel-test.c', a.baseline / 'census-mr-kernel-test.c')
        run(['cc', '-DGC_MR_BASELINE', '-O0', '-std=c11', '-Wall', '-Wextra', '-Werror', a.baseline / 'census-mr-kernel-test.c', '-o', a.out / 'baseline-test'], 'compile-baseline')
        run([a.out / 'baseline-test'], 'baseline-RED')
    run(['cc', '-O0', '-std=c11', '-Wall', '-Wextra', '-Werror', source / 'census-mr-kernel-test.c', '-o', a.out / 'mr-test'], 'compile-candidate')
    run([a.out / 'mr-test'], 'mixed-GREEN')
    run([a.out / 'mr-test', 'normalization'], 'normalization-GREEN')
    for case in ('wrongfd', 'wrongdir', 'live', 'binding', 'uid', 'secondpass', 'rootloss', 'deadline', 'budget', 'numeric', 'r-wrongfd', 'r-wrongdir', 'r-uid', 'r-reuse', 'r-protected', 'comm-before-env', 'late9'):
        run([a.out / 'mr-test', 'negative-' + case], 'negative-' + case)
    run(['cc', '-O0', '-std=c11', '-Wall', '-Wextra', '-Werror', source / 'census-original-custody-kernel-test.c', '-o', a.out / 'custody-test'], 'compile-strong-custody')
    run([a.out / 'custody-test'], 'OTHER-REAPED-ABA-resource')
    run(['cc', '-O0', '-std=c11', '-Wall', '-Wextra', '-Werror', source / 'census-exit-window-kernel-test.c', '-o', a.out / 'legacy-test'], 'compile-legacy')
    for mode in ('automatic', 'normalization'):
        run([a.out / 'legacy-test', mode], 'legacy-ABCD-' + mode)
    receipt['pass'] = True
finally:
    for f in sorted(a.out.glob('*.std*')):
        receipt['hashes'][f.name] = {'sha256': hashlib.sha256(f.read_bytes()).hexdigest(), 'bytes': f.stat().st_size}
    receipt['source_hashes'] = {f.name: hashlib.sha256(f.read_bytes()).hexdigest() for f in sorted(source.iterdir()) if f.is_file() and f.suffix in ('.c', '.h', '.py')}
    (a.out / 'RECEIPT.json').write_text(json.dumps(receipt, indent=2) + '\n')
print(json.dumps({'pass': receipt['pass'], 'commands': len(receipt['commands'])}))
