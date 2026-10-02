#!/usr/bin/env python3
"""Diagnostic-only bounded reader of an exclusive acceptance TMPDIR.

Never writes fixtures, follows symlinks, runs gc/SQL, or changes test outcomes.
Only typed scheduler/event fields and exact FS-pressure lines are exported.
"""
import datetime
import json
import os
import re
import stat
import sys
import time
from pathlib import Path

READ_CAP = 64 << 20
FILE_CAP = 16 << 20
CHUNK = 2 << 20
TRACE_KEYS = ('seq', 'trace_id', 'tick_id', 'record_type', 'ts', 'tick_trigger',
              'site_code', 'reason_code', 'outcome_code', 'completion_status',
              'duration_ms', 'cycle_offset_ms', 'controller_pid',
              'controller_started_at', 'record_count', 'dropped_record_count', 'dropped_batch_count')
FIELD_KEYS = ('operation_name', 'phase', 'reason', 'trigger', 'avg60', 'threshold',
              'consecutive_skips', 'max_consecutive_skips', 'outcome')
FS_LINE = re.compile(r'supervisor: FS pressure high \(some avg60=[0-9.]+ > threshold=[0-9.]+\), (?:skipping order dispatch|forcing order dispatch after [0-9]+ skipped passes)$')


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def open_owned(root_fd, rel):
    parts = Path(rel).parts
    if not parts or any(p in ('..', '.', '/') for p in parts):
        raise ValueError('unsafe relative path')
    parent = os.dup(root_fd)
    try:
        for part in parts[:-1]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
            os.close(parent)
            parent = child
        fd = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            os.close(fd)
            raise ValueError('not regular')
        return fd
    finally:
        os.close(parent)


def names(root_fd, rel='', directories=True):
    fd = os.dup(root_fd)
    try:
        for part in Path(rel).parts:
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = nxt
        with os.scandir(fd) as entries:
            result = []
            for n, entry in enumerate(entries):
                if n >= 256:
                    raise ValueError('directory entry cap')
                if (entry.is_dir(follow_symlinks=False) if directories else entry.is_file(follow_symlinks=False)):
                    result.append(entry.name)
        return sorted(result)
    finally:
        os.close(fd)


def dirs(root_fd, rel=''):
    return names(root_fd, rel, True)


def candidates(root_fd, gaps=None):
    result = []
    for base in dirs(root_fd):
        if not base.startswith('gc-acceptance-'):
            continue
        result.append((base + '/gc-home/supervisor.log', 'log'))
        try:
            cities = dirs(root_fd, base)
        except (FileNotFoundError, NotADirectoryError):
            if gaps is not None and len(gaps) < 100:
                gaps.add(base + ": disappeared during discovery")
            continue
        for city in cities:
            if not city.startswith('at-'):
                continue
            prefix = base + '/' + city
            result.extend([(prefix + '/.gc/events.jsonl', 'event'),
                           (prefix + '/.gc/runtime/events.jsonl', 'event')])
            trace = prefix + '/.gc/runtime/session-reconciler-trace/segments'
            try:
                for year in dirs(root_fd, trace):
                    if not re.fullmatch(r'[0-9]{4}', year):
                        continue
                    for month in dirs(root_fd, trace + '/' + year):
                        if not re.fullmatch(r'[0-9]{2}', month):
                            continue
                        for day in dirs(root_fd, trace + '/' + year + '/' + month):
                            if not re.fullmatch(r'[0-9]{2}', day):
                                continue
                            folder = trace + '/' + year + '/' + month + '/' + day
                            for segment in names(root_fd, folder, False):
                                if re.fullmatch(r'segment-[0-9]{6}\.jsonl', segment):
                                    result.append((folder + '/' + segment, 'trace'))
            except (FileNotFoundError, NotADirectoryError):
                if gaps is not None and len(gaps) < 100:
                    gaps.add(trace + ": missing during discovery")
    if len(result) > 128:
        raise ValueError('candidate cap')
    return result


