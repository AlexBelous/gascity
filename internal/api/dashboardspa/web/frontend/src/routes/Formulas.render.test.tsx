import { cleanup, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setActiveCity } from '../api/cityBase';
import { invalidate } from '../api/cache';
import { resetSupervisorApiForTests } from '../supervisor/client';
import { NowProvider } from '../contexts/NowContext';
import { ReadOnlyProvider } from '../contexts/ReadOnlyContext';
import { FormulasPage } from './Formulas';

interface FetchCall {
  method: string;
  path: string;
  query: URLSearchParams;
}
const fetchCalls: FetchCall[] = [];

function parsedUrl(input: RequestInfo | URL): URL {
  if (input instanceof Request) return new URL(input.url);
  if (input instanceof URL) return input;
  return new URL(String(input), 'http://localhost');
}
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

const FORMULAS = {
  items: [
    {
      name: 'code-review',
      description: 'Review a change.',
      version: 'v2',
      run_count: 12,
      recent_runs: [
        {
          workflow_id: 'wf_9a1c',
          status: 'done',
          target: 'reviewer',
          started_at: '2026-06-01T00:00:00Z',
          updated_at: '2026-06-01T01:00:00Z',
        },
      ],
      var_defs: [
        { name: 'repo', type: 'string', required: true },
        { name: 'base_branch', type: 'string' },
      ],
    },
    {
      name: 'pancakes',
      description: 'A tiny demo.',
      version: 'v1',
      run_count: 0,
      recent_runs: [],
      var_defs: [],
    },
  ],
  partial: false,
  total: 2,
};

// A unique city per test → a unique cache key per test, so a prior test's
// stale-while-revalidate setCached (which fires after findBy* resolves) can't
// bleed into the next test's mount seed.
let cityCounter = 0;
let activeTestCity = 'test-city';
const FORMULAS_PATH = /^\/v0\/city\/[^/]+\/formulas$/;

function stubFetch(formulasBody: unknown = FORMULAS, status = 200): void {
  fetchCalls.length = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = parsedUrl(input);
      const method = init?.method ?? (input instanceof Request ? input.method : 'GET');
      fetchCalls.push({ method, path: url.pathname, query: url.searchParams });
      if (FORMULAS_PATH.test(url.pathname)) return jsonResponse(formulasBody, status);
      return jsonResponse({ error: `unexpected ${url.pathname}` }, 404);
    }),
  );
}

function renderPage(path = '/formulas', options: { readOnly?: boolean } = {}) {
  return render(
    <MemoryRouter
      initialEntries={[path]}
      future={{ v7_relativeSplatPath: true, v7_startTransition: true }}
    >
      <NowProvider intervalMs={1_000_000}>
        <ReadOnlyProvider readOnly={options.readOnly ?? false}>
          <FormulasPage />
        </ReadOnlyProvider>
      </NowProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  activeTestCity = `test-city-${++cityCounter}`;
  setActiveCity(activeTestCity);
  invalidate('');
  stubFetch();
});

afterEach(() => {
  cleanup();
  resetSupervisorApiForTests();
  vi.unstubAllGlobals();
});

describe('FormulasPage', () => {
  it('renders the catalog table from the supervisor formulas list', async () => {
    renderPage();

    expect(await screen.findByText('code-review')).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Formula' })).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Vars' })).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Last run' })).toBeTruthy();
    // The catalog read carries the active city scope the server requires.
    const listCall = fetchCalls.find((c) => FORMULAS_PATH.test(c.path));
    expect(listCall).toBeTruthy();
    expect(listCall?.query.get('scope_kind')).toBe('city');
    expect(listCall?.query.get('scope_ref')).toBe(activeTestCity);
  });

  it('shows vars + run counts and a glyph+word last-run badge', async () => {
    renderPage();

    const row = (await screen.findByText('code-review')).closest('tr');
    expect(row).not.toBeNull();
    const cells = within(row as HTMLElement);
    expect(cells.getByText('2')).toBeTruthy(); // vars = var_defs.length
    expect(cells.getByText('12')).toBeTruthy(); // runs = run_count
    expect(cells.getByText('done')).toBeTruthy(); // last-run status word (tone aside)
  });

  it('shows a no-runs badge when a formula has never run', async () => {
    renderPage();

    const row = (await screen.findByText('pancakes')).closest('tr');
    expect(within(row as HTMLElement).getByText('no runs')).toBeTruthy();
  });

  it('links each formula to its detail page; clicking Run issues no mutation', async () => {
    renderPage();

    const nameLink = await screen.findByRole('link', { name: 'code-review' });
    expect(nameLink.getAttribute('href')).toBe('/formulas/code-review');

    const row = (await screen.findByText('code-review')).closest('tr') as HTMLElement;
    const runLink = within(row).getByText('Run ▸');
    expect(runLink.getAttribute('href')).toBe('/formulas/code-review');
    expect(fetchCalls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('renders an empty slot when the city defines no formulas', async () => {
    stubFetch({ items: [], partial: false, total: 0 });
    renderPage();

    expect(await screen.findByText('No formulas defined in this city yet.')).toBeTruthy();
  });

  it('surfaces a load error as role=alert with no rows', async () => {
    stubFetch({ error: 'supervisor down' }, 503);
    renderPage();

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toMatch(/formulas/i);
    expect(screen.queryByText('code-review')).toBeNull();
  });

  it('notes the read-only posture in the header', async () => {
    renderPage('/formulas', { readOnly: true });

    await screen.findByText('code-review');
    expect(screen.getByText('Read-only')).toBeTruthy();
  });
});
