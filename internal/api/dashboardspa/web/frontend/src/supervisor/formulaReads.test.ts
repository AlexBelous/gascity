import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  FormulaRecentRunResponse,
  FormulaSummaryResponse,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { setActiveCity } from '../api/cityBase';
import {
  type SupervisorApi,
  SupervisorApiError,
  resetSupervisorApiForTests,
  setSupervisorApiForTests,
} from './client';
import {
  getSupervisorFormulaPreview,
  getSupervisorFormulaRuns,
  getSupervisorFormulaSteps,
  listSupervisorFormulas,
  recentRunTone,
} from './formulaReads';

function stub(over: Partial<SupervisorApi>): void {
  setSupervisorApiForTests(over as unknown as SupervisorApi);
}

function summary(over: Partial<FormulaSummaryResponse> = {}): FormulaSummaryResponse {
  return {
    name: 'code-review',
    description: 'Review a change.',
    version: 'v2',
    run_count: 3,
    recent_runs: null,
    var_defs: null,
    ...over,
  };
}

function run(over: Partial<FormulaRecentRunResponse> = {}): FormulaRecentRunResponse {
  return {
    workflow_id: 'wf1',
    status: 'done',
    target: 'reviewer',
    started_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T01:00:00Z',
    ...over,
  };
}

beforeEach(() => setActiveCity('test-city'));
afterEach(() => {
  resetSupervisorApiForTests();
  vi.restoreAllMocks();
});