def project(line, kind):
    if kind == 'log':
        match = FS_LINE.search(line)
        return {'fs_pressure_line': match.group(0)} if match else None
    obj = json.loads(line)
    if not isinstance(obj, dict):
        return None
    if kind == 'event':
        if obj.get('type') not in ('order.fired', 'order.completed', 'order.failed',
                                   'order.skipped', 'order.suppressed', 'controller.started', 'controller.stopped'):
            return None
        return {k: obj[k] for k in ('seq', 'ts', 'type', 'subject') if k in obj}
    if obj.get('tick_trigger') != 'orders':
        return None
    out = {k: obj[k] for k in TRACE_KEYS if k in obj and isinstance(obj[k], (str, int, float, bool))}
    fields = obj.get('fields', {})
    if isinstance(fields, dict):
        out['fields'] = {k: fields[k] for k in FIELD_KEYS if k in fields and isinstance(fields[k], (str, int, float, bool))}
    return out


def main():
    root, output = map(Path, sys.argv[1:])
    output.mkdir(exist_ok=False)
    root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    state, read_bytes, records, errors, limits, gaps = {}, 0, 0, [], set(), set()
    started = time.monotonic()
    (output / 'ready').write_text(utc() + '\n')
    try:
        with (output / 'scheduler-evidence.jsonl').open('w') as evidence, (output / 'host.jsonl').open('w') as host:
            while time.monotonic() - started < 2100:
                host.write(json.dumps({'utc': utc(), 'cpu_count': os.cpu_count(), 'load': os.getloadavg(),
                                       'io_psi': Path('/proc/pressure/io').read_text(),
                                       'cpu_psi': Path('/proc/pressure/cpu').read_text(),
                                       'mem_available_kib': next(int(line.split()[1]) for line in Path('/proc/meminfo').read_text().splitlines() if line.startswith('MemAvailable:')),
                                       'memory_psi': Path('/proc/pressure/memory').read_text()}) + '\n')
                host.flush()
                for rel, kind in candidates(root_fd, gaps):
                    try:
                        fd = open_owned(root_fd, rel)
                        with os.fdopen(fd, 'rb') as source:
                            info = os.fstat(source.fileno())
                            key = (rel, info.st_dev, info.st_ino)
                            offset, pending = state.get(key, (0, b''))
                            if info.st_size < offset:
                                offset, pending = 0, b''
                            size = min(CHUNK, FILE_CAP - offset, READ_CAP - read_bytes)
                            if size <= 0:
                                if info.st_size > offset:
                                    limits.add('read cap reached: ' + rel)
                                continue
                            source.seek(offset)
                            data = source.read(size)
                            read_bytes += len(data)
                            lines = (pending + data).split(b'\n')
                            state[key] = (offset + len(data), lines.pop())
                            for line in lines:
                                try:
                                    value = project(line.decode('utf-8'), kind)
                                    if value is not None:
                                        evidence.write(json.dumps({'observed_at': utc(), 'source': rel, 'inode': info.st_ino, 'record': value}) + '\n')
                                        records += 1
                                except (ValueError, UnicodeError):
                                    if len(errors) < 100:
                                        errors.append({'source': rel, 'error': 'unparseable complete line'})
                    except FileNotFoundError:
                        continue
                    except (OSError, ValueError) as exc:
                        if len(errors) < 100:
                            errors.append({'source': rel, 'error': str(exc)})
                evidence.flush()
                if (output.parent / 'observer-stop').exists():
                    break
                time.sleep(2)
    finally:
        os.close(root_fd)
        receipt = {'utc': utc(), 'root': str(root), 'elapsed_seconds': time.monotonic() - started,
                   'bytes_read': read_bytes, 'records_exported': records,
                   'read_cap': READ_CAP, 'file_cap': FILE_CAP, 'errors': errors[:100], 'limits': sorted(limits), 'discovery_gaps': sorted(gaps),
                   'files': [{'source': k[0], 'inode': k[2], 'offset': v[0], 'pending_bytes': len(v[1])} for k, v in state.items()],
                   'stopped_by_owner': (output.parent / 'observer-stop').exists(),
                   'limitation': 'Orders cycles are buffered until End; no proof of current in-flight phase. Typed projection omits arbitrary payloads/messages/env/argv and free-form trace degradation logs/drop-reason details; absence of degradation is not proved.'}
        (output / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    if errors or limits:
        raise SystemExit('observer coverage incomplete; see receipt')
    if not receipt['stopped_by_owner']:
        raise SystemExit('observer lifetime expired before owner completed')


if __name__ == '__main__':
    main()
