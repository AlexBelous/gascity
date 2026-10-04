#!/usr/bin/env python3
"""Check serialized native producer results against an exact external consumer.

No live runtime or ledger operations. Takes a verbose Go-test log and a pinned
consumer source path; the consumer is neither downloaded nor substituted here.
"""
import argparse
from datetime import datetime
import hashlib
import importlib.util
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--consumer', type=Path, required=True)
    parser.add_argument('--consumer-sha256', required=True)
    parser.add_argument('--test-log', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    actual = hashlib.sha256(args.consumer.read_bytes()).hexdigest()
    if actual != args.consumer_sha256:
        raise SystemExit('consumer source hash mismatch')
    spec = importlib.util.spec_from_file_location('exact_consumer', args.consumer)
    consumer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(consumer)
    source = Path(__file__).resolve().parents[2]
    context = json.loads((source / 'internal/runtime/observation/testdata/consumer-input.json').read_text())
    fixtures = []
    for line in args.test_log.read_text().splitlines():
        if 'NATIVE_CONSUMER_FIXTURE=' in line:
            fixtures.append(json.loads(line.split('NATIVE_CONSUMER_FIXTURE=', 1)[1]))
    expected = {'complete', 'strict-unknown', 'scanner-partial', 'missing-process',
                'alias-conflict', 'wrong-run', 'scannerless'}
    if len(fixtures) != len(expected) or {f['name'] for f in fixtures} != expected:
        raise SystemExit('missing or duplicated native producer fixtures')
    results = []
    for fixture in fixtures:
        result = consumer.reconcile(context['status'], fixture['observation'],
                                    context['identities'], context['routes'],
                                    city_path=context['city_path'],
                                    now=datetime.fromisoformat(context['now'].replace('Z', '+00:00')))
        if fixture['name'] == 'complete':
            assert result['complete'] is True, result
            assert result['count'] == context['expected_count'], result
            assert sorted(row['id'] for row in result['active']) == context['expected_active'], result
        else:
            assert result['complete'] is False and result['count'] is None, result
        assert result['drain_candidates'] == [], result
        results.append({'fixture': fixture, 'consumer_result': result})
    args.output.write_text(json.dumps({'consumer_sha256': actual, 'context': context,
                                      'results': results}, indent=2) + '\n')
    print(f'{len(results)} serialized native producer/consumer checks PASS; no runtime effects')


if __name__ == '__main__':
    main()
