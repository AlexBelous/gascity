import type { SlingInputBody, SlingResponse } from 'gas-city-dashboard-shared/gc-supervisor';
import { activeCityOrThrow } from '../api/cityBase';
import { supervisorApi } from './client';
import { cleanVars } from './formulaVars';

export interface SlingFormulaInput {
  formula: string;
  target: string;
  /** Formula variables (name → value). Empty entries are dropped by the caller. */
  vars?: Record<string, string>;
  scopeKind?: string;
  scopeRef?: string;
  title?: string;
}

// Write adapter for the Formulas launcher: the ONE mutation the tab performs.
// Formula-native — the sling body carries {formula, target, vars?} and never a
// `bead` (a formula launch creates its own workflow root, unlike a bead sling).
// Required fields are validated client-side so an empty form can't 400; the
// dashboard's read-only posture disables the control, and the supervisor proxy
// gate (405) stays the real enforcement.

export async function slingFormula(input: SlingFormulaInput): Promise<SlingResponse> {
  const formula = input.formula.trim();
  const target = input.target.trim();
  if (formula.length === 0) throw new Error('formula is required');
  if (target.length === 0) throw new Error('sling target is required');

  const cityName = activeCityOrThrow('sling formula');
  const body: SlingInputBody = { formula, target };

  const vars = cleanVars(input.vars);
  if (vars !== undefined) body.vars = vars;
  if (input.scopeKind) body.scope_kind = input.scopeKind;
  if (input.scopeRef) body.scope_ref = input.scopeRef;
  if (input.title && input.title.trim().length > 0) body.title = input.title.trim();

  return supervisorApi().sling(cityName, body);
}
