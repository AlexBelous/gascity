import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import type { RunDiffResponse } from 'gas-city-dashboard-shared';
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest';
import { invalidate } from '../api/cache';
import { api } from '../api/client';
import { reportClientError } from '../lib/clientErrorReporting';
import { useRunDiff } from './useRunDiff';

vi.mock('../api/client', () => ({
  api: {
    runDiff: vi.fn(),
  },
}));

vi.mock('../lib/clientErrorReporting', () => ({
  reportClientError: vi.fn(() => Promise.resolve({ status: 'reported' })),
}));

const mockRunDiff = api.runDiff as Mock;
const mockReportClientError = reportClientError as Mock;

afterEach(() => {
  cleanup();
  invalidate('');
  vi.clearAllMocks();
});

describe('useRunDiff', () => {
  it('does not fetch or report when no run id is available', async () => {
    const { result } = renderHook(() => useRunDiff(undefined));

    await waitFor(() => expect(result.current.kind).toBe('idle'));

    expect(mockRunDiff).not.toHaveBeenCalled();
    expect(mockReportClientError).not.toHaveBeenCalled();
  });

  it('requests the diff by run id with NO execution path (the server resolves it)', async () => {
    const diff = okDiff();
    mockRunDiff.mockResolvedValue(diff);

    const { result } = renderHook(() => useRunDiff('wf-1'));

    await waitFor(() => expect(result.current.kind).toBe('ready'));

    if (result.current.kind !== 'ready') throw new Error('diff did not load');
    expect(result.current.diff).toBe(diff);
    expect(result.current.refreshState).toEqual({ kind: 'idle' });
    // The body carries no executionPath — just an empty object.
    expect(mockRunDiff).toHaveBeenCalledWith('wf-1', {}, {});
  });

  it('renders the server 4xx/403 message verbatim on the failed state', async () => {
    // The server returns a 403 on the read-only floor (or a 4xx no-known-path);
    // the hook surfaces the message so the diff panel can render it verbatim.
    mockRunDiff.mockRejectedValue(new Error("Run diff isn't available on this read-only dashboard."));

    const { result } = renderHook(() => useRunDiff('wf-1'));

    await waitFor(() =>
      expect(result.current).toMatchObject({
        kind: 'failed',
        error: "Run diff isn't available on this read-only dashboard.",
      }),
    );

    expect(mockReportClientError).toHaveBeenCalledWith({
      component: 'formula-run-detail',
      operation: 'load diff',
      message: "wf-1: Run diff isn't available on this read-only dashboard.",
    });
  });

  it('passes the scope through to the request query', async () => {
    mockRunDiff.mockResolvedValue(okDiff());

    const { result } = renderHook(() => useRunDiff('wf-1', 'rig', 'demo'));
    await waitFor(() => expect(result.current.kind).toBe('ready'));

    expect(mockRunDiff).toHaveBeenCalledWith('wf-1', {}, { scopeKind: 'rig', scopeRef: 'demo' });
  });

  it('sends refresh=true on an explicit manual refresh (the bypass-lane client contract)', async () => {
    mockRunDiff.mockResolvedValue(okDiff());

    const { result } = renderHook(() => useRunDiff('wf-1'));
    await waitFor(() => expect(result.current.kind).toBe('ready'));
    if (result.current.kind !== 'ready') throw new Error('diff did not load');
    mockRunDiff.mockClear();

    await act(async () => {
      await result.current.refresh();
    });

    expect(mockRunDiff).toHaveBeenCalledWith('wf-1', {}, { refresh: true });
  });

  it('omits refresh on cheapRefresh (the event-driven cheap-lane client contract)', async () => {
    mockRunDiff.mockResolvedValue(okDiff());

    const { result } = renderHook(() => useRunDiff('wf-1'));
    await waitFor(() => expect(result.current.kind).toBe('ready'));
    if (result.current.kind !== 'ready') throw new Error('diff did not load');
    mockRunDiff.mockClear();

    await act(async () => {
      await result.current.cheapRefresh();
    });

    // cheapRefresh omits `refresh: true` — the cheap-lane client contract for a
    // future server-side diff cache. It is inert on the wire today (runQuery
    // drops the flag, no cache exists); the live burst protection is the
    // tab-gating + useGcEventRefresh coalescing, not this flag.
    expect(mockRunDiff).toHaveBeenCalledWith('wf-1', {}, {});
  });
});

function okDiff(): RunDiffResponse {
  return {
    kind: 'ok',
    rootPath: { kind: 'known', path: '/tmp/run' },
    comparison: { kind: 'head', reason: 'no_upstream' },
    status: [],
    changedFiles: [],
    patch: '',
    truncated: false,
  };
}
