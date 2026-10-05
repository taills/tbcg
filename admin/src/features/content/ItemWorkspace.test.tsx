import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Draft } from '../../shared/admin-api';
import type { ItemDefinition } from '../../shared/content/item-schema';
import ItemWorkspace from './ItemWorkspace';
import { readPendingCreateRecovery, savePendingCreateRecovery } from './item-workspace-utils';

function response(data: unknown, status = 200, success = true, error: unknown = null): Response {
  return new Response(JSON.stringify({ success, data, error, meta: {} }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function pageResponse(data: unknown, total: number): Response {
  return new Response(
    JSON.stringify({
      success: true,
      data,
      error: null,
      meta: { total, limit: 50, offset: 0 },
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  );
}

function definition(displayName = '回复药剂'): ItemDefinition {
  return {
    game_id: 'demo',
    content_id: 'demo:item/healing-potion',
    revision: 1,
    schema_version: 1,
    public: {
      game_id: 'demo',
      content_id: 'demo:item/healing-potion',
      revision: 1,
      schema_version: 1,
      item_type: 'consumable',
      display_name: displayName,
      icon_id: 'demo:icon/placeholder',
      stack_limit: 20,
      price_minor: '9007199254740993',
    },
    server: { reward_value_minor: '250000000000000001', drop_weight_basis_points: 1250 },
  };
}

function buildDraft(
  item: ItemDefinition,
  version: string,
  draftId = '0ad049e6-1234-4abc-8000-000000000001',
): Draft {
  return {
    draft_id: draftId,
    game_id: 'demo',
    content_id: item.content_id,
    draft_version: version,
    definition: item,
    updated_at: '2026-10-04T00:00:00Z',
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.history.replaceState(null, '', '/');
});

describe('ItemWorkspace', () => {
  it('retains local form input on CAS conflict and reapplies only after explicit action', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as { definition: ItemDefinition };
        return response(buildDraft(body.definition, '1'), 201);
      })
      .mockImplementationOnce(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as { definition: ItemDefinition };
        const serverDefinition = {
          ...body.definition,
          public: { ...body.definition.public, display_name: '服务器更新' },
        };
        return response(buildDraft(serverDefinition, '2'), 409, false, {
          code: 'draft_version_conflict',
          message: 'stale',
          fields: [],
        });
      })
      .mockImplementationOnce(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as {
          definition: ItemDefinition;
          expected_draft_version: string;
        };
        expect(body.expected_draft_version).toBe('2');
        expect(body.definition.public.display_name).toBe('本地更改');
        return response(buildDraft(body.definition, '3'));
      });
    vi.stubGlobal('fetch', fetchMock);
    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />);

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '初始名称' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByText(/草稿已保存/)).toBeInTheDocument();

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '本地更改' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByRole('heading', { name: '草稿版本冲突' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('本地更改');
    expect(screen.getByText('服务器更新')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '将本地更改重应用到最新版本' }));
    expect(await screen.findByText(/本地更改已明确重应用，当前版本 3/)).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('本地更改');
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('lets the author explicitly discard local edits and load the server comparison version', async () => {
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as { definition: ItemDefinition };
        return response(buildDraft(body.definition, '1'), 201);
      })
      .mockImplementationOnce(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as { definition: ItemDefinition };
        return response(
          buildDraft(
            {
              ...body.definition,
              public: { ...body.definition.public, display_name: '服务器版本' },
            },
            '2',
          ),
          409,
          false,
          {
            code: 'draft_version_conflict',
            message: 'stale',
            fields: [],
          },
        );
      });
    vi.stubGlobal('fetch', fetchMock);
    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />);

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '本地未保存' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByText(/草稿已保存/)).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '本地冲突输入' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByRole('heading', { name: '草稿版本冲突' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '使用服务器版本' }));
    expect(screen.queryByRole('heading', { name: '草稿版本冲突' })).not.toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('服务器版本');
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('does not hydrate an Author recovery form after the same account becomes Reader', async () => {
    const recovery = {
      contentId: 'demo:item/healing-potion',
      local: {
        contentId: 'demo:item/healing-potion',
        itemType: 'consumable' as const,
        displayName: 'Author private title',
        description: 'Author private description',
        iconId: 'demo:icon/placeholder',
        stackLimit: '20',
        priceMinor: '0',
        rewardValueMinor: '987654321',
        dropWeight: '4321',
      },
    };
    savePendingCreateRecovery('demo', 'admin-1', recovery);
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    const { rerender } = render(
      <ItemWorkspace
        gameId="demo"
        csrfToken="csrf"
        canEdit
        canReview={false}
        storageOwnerId="admin-1"
      />,
    );
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('Author private title');

    rerender(
      <ItemWorkspace
        gameId="demo"
        csrfToken="csrf"
        canEdit={false}
        canReview
        storageOwnerId="admin-1"
      />,
    );
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('');
    expect(screen.getByRole('textbox', { name: '奖励值（最小货币单位）' })).toHaveValue('0');
    expect(screen.queryByRole('heading', { name: '草稿创建结果待核验' })).not.toBeInTheDocument();
    expect(window.sessionStorage.getItem('tbcg:admin-1:demo:pending-item-draft-create')).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('loads a shared draft by opaque id and presents it read-only for non-authors', async () => {
    window.history.replaceState(null, '', '/?draftId=0ad049e6-1234-4abc-8000-000000000001');
    const fetchMock = vi
      .fn()
      .mockResolvedValue(response(buildDraft(definition('共享草稿'), '9007199254740993')));
    vi.stubGlobal('fetch', fetchMock);
    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit={false} canReview />);

    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('共享草稿');
    expect(window.sessionStorage.getItem('tbcg:demo:item-draft')).toBe(
      '0ad049e6-1234-4abc-8000-000000000001',
    );
    expect(screen.getAllByText('9007199254740993')).toHaveLength(2);
    expect(screen.getByText(/Reviewer 可查看草稿/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '保存草稿' })).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('validates, reports field errors, saves corrected content, and submits an integer revision', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(buildDraft(definition('待校验药剂'), '1'), 201))
      .mockResolvedValueOnce(
        response({
          valid: false,
          errors: [
            { pointer: '/public/icon_id', code: 'invalid_reference', message: '图标未授权' },
          ],
        }),
      )
      .mockImplementationOnce(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as { definition: ItemDefinition };
        return response(buildDraft(body.definition, '2'));
      })
      .mockResolvedValueOnce(response({ valid: true, errors: [] }))
      .mockResolvedValueOnce(
        response(
          {
            game_id: 'demo',
            content_id: 'demo:item/healing-potion',
            revision: 1,
            body_hash: 'b'.repeat(64),
            definition: definition('修正后药剂'),
            submitted_at: '2026-10-04T00:00:00Z',
          },
          201,
        ),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />);

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '待校验药剂' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByText(/草稿已保存/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '校验草稿' }));
    expect(await screen.findByText('图标未授权')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '价格（最小货币单位）' })).toBeInTheDocument();

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '修正后药剂' },
    });
    fireEvent.click(screen.getByRole('button', { name: '校验草稿' }));
    expect(await screen.findByText('校验通过。可以提交当前草稿 revision。')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '提交不可变 revision' }));
    expect(await screen.findByRole('heading', { name: 'revision 1 已提交' })).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(5));
    const submitRequest = fetchMock.mock.calls[4]?.[1] as RequestInit;
    expect(JSON.parse(String(submitRequest.body))).toMatchObject({ expected_draft_version: '2' });
  });

  it('ignores an older draft load after the selected draft changes', async () => {
    let resolveOld: ((value: Response) => void) | undefined;
    let resolveCurrent: ((value: Response) => void) | undefined;
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveOld = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveCurrent = resolve;
          }),
      );
    vi.stubGlobal('fetch', fetchMock);
    window.history.replaceState(null, '', '/?draftId=old-draft');
    const props = { gameId: 'demo', csrfToken: 'csrf', canEdit: false, canReview: false };
    const { rerender } = render(<ItemWorkspace {...props} />);

    window.history.replaceState(null, '', '/?draftId=current-draft');
    rerender(<ItemWorkspace {...props} />);
    resolveCurrent?.(response(buildDraft(definition('当前草稿'), '2', 'current-draft')));
    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('当前草稿');
    resolveOld?.(response(buildDraft(definition('旧草稿'), '1', 'old-draft')));

    await waitFor(() =>
      expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('当前草稿'),
    );
  });

  it('isolates a pending save when the Game scope changes', async () => {
    let resolveSave: ((value: Response) => void) | undefined;
    const fetchMock = vi.fn().mockImplementation(
      () =>
        new Promise<Response>((resolve) => {
          resolveSave = resolve;
        }),
    );
    vi.stubGlobal('fetch', fetchMock);
    const { rerender } = render(
      <ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />,
    );

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: 'demo 本地内容' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(fetchMock).toHaveBeenCalledTimes(1);

    rerender(<ItemWorkspace gameId="other-game" csrfToken="csrf" canEdit canReview={false} />);
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('');
    resolveSave?.(response(buildDraft(definition('demo 已保存'), '1'), 201));

    await waitFor(() => expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue(''));
    expect(window.sessionStorage.getItem('tbcg:demo:item-draft')).toBe(
      '0ad049e6-1234-4abc-8000-000000000001',
    );
    expect(new URLSearchParams(window.location.search).has('draftId')).toBe(false);
    expect(screen.queryByText(/草稿已保存/)).not.toBeInTheDocument();
  });

  it('does not render a submit result in a new Game scope after unmount', async () => {
    let resolveSubmit: ((value: Response) => void) | undefined;
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(buildDraft(definition('旧游戏草稿'), '1', 'submit-draft')))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveSubmit = resolve;
          }),
      );
    vi.stubGlobal('fetch', fetchMock);
    window.history.replaceState(null, '', '/?draftId=submit-draft');
    const { rerender } = render(
      <ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />,
    );

    expect(await screen.findByRole('textbox', { name: '物品名称' })).toHaveValue('旧游戏草稿');
    fireEvent.click(screen.getByRole('button', { name: '提交不可变 revision' }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    window.history.replaceState(null, '', '/');
    rerender(<ItemWorkspace gameId="other-game" csrfToken="csrf" canEdit canReview={false} />);
    await screen.findByRole('textbox', { name: '物品名称' });
    await act(async () =>
      resolveSubmit?.(
        response({
          game_id: 'demo',
          content_id: 'demo:item/healing-potion',
          revision: 1,
          body_hash: 'c'.repeat(64),
          definition: definition('旧游戏草稿'),
          submitted_at: '2026-10-04T00:00:00Z',
        }),
      ),
    );

    expect(screen.queryByRole('heading', { name: 'revision 1 已提交' })).not.toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('');
  });

  it('keeps a create with an unknown outcome pending when exact-content recovery is empty', async () => {
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new Error('response lost after commit'))
      .mockResolvedValueOnce(pageResponse([], 0));
    vi.stubGlobal('fetch', fetchMock);
    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />);

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '本地待核验草稿' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByRole('button', { name: '查找已有草稿' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '查找已有草稿' }));

    expect(await screen.findByText(/仍待核验/)).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('本地待核验草稿');
    expect(screen.getByRole('button', { name: '保存草稿' })).toBeDisabled();
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain(
      'content_id=demo%3Aitem%2Fhealing-potion',
    );
  });

  it('restores local create input after remount and discovers a candidate by its content ID', async () => {
    const candidate = buildDraft(definition('服务器已保存草稿'), '3');
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new Error('response lost after commit'))
      .mockResolvedValueOnce(pageResponse([candidate], 1));
    vi.stubGlobal('fetch', fetchMock);
    const firstRender = render(
      <ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />,
    );

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '刷新后仍保留的本地内容' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByRole('button', { name: '查找已有草稿' })).toBeInTheDocument();
    firstRender.unmount();

    const otherOwnerRender = render(
      <ItemWorkspace
        gameId="demo"
        csrfToken="csrf"
        canEdit
        canReview={false}
        storageOwnerId="other-admin"
      />,
    );
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('');
    expect(screen.queryByRole('heading', { name: '草稿创建结果待核验' })).not.toBeInTheDocument();
    otherOwnerRender.unmount();

    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />);
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('刷新后仍保留的本地内容');
    fireEvent.click(screen.getByRole('button', { name: '查找已有草稿' }));

    expect(await screen.findByRole('button', { name: '比较已有草稿版本 3' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('刷新后仍保留的本地内容');
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain(
      'content_id=demo%3Aitem%2Fhealing-potion',
    );
  });

  it('compares an existing draft and only reapplies a different local payload after explicit choice', async () => {
    const existingDraft = buildDraft(definition('服务器已有草稿'), '7');
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        response(null, 409, false, {
          code: 'draft_content_conflict',
          message: 'draft already exists',
          fields: [],
        }),
      )
      .mockResolvedValueOnce(pageResponse([existingDraft], 1))
      .mockImplementationOnce(async (_input: RequestInfo | URL, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as {
          definition: ItemDefinition;
          expected_draft_version: string;
        };
        expect(body.expected_draft_version).toBe('7');
        expect(body.definition.public.display_name).toBe('本地不同内容');
        return response(buildDraft(body.definition, '8'));
      });
    vi.stubGlobal('fetch', fetchMock);
    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />);

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '本地不同内容' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    fireEvent.click(await screen.findByRole('button', { name: '查找已有草稿' }));
    expect(await screen.findByRole('button', { name: '比较已有草稿版本 7' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('本地不同内容');
    expect(fetchMock).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByRole('button', { name: '比较已有草稿版本 7' }));
    expect(await screen.findByRole('heading', { name: '草稿版本冲突' })).toBeInTheDocument();
    expect(screen.getByText('服务器已有草稿')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('本地不同内容');
    fireEvent.click(screen.getByRole('button', { name: '将本地更改重应用到最新版本' }));
    expect(await screen.findByText(/本地更改已明确重应用，当前版本 8/)).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('keeps the empty editor usable and reports local field validation without network writes', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    render(<ItemWorkspace gameId="demo" csrfToken="csrf" canEdit canReview={false} />);

    fireEvent.change(screen.getByRole('combobox', { name: '物品类型' }), {
      target: { value: 'equipment' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect(await screen.findByText('请先修正标记的字段。')).toBeInTheDocument();
    expect(screen.getByText('物品名称为必填项，最多 80 个字符。')).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
