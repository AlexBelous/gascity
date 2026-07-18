// Shared helpers for formula variable maps. Used by the inline launcher, the
// write adapter (sling body), and the read adapter (POST preview body) so the
// three call sites clean and scope variables identically.

/**
 * Drop empty values; return undefined when nothing remains so an empty `vars`
 * key is omitted from the request body rather than sent as `{}`.
 */
export function cleanVars(
  vars: Record<string, string> | undefined,
): Record<string, string> | undefined {
  if (vars === undefined) return undefined;
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(vars)) {
    if (value !== '') out[name] = value;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/**
 * Keep only the variables the current formula declares, dropping any keys that
 * leaked from a previously mounted formula's launcher state. Defence in depth
 * behind the per-formula launcher remount: a stale key can never reach a sling
 * or preview body for a different formula.
 */
export function pickDeclaredVars(
  vars: Record<string, string>,
  varDefs: ReadonlyArray<{ name: string }>,
): Record<string, string> {
  const declared = new Set(varDefs.map((v) => v.name));
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(vars)) {
    if (declared.has(name)) out[name] = value;
  }
  return out;
}

/**
 * A stable cache-key fragment for a cleaned variable map: keys sorted so an
 * edit that only reorders insertion (never happens today, but cheap to defend)
 * produces the same key, while any value or key change produces a new one.
 */
export function varsCacheKey(vars: Record<string, string> | undefined): string {
  if (vars === undefined) return '';
  return JSON.stringify(vars, Object.keys(vars).sort());
}
