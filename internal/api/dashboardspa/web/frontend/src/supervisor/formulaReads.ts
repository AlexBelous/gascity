import type {
  FormulaDetailResponse,
  FormulaRecentRunResponse,
  FormulaStepResponse,
  FormulaSummaryResponse,
  FormulaVarDefResponse,
  GetV0CityByCityNameFormulasByNameRunsData,
} from 'gas-city-dashboard-shared/gc-supervisor';
import { activeCityOrThrow } from '../api/cityBase';
import type { StatusTone } from '../components/StatusBadge';
import { supervisorApi } from './client';

// Read adapter for the Formulas tab. Routes import ONLY from here (never the
// supervisor client or generated SDK directly), mirroring the stable
// read-adapter pattern used by beadReads. Two jobs:
//   - normalize the wire's `T[] | null` arrays (items / recent_runs) to [], so
//     callers map/length without guards;
//   - depend only on STABLE supervisor DTOs (FormulaListBody / FormulaRunsResponse),
//     never on feat/runproj-event-sourcing run-view/projection internals — so a
//     rebase onto the churning base branch can't break this surface.

export type SupervisorFormula = FormulaSummaryResponse;
export type SupervisorFormulaRun = FormulaRecentRunResponse;
export type SupervisorFormulaStep = FormulaStepResponse;
export type SupervisorFormulaVarDef = FormulaVarDefResponse;

export interface FormulaScope {
  scope_kind?: string;
  scope_ref?: string;
}

/** Catalog list of formula definitions for the active city. */
export async function listSupervisorFormulas(scope?: FormulaScope): Promise<SupervisorFormula[]> {
  const cityName = activeCityOrThrow('list supervisor formulas');
  const body = await supervisorApi().formulas(cityName, resolveFormulaScope(cityName, scope));
  return body.items ?? [];
}

/** Recent runs for one formula (newest first, per the supervisor). */
export async function getSupervisorFormulaRuns(
  name: string,
  scope?: FormulaScope & { limit?: number },
): Promise<SupervisorFormulaRun[]> {
  const cityName = activeCityOrThrow('get supervisor formula runs');
  const limit = scope?.limit;
  const query: NonNullable<GetV0CityByCityNameFormulasByNameRunsData['query']> = {
    ...resolveFormulaScope(cityName, scope),
    ...(limit === undefined ? {} : { limit }),
  };
  const body = await supervisorApi().formulaRuns(cityName, name, query);
  return body.recent_runs ?? [];
}

/**
 * Compile a formula's step graph with its DECLARED DEFAULT variable values for
 * a chosen target, via GET detail. GET ignores caller-entered vars, so this is
 * used only where variable-aware compilation is unavailable: the read-only
 * dashboard, where the launch — and the POST preview that mirrors it — is
 * disabled by the server mutation gate. Steps are null-normalized to [].
 */
export async function getSupervisorFormulaSteps(
  name: string,
  target: string,
  scope?: FormulaScope,
): Promise<SupervisorFormulaStep[]> {
  const cityName = activeCityOrThrow('compile supervisor formula preview');
  const detail: FormulaDetailResponse = await supervisorApi().formulaDetail(cityName, name, {
    target,
    ...resolveFormulaScope(cityName, scope),
  });
  return detail.steps ?? [];
}

/**
 * Compile a formula's step graph against a chosen target AND the operator's
 * entered variable values, via POST preview. This is the variable-aware path
 * the launcher uses when mutations are enabled: GET detail compiles the
 * declared defaults and silently ignores entered vars, so a preview beside the
 * launch form must post them. `vars` should already be cleaned by the caller;
 * an undefined `vars` compiles the declared defaults. Steps normalize to [].
 */
export async function getSupervisorFormulaPreview(
  name: string,
  target: string,
  vars: Record<string, string> | undefined,
  scope?: FormulaScope,
): Promise<SupervisorFormulaStep[]> {
  const cityName = activeCityOrThrow('preview supervisor formula');
  const detail: FormulaDetailResponse = await supervisorApi().formulaPreview(cityName, name, {
    target,
    ...resolveFormulaScope(cityName, scope),
    ...(vars === undefined ? {} : { vars }),
  });
  return detail.steps ?? [];
}

/**
 * The effective workflow scope for a formula read. The supervisor rejects a
 * missing scope ("scope_kind and scope_ref are required") and a half-specified
 * one ("... must be provided together"), so default to the active city scope
 * unless the caller supplied BOTH fields explicitly (e.g. a rig lane).
 */
function resolveFormulaScope(
  cityName: string,
  scope: FormulaScope | undefined,
): { scope_kind: string; scope_ref: string } {
  const kind = scope?.scope_kind?.trim();
  const ref = scope?.scope_ref?.trim();
  if (kind && ref) return { scope_kind: kind, scope_ref: ref };
  return { scope_kind: 'city', scope_ref: cityName };
}

/**
 * Map a formula run's free-form status to a StatusBadge tone. Color is never the
 * sole signal (StatusBadge pairs glyph + word), so two states sharing a tone
 * (done and running are both `ok`) still read distinctly in the greyscale test.
 */
export function recentRunTone(status: string): StatusTone {
  switch (status.trim().toLowerCase()) {
    case 'completed':
    case 'done':
    case 'closed':
    case 'success':
    case 'succeeded':
    case 'running':
    case 'active':
    case 'in_progress':
      return 'ok';
    case 'failed':
    case 'error':
    case 'errored':
      return 'stuck';
    case 'blocked':
    case 'waiting':
      return 'warn';
    default:
      return 'neutral';
  }
}
