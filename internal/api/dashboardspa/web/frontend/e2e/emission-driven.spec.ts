import {
  EMIT_CITY_BASE,
  EMIT_FORMULA,
  EMIT_HOME_SYNOPSIS,
  EMIT_PHASE_LABEL,
  EMIT_RUN_DETAIL_SYNOPSIS,
  EMIT_RUN_ID,
  EMIT_STEP_A_ID,
} from './fixtures/expected';
import { gotoCityRoute } from './support/renderGuards';
import { expect, test } from './support/fixtures';

// Layer B, emission project (chromium-emit): drive Chromium against the
// fakesupervisor started with -seed=emit, whose entire state — the run views and
// the home page — was produced by the REAL event-emission pipeline
// (test/dashport/emitseed), not a hand-authored corpus. The four specs prove the
// genuine emissions render at the pixel level: the completed run in the runs
// history, its terminal run detail, the home dial's active-session count, and the
// real bead.closed / recovered bead.updated rows in the activity feed.
//
// The no-error-boundary + no-client-error-POST guards run automatically after
// each test via the auto renderGuards fixture (support/fixtures.ts), so each spec
// asserts only positive emission-derived content, scoped so a stray substring
// cannot satisfy it. Selectors were grounded against the live rendered DOM of the
// -seed=emit server.

test.describe('dashboard render smoke over the emission-driven run', () => {
  test('runs list history reveals the emission run as terminal complete', async ({ page }) => {
    // history=1 reveals the historical section; the completed run is hidden from
    // the default active view (routes/Runs.tsx).
    await gotoCityRoute(page, EMIT_CITY_BASE, '/runs?history=1');
    await expect(page.getByRole('heading', { name: 'Runs', level: 1 })).toBeVisible();
    // Scope every assertion to the Historical region so nothing in the active
    // lanes (empty here) can satisfy it. The lane renders the run root id, its
    // formula title, and the terminal phase label — all emission-derived.
    const history = page.getByRole('region', { name: 'Historical runs' });
    await expect(history.getByText(EMIT_RUN_ID, { exact: true }).first()).toBeVisible();
    await expect(history.getByText(EMIT_FORMULA, { exact: true }).first()).toBeVisible();
    await expect(history.getByText(EMIT_PHASE_LABEL, { exact: true }).first()).toBeVisible();
  });

  test('run detail renders the emission run node titles with terminal state', async ({ page }) => {
    await gotoCityRoute(page, EMIT_CITY_BASE, `/runs/${EMIT_RUN_ID}`);
    // The detail h1 is the emission run's formula name (routes/FormulaRunDetail.tsx).
    await expect(page.getByRole('heading', { name: EMIT_FORMULA, level: 1 })).toBeVisible();
    // Terminal proof via the synopsis: "3 nodes. 3 done." renders only when the
    // root and BOTH steps read terminal. A bare getByText('done') would be vacuous
    // (it substring-matches the synopsis and metadata cells), so scope the node's
    // terminal status to the node button itself.
    await expect(page.getByText(EMIT_RUN_DETAIL_SYNOPSIS, { exact: false })).toBeVisible();
    const graph = page.getByRole('region', { name: 'Formula run graph' });
    const stepA = graph.getByRole('button', { name: /step-a step/i });
    await expect(stepA).toBeVisible();
    await expect(stepA.getByText('done', { exact: false })).toBeVisible();
    await expect(graph.getByRole('button', { name: /step-b step/i })).toBeVisible();
  });

  test('home dial-grid and synopsis reflect the emission-driven state', async ({ page }) => {
    await gotoCityRoute(page, EMIT_CITY_BASE, '');
    await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible();
    // The h1 renders identically in the loading/error branches, so assert the
    // status+census-derived synopsis that renders ONLY once the home data loaded:
    // the emission city name, its one active session bead, and zero in-flight runs
    // (the emission run completed). Its presence proves the sessions read AND the
    // run census wired through — the census having classified run-emit as a
    // non-running (completed) run is exactly why "0 running" renders.
    await expect(page.getByText(EMIT_HOME_SYNOPSIS, { exact: false })).toBeVisible();
    // Dial grid: the "active sessions" instrument carries the same count (1). It
    // renders identically empty on a home-data failure, so a populated value proves
    // the read wired through.
    const dials = page.getByTestId('dial-grid');
    await expect(dials).toBeVisible();
    await expect(dials.getByRole('link', { name: 'active sessions: 1' })).toBeVisible();
    // A healthy home shows no alert; the error branches render one.
    await expect(page.getByRole('alert')).toHaveCount(0);
  });

  test('activity renders the emission run close edges and the recovered event', async ({ page }) => {
    await gotoCityRoute(page, EMIT_CITY_BASE, '/activity');
    await expect(page.getByRole('heading', { name: 'Activity', level: 1 })).toBeVisible();
    // Scope to the named events table (three tables render: Supervisor events,
    // Deploy history, Git commits). Every row here is a genuine emission.
    const eventsTable = page.getByRole('table', { name: 'Supervisor events' });
    // The real bead.closed close edges (both steps + the root close) render, keyed
    // to the exact emission run subject.
    await expect(eventsTable.getByText('bead.closed', { exact: true }).first()).toBeVisible();
    await expect(eventsTable.getByText(EMIT_RUN_ID, { exact: true }).first()).toBeVisible();
    // The recovered event (the #4397 backdated bead.updated for step-a) is visible:
    // its type and its subject both render as a row. Scope the row so the type and
    // subject must co-occur, proving the recovered edge — not merely some update —
    // reached the feed.
    const recoveredRow = eventsTable
      .getByRole('row')
      .filter({ hasText: 'bead.updated' })
      .filter({ hasText: EMIT_STEP_A_ID });
    await expect(recoveredRow.first()).toBeVisible();
  });
});
