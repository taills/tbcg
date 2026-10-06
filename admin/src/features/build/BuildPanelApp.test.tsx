import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from '../../App';

function envelope(data: unknown, meta: Record<string, unknown> = {}): Response {
  return new Response(JSON.stringify({ success: true, data, error: null, meta }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function sessionWith(roles: string[]) {
  return {
    authenticated: true,
    csrf_token: 'publisher-session-csrf-token',
    user: { admin_id: '00000000-0000-4000-8000-000000000071', username: 'publisher' },
    game_roles: [{ game_id: 'demo', roles }],
  };
}

function routedFetch(roles: string[]) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url === '/api/v1/session') {
      return envelope(sessionWith(roles));
    }
    if (url.startsWith('/api/v1/games/demo/build-candidates')) {
      return envelope([], { total: 0, limit: 50, offset: 0 });
    }
    if (url.startsWith('/api/v1/games/demo/item-drafts')) {
      return envelope([], { total: 0, limit: 50, offset: 0 });
    }
    throw new Error(`unexpected request ${url}`);
  });
}

afterEach(() => vi.unstubAllGlobals());

describe('App build panel access', () => {
  it('shows the build panel to a Publisher who is not also a Reviewer', async () => {
    const fetchMock = routedFetch(['Publisher']);
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);
    expect(await screen.findByRole('heading', { name: '单物品构建' })).toBeInTheDocument();
    expect(await screen.findByText('当前没有可构建的已批准 revision。')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '待审核 revision' })).not.toBeInTheDocument();
  });

  it('does not render or request builds for an Author', async () => {
    const fetchMock = routedFetch(['Author']);
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    await screen.findByText('游戏：demo');
    expect(screen.queryByRole('heading', { name: '单物品构建' })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('build-candidates'))).toBe(
      false,
    );
  });
});
