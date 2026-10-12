"""Persist shared reader negatives from actual controlled M/R producer raws."""
import copy
import json
from pathlib import Path
import sys
mixed, normalized, target = map(Path, sys.argv[1:])
x = json.loads(mixed.read_text())
x['errors_truncated'] = False
cases = [{'name': 'mixed-real-kernel-M-Mbirth-R', 'case': x, 'want': True}]
y = json.loads(normalized.read_text()); y['errors_truncated'] = False
cases.append({'name': 'real-kernel-normalized-closing5-to-seal7', 'case': y, 'want': True})

def add(name, f):
    v = copy.deepcopy(x); f(v); cases.append({'name': name, 'case': v, 'want': False})

def link(v, kind='deferred_managed_retirement'):
    return next(r for r in v['census']['resolutions'] if r['kind'] == kind)

for kind in ('deferred_managed_retirement', 'managed_birth_retirement', 'direct_reaped_birth'):
    for field, value in (('terminal_fdinfo_pid', 1), ('terminal_poll', 'refused_flags'),
                         ('same_original_descriptors', False), ('original_pidfd_inode', 0),
                         ('original_procdir_inode', 0), ('bound_start_ticks', '1'),
                         ('terminal_offset_ms', 10000), ('missing_scan', 10), ('capture_handle', 0),
                         ('original_pidfd_generation', 0), ('original_procdir_generation', 0)):
        add(kind + '/' + field, lambda v, k=kind, f=field, val=value: link(v, k)['original_capture'].__setitem__(f, val))
    add(kind + '/missing-custody', lambda v, k=kind: link(v, k).pop('original_capture'))
    add(kind + '/root-uid', lambda v, k=kind: link(v, k)['original_capture']['original_uids'].__setitem__(0, 0))
add('second-pass-ENV', lambda v: v['errors'][0]['capture_diagnostic'].__setitem__('fields_pass', 2))
add('rewrite-original-poll', lambda v: v['errors'][0]['capture_diagnostic'].__setitem__('exit_poll', 'valid_pollin'))
add('rewrite-raw-witness', lambda v: v['errors'][0]['capture_diagnostic'].__setitem__('exit_witness', True))
add('duplicate-resolution-index', lambda v: v['census']['resolutions'][1].__setitem__('error_index', v['census']['resolutions'][0]['error_index']))
add('incomplete-numeric-membership', lambda v: v['census']['scans'][2].pop('numeric_pids'))
add('wrong-numeric-digest', lambda v: v['census']['scans'][2].__setitem__('enumeration_digest', '0' * 64))
add('FD33', lambda v: v['census'].__setitem__('peak_fd', 33))
add('memory-over-limit', lambda v: v['census'].__setitem__('retained_bytes', 16777217))
add('truncated-input', lambda v: v.__setitem__('errors_truncated', True))
add('missing-managed-birth-index', lambda v: v['census']['resolutions'].remove(link(v, 'managed_birth_retirement')))
add('changed-managed-parent', lambda v: v['census']['certificates'][0]['chain'][0].__setitem__('ppid', 1))
add('changed-managed-SID', lambda v: v['census']['certificates'][0]['chain'][0].__setitem__('session_id', 'other'))
add('changed-root-incarnation', lambda v: v['census']['certificates'][0]['chain'][-1].__setitem__('start_ticks', '1'))
# Keep the legacy certificate's actual same-scan fault, never a backdated birth fault.
def older_legacy_fault(v):
    r = next(r for r in v['census']['resolutions'] if r['kind'] == 'kernel_absence')
    v['errors'][r['error_index'] - 1]['scan_index'] -= 1
add('legacy-own-fault-retimed', older_legacy_fault)
add('unrelated-custody', lambda v: next(r for r in v['census']['resolutions'] if r['kind'] == 'kernel_absence').__setitem__('original_capture', copy.deepcopy(link(v)['original_capture'])))
add('M-current-comm-not-verified', lambda v: link(v)['original_capture'].__setitem__('comm_verified_before_first_environ', False))
for kind in ('managed_birth_retirement', 'direct_reaped_birth'):
    add(kind + '/false-postreap-comm-claim', lambda v, k=kind: link(v, k)['original_capture'].__setitem__('comm_verified_before_first_environ', True))
add('duplicate-original-handle', lambda v: link(v, 'direct_reaped_birth')['original_capture'].__setitem__('capture_handle', link(v)['original_capture']['capture_handle']))
target.write_text(json.dumps(cases, indent=2) + '\n')
print(json.dumps({'cases': len(cases), 'positive': 2, 'negative': len(cases) - 2}))

# These are the exact new mandatory original-record/comm predicates.