describe('formulaReads', () => {
  it('lists the active city formulas and defaults to the active city scope', async () => {
    const formulas = vi.fn(async () => ({ items: [summary()], partial: false, total: 1 }));
    stub({ formulas });

    await expect(listSupervisorFormulas()).resolves.toEqual([
      expect.objectContaining({ name: 'code-review', run_count: 3 }),
    ]);
    // The server rejects a missing scope with "scope_kind and scope_ref are
    // required", so the adapter defaults to the active city scope.
    expect(formulas).toHaveBeenCalledWith('test-city', {
      scope_kind: 'city',
      scope_ref: 'test-city',
    });
  });

  it('normalizes a null formulas list to []', async () => {
    stub({ formulas: vi.fn(async () => ({ items: null, partial: false, total: 0 })) });
    await expect(listSupervisorFormulas()).resolves.toEqual([]);
  });

  it('forwards scope query params only when present', async () => {
    const formulas = vi.fn(async () => ({ items: [], partial: false, total: 0 }));
    stub({ formulas });

    await listSupervisorFormulas({ scope_kind: 'rig', scope_ref: 'east' });
    expect(formulas).toHaveBeenCalledWith('test-city', { scope_kind: 'rig', scope_ref: 'east' });
  });

  it('reads recent runs for a formula', async () => {
    const formulaRuns = vi.fn(async () => ({
      formula: 'demo',
      partial: false,
      recent_runs: [run({ workflow_id: 'wf-a' })],
      run_count: 1,
    }));
    stub({ formulaRuns });

    await expect(getSupervisorFormulaRuns('demo')).resolves.toEqual([
      expect.objectContaining({ workflow_id: 'wf-a' }),
    ]);
    expect(formulaRuns).toHaveBeenCalledWith('test-city', 'demo', {
      scope_kind: 'city',
      scope_ref: 'test-city',
    });
  });

  it('normalizes null recent_runs to [] and forwards a limit', async () => {
    const formulaRuns = vi.fn(async () => ({
      formula: 'demo',
      partial: false,
      recent_runs: null,
      run_count: 0,
    }));
    stub({ formulaRuns });

    await expect(getSupervisorFormulaRuns('demo', { limit: 5 })).resolves.toEqual([]);
    expect(formulaRuns).toHaveBeenCalledWith('test-city', 'demo', {
      scope_kind: 'city',
      scope_ref: 'test-city',
      limit: 5,
    });
  });

  it('honors an explicit rig scope over the city default', async () => {
    const formulaRuns = vi.fn(async () => ({
      formula: 'demo',
      partial: false,
      recent_runs: null,
      run_count: 0,
    }));
    stub({ formulaRuns });

    await getSupervisorFormulaRuns('demo', { scope_kind: 'rig', scope_ref: 'east', limit: 2 });
    expect(formulaRuns).toHaveBeenCalledWith('test-city', 'demo', {
      scope_kind: 'rig',
      scope_ref: 'east',
      limit: 2,
    });
  });

  it('propagates a SupervisorApiError from the facade (no swallow)', async () => {
    stub({
      formulas: vi.fn(async () => {
        throw new SupervisorApiError(500, 'boom', undefined);
      }),
    });
    await expect(listSupervisorFormulas()).rejects.toBeInstanceOf(SupervisorApiError);
  });

  it('compiles default-var target steps via GET detail with the city scope', async () => {
    const formulaDetail = vi.fn(async () => ({
      name: 'demo',
      description: '',
      version: 'v1',
      preview: { nodes: [], edges: [] },
      deps: null,
      var_defs: null,
      steps: [{ id: 'review', kind: 'agent', title: 'Review' }],
    }));
    stub({ formulaDetail });

    await expect(getSupervisorFormulaSteps('demo', 'reviewer')).resolves.toEqual([
      expect.objectContaining({ id: 'review' }),
    ]);
    expect(formulaDetail).toHaveBeenCalledWith('test-city', 'demo', {
      target: 'reviewer',
      scope_kind: 'city',
      scope_ref: 'test-city',
    });

    formulaDetail.mockResolvedValueOnce({
      name: 'demo',
      description: '',
      version: 'v1',
      preview: { nodes: [], edges: [] },
      deps: null,
      var_defs: null,
      steps: null,
    });
    await expect(getSupervisorFormulaSteps('demo', 'reviewer')).resolves.toEqual([]);
  });

  it('compiles variable-aware target steps via POST preview (vars + city scope)', async () => {
    const formulaPreview = vi.fn(async () => ({
      name: 'demo',
      description: '',
      version: 'v1',
      preview: { nodes: [], edges: [] },
      deps: null,
      var_defs: null,
      steps: [{ id: 'review', kind: 'agent', title: 'Review' }],
    }));
    stub({ formulaPreview });

    await expect(
      getSupervisorFormulaPreview('demo', 'reviewer', { repo: 'gc/ds' }),
    ).resolves.toEqual([expect.objectContaining({ id: 'review' })]);
    // Entered vars reach the POST body — GET detail would drop them.
    expect(formulaPreview).toHaveBeenCalledWith('test-city', 'demo', {
      target: 'reviewer',
      scope_kind: 'city',
      scope_ref: 'test-city',
      vars: { repo: 'gc/ds' },
    });

    // No vars: the vars key is omitted rather than sent as {}.
    formulaPreview.mockClear();
    formulaPreview.mockResolvedValueOnce({
      name: 'demo',
      description: '',
      version: 'v1',
      preview: { nodes: [], edges: [] },
      deps: null,
      var_defs: null,
      steps: [],
    });
    await expect(getSupervisorFormulaPreview('demo', 'reviewer', undefined)).resolves.toEqual([]);
    expect(formulaPreview).toHaveBeenCalledWith('test-city', 'demo', {
      target: 'reviewer',
      scope_kind: 'city',
      scope_ref: 'test-city',
    });
  });

  it('maps run status to a glyph+word StatusBadge tone', () => {
    expect(recentRunTone('completed')).toBe('ok');
    expect(recentRunTone('done')).toBe('ok');
    expect(recentRunTone('running')).toBe('ok');
    expect(recentRunTone('failed')).toBe('stuck');
    expect(recentRunTone('blocked')).toBe('warn');
    expect(recentRunTone('queued')).toBe('neutral');
    expect(recentRunTone('')).toBe('neutral');
  });
});
