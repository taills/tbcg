import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import type { ItemDefinition } from './shared/content/item-schema';
import { savePendingCreateRecovery } from './features/content/item-workspace-utils';

function response(data: unknown, status = 200, success = true, error: unknown = null): Response {
  return new Response(JSON.stringify({ success, data, error, meta: {} }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const anonymousSession = {
  authenticated: false,
  csrf_token: 'anonymous-session-csrf-token',
  user: null,
  game_roles: [],
};

const authorSession = {
  authenticated: true,
  csrf_token: 'authenticated-session-csrf-token',
  user: { admin_id: '00000000-0000-4000-8000-000000000001', username: 'author' },
  game_roles: [{ game_id: 'demo', roles: ['Author'] }],
};

function itemDefinition(gameId: string, displayName: string): ItemDefinition {
  return {
    game_id: gameId,
    content_id: 'demo:item/healing-potion',
    revision: 1,
    schema_version: 1,
    public: {
      game_id: gameId,
      content_id: 'demo:item/healing-potion',
      revision: 1,
      schema_version: 1,
      item_type: 'consumable',
      display_name: displayName,
      icon_id: 'demo:icon/placeholder',
      stack_limit: 20,
      price_minor: '900',
    },
    server: { reward_value_minor: '250', drop_weight_basis_points: 1000 },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.sessionStorage.clear();
  window.history.replaceState(null, '', '/');
});

describe('App', () => {
  it('requires a session before showing content editing controls', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(anonymousSession));
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);

    expect(await screen.findByRole('heading', { name: '管理端登录' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '内容工作台' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('用户名')).toBeInTheDocument();
    expect(screen.getByLabelText('密码')).toBeInTheDocument();
  });

  it('shows a recoverable error if the server session cannot be loaded', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network unavailable')));
    render(<App />);

    expect(await screen.findByRole('alert')).toHaveTextContent('无法连接管理服务');
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument();
  });

  it('does not expose the previous admin recovery data after guest login as another Reader', async () => {
    const authorId = '00000000-0000-4000-8000-000000000001';
    const readerSession = {
      ...authorSession,
      csrf_token: 'reader-session-csrf-token',
      user: { admin_id: '00000000-0000-4000-8000-000000000002', username: 'reader' },
      game_roles: [{ game_id: 'demo', roles: ['Reader'] }],
    };
    savePendingCreateRecovery('demo', authorId, {
      contentId: 'demo:item/healing-potion',
      local: {
        contentId: 'demo:item/healing-potion',
        itemType: 'consumable',
        displayName: 'Author private title',
        description: 'Author private description',
        iconId: 'demo:icon/placeholder',
        stackLimit: '20',
        priceMinor: '0',
        rewardValueMinor: '987654321',
        dropWeight: '4321',
      },
    });
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response(anonymousSession))
        .mockResolvedValueOnce(response(readerSession)),
    );
    render(<App />);

    fireEvent.change(await screen.findByLabelText('用户名'), { target: { value: 'reader' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'reader-password' } });
    fireEvent.submit(
      screen.getByRole('button', { name: '登录' }).closest('form') as HTMLFormElement,
    );

    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('');
    expect(screen.getByRole('textbox', { name: '奖励值（最小货币单位）' })).toHaveValue('0');
    expect(screen.queryByRole('heading', { name: '草稿创建结果待核验' })).not.toBeInTheDocument();
    expect(
      window.sessionStorage.getItem(
        'tbcg:00000000-0000-4000-8000-000000000001:demo:pending-item-draft-create',
      ),
    ).toContain('987654321');
  });

  it('keeps invalid credentials on the login screen with the server error', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(anonymousSession))
      .mockResolvedValueOnce(
        response(null, 401, false, {
          code: 'invalid_credentials',
          message: '用户名或密码错误',
          fields: [],
        }),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);

    fireEvent.change(await screen.findByLabelText('用户名'), { target: { value: 'author' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'wrong-password' } });
    fireEvent.submit(
      screen.getByRole('button', { name: '登录' }).closest('form') as HTMLFormElement,
    );

    expect(await screen.findByRole('alert')).toHaveTextContent('用户名或密码错误');
    expect(screen.queryByRole('heading', { name: '内容工作台' })).not.toBeInTheDocument();
  });

  it('switches the editor authorization scope between Game-bound roles', async () => {
    const multiGameSession = {
      ...authorSession,
      game_roles: [
        { game_id: 'demo-read', roles: ['Reader'] },
        { game_id: 'demo-author', roles: ['Author'] },
      ],
    };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(multiGameSession)));
    render(<App />);

    expect(await screen.findByRole('combobox', { name: '游戏' })).toHaveValue('demo-read');
    expect(screen.queryByRole('button', { name: '保存草稿' })).not.toBeInTheDocument();
    window.history.replaceState(null, '', '/?draftId=draft-for-demo-read');
    fireEvent.change(screen.getByRole('combobox', { name: '游戏' }), {
      target: { value: 'demo-author' },
    });
    expect(await screen.findByRole('button', { name: '保存草稿' })).toBeInTheDocument();
    expect(new URLSearchParams(window.location.search).has('draftId')).toBe(false);
  });

  it('keeps dirty input when leaving is canceled and only discards after confirmation', async () => {
    const multiGameSession = {
      ...authorSession,
      game_roles: [
        { game_id: 'demo', roles: ['Author'] },
        { game_id: 'other-game', roles: ['Author'] },
      ],
    };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(multiGameSession)));
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    render(<App />);

    fireEvent.change(await screen.findByRole('textbox', { name: '物品名称' }), {
      target: { value: '尚未保存的本地名称' },
    });
    fireEvent.change(screen.getByRole('combobox', { name: '游戏' }), {
      target: { value: 'other-game' },
    });

    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('尚未保存'));
    expect(screen.getByRole('combobox', { name: '游戏' })).toHaveValue('demo');
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('尚未保存的本地名称');

    confirm.mockReturnValue(true);
    fireEvent.change(screen.getByRole('combobox', { name: '游戏' }), {
      target: { value: 'other-game' },
    });
    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('');
    expect(screen.getByRole('combobox', { name: '游戏' })).toHaveValue('other-game');
  });

  it('reloads the persisted draft when returning to its Game scope', async () => {
    const multiGameSession = {
      ...authorSession,
      game_roles: [
        { game_id: 'demo', roles: ['Author'] },
        { game_id: 'other-game', roles: ['Author'] },
      ],
    };
    const savedDraftId = '0ad049e6-1234-4abc-8000-000000000001';
    let savedDraft: Record<string, unknown> | null = null;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = String(input);
        if (url === '/api/v1/session') {
          return response(multiGameSession);
        }
        if (url === '/api/v1/games/demo/item-drafts' && init?.method === 'POST') {
          const body = JSON.parse(String(init.body)) as { definition: ItemDefinition };
          savedDraft = {
            draft_id: savedDraftId,
            game_id: 'demo',
            content_id: body.definition.content_id,
            draft_version: '1',
            definition: body.definition,
            updated_at: '2026-10-04T00:00:00Z',
          };
          return response(savedDraft, 201);
        }
        if (url === `/api/v1/games/demo/item-drafts/${savedDraftId}` && savedDraft !== null) {
          return response(savedDraft);
        }
        return response(null, 404, false, {
          code: 'not_found',
          message: 'not found',
          fields: [],
        });
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<App />);

    fireEvent.change(await screen.findByRole('textbox', { name: '物品名称' }), {
      target: { value: '服务器已保存名称' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/games/demo/item-drafts');
    await waitFor(() => {
      const alert = screen.queryByRole('alert');
      if (alert !== null) {
        throw new Error(alert.textContent ?? '草稿保存失败');
      }
      expect(screen.getByText(/草稿已保存/)).toBeInTheDocument();
      expect(screen.getByRole('combobox', { name: '游戏' })).toBeEnabled();
    });

    fireEvent.change(screen.getByRole('combobox', { name: '游戏' }), {
      target: { value: 'other-game' },
    });
    expect(confirm).not.toHaveBeenCalled();
    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('');
    fireEvent.change(screen.getByRole('combobox', { name: '游戏' }), {
      target: { value: 'demo' },
    });

    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue(
      '服务器已保存名称',
    );
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/games/demo/item-drafts/${savedDraftId}`,
      expect.anything(),
    );
  });

  it('restores a shared-link draft after leaving and returning to its Game', async () => {
    const multiGameSession = {
      ...authorSession,
      game_roles: [
        { game_id: 'demo', roles: ['Author'] },
        { game_id: 'other-game', roles: ['Author'] },
      ],
    };
    const draftId = '0ad049e6-1234-4abc-8000-000000000001';
    const draft = {
      draft_id: draftId,
      game_id: 'demo',
      content_id: 'demo:item/healing-potion',
      draft_version: '4',
      definition: itemDefinition('demo', '共享链接草稿'),
      updated_at: '2026-10-04T00:00:00Z',
    };
    const draftPath = `/api/v1/games/demo/item-drafts/${draftId}`;
    const fetchMock = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const url = String(input);
      if (url === '/api/v1/session') {
        return response(multiGameSession);
      }
      if (url === draftPath) {
        return response(draft);
      }
      return response(null, 404, false, {
        code: 'not_found',
        message: 'not found',
        fields: [],
      });
    });
    vi.stubGlobal('fetch', fetchMock);
    window.history.replaceState(null, '', `/?draftId=${draftId}`);
    render(<App />);

    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('共享链接草稿');
    expect(window.sessionStorage.getItem('tbcg:demo:item-draft')).toBe(draftId);
    fireEvent.change(screen.getByRole('combobox', { name: '游戏' }), {
      target: { value: 'other-game' },
    });
    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('');
    fireEvent.change(screen.getByRole('combobox', { name: '游戏' }), {
      target: { value: 'demo' },
    });

    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('共享链接草稿');
    expect(fetchMock.mock.calls.filter(([url]) => String(url) === draftPath)).toHaveLength(2);
  });

  it('blocks Game switching while a write is pending', async () => {
    let resolveSave: ((value: Response) => void) | undefined;
    const multiGameSession = {
      ...authorSession,
      game_roles: [
        { game_id: 'demo', roles: ['Author'] },
        { game_id: 'other-game', roles: ['Author'] },
      ],
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(multiGameSession))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveSave = resolve;
          }),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);

    fireEvent.change(await screen.findByRole('textbox', { name: '物品名称' }), {
      target: { value: '保存中的名称' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));

    expect(await screen.findByRole('combobox', { name: '游戏' })).toBeDisabled();
    resolveSave?.(response({}));
  });

  it('does not show content controls when the authenticated account has no Game role', async () => {
    const noRoles = { ...authorSession, game_roles: [] };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(noRoles)));
    render(<App />);

    expect(await screen.findByText('当前账号没有游戏授权')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '公开预览' })).not.toBeInTheDocument();
  });

  it('revokes the current session on logout', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(authorSession))
      .mockResolvedValueOnce(response({}))
      .mockResolvedValueOnce(response(anonymousSession));
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '退出登录' }));
    expect(await screen.findByRole('heading', { name: '管理端登录' })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(3);
    const logoutRequest = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(new Headers(logoutRequest.headers).get('X-CSRF-Token')).toBe(authorSession.csrf_token);
  });

  it('reports logout failures without fabricating a logged-out session', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(authorSession))
      .mockResolvedValueOnce(
        response(null, 500, false, { code: 'internal_error', message: '服务暂不可用', fields: [] }),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: '退出登录' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('服务暂不可用');
    expect(screen.getByRole('heading', { name: '内容工作台' })).toBeInTheDocument();
  });

  it('sends session-bound CSRF on login and opens the accessible item editor', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(anonymousSession))
      .mockResolvedValueOnce(response(authorSession));
    vi.stubGlobal('fetch', fetchMock);
    render(<App />);

    fireEvent.change(await screen.findByLabelText('用户名'), { target: { value: 'author' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'test-password' } });
    fireEvent.submit(
      screen.getByRole('button', { name: '登录' }).closest('form') as HTMLFormElement,
    );

    expect(await screen.findByRole('heading', { name: '内容工作台' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '价格（最小货币单位）' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '公开预览' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '保存草稿' })).toBeInTheDocument();

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const loginRequest = fetchMock.mock.calls[1]?.[1] as RequestInit;
    const headers = new Headers(loginRequest.headers);
    expect(headers.get('X-CSRF-Token')).toBe(anonymousSession.csrf_token);
    expect(loginRequest.credentials).toBe('same-origin');
  });
});
