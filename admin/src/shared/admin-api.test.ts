import { afterEach, describe, expect, it, vi } from 'vitest';
import { AdminApiError, adminApi } from './admin-api';
import type { ItemDefinition } from './content/item-schema';

const itemDefinition: ItemDefinition = {
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
    display_name: '回复药剂',
    icon_id: 'demo:icon/placeholder',
    stack_limit: 20,
    price_minor: '9007199254740993',
  },
  server: { reward_value_minor: '250000000000000001', drop_weight_basis_points: 1250 },
};

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('adminApi', () => {
  it('keeps content_id in create body and sends CSRF with same-origin credentials', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({
        success: true,
        data: {
          draft_id: '0ad049e6-1234-4abc-8000-000000000001',
          game_id: 'demo',
          content_id: 'demo:item/healing-potion',
          draft_version: '1',
          definition: itemDefinition,
          updated_at: '2026-10-04T00:00:00Z',
        },
        error: null,
        meta: {},
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await adminApi.createItemDraft(
      'demo',
      {
        content_id: 'demo:item/healing-potion',
        expected_draft_version: '0',
        definition: itemDefinition,
      },
      'csrf-token',
    );

    const [url, init] = fetchMock.mock.calls[0] as [RequestInfo, RequestInit];
    expect(String(url)).toBe('/api/v1/games/demo/item-drafts');
    expect(JSON.parse(String(init.body))).toMatchObject({ content_id: 'demo:item/healing-potion' });
    expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('csrf-token');
    expect(init.credentials).toBe('same-origin');
  });

  it('preserves bigint CAS versions as decimal strings on update', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({
        success: true,
        data: {
          draft_id: '0ad049e6-1234-4abc-8000-000000000001',
          game_id: 'demo',
          content_id: 'demo:item/healing-potion',
          draft_version: '9007199254740994',
          definition: itemDefinition,
          updated_at: '2026-10-04T00:00:00Z',
        },
        error: null,
        meta: {},
      }),
    );
    vi.stubGlobal('fetch', fetchMock);
    const version = '9007199254740993';

    await adminApi.updateItemDraft(
      'demo',
      '0ad049e6-1234-4abc-8000-000000000001',
      {
        expected_draft_version: version,
        definition: itemDefinition,
      },
      'csrf-token',
    );

    const [url, init] = fetchMock.mock.calls[0] as [RequestInfo, RequestInit];
    expect(String(url)).toContain('/item-drafts/0ad049e6-1234-4abc-8000-000000000001');
    expect(JSON.parse(String(init.body)).expected_draft_version).toBe(version);
  });

  it('lists drafts for an exact content ID with validated pagination metadata', async () => {
    const draft = {
      draft_id: '0ad049e6-1234-4abc-8000-000000000001',
      game_id: 'demo',
      content_id: itemDefinition.content_id,
      draft_version: '9007199254740993',
      definition: itemDefinition,
      updated_at: '2026-10-04T00:00:00Z',
    };
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({
        success: true,
        data: [draft],
        error: null,
        meta: { total: 1, limit: 50, offset: 0 },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    const page = await adminApi.listItemDrafts('demo', itemDefinition.content_id, 50, 0);

    expect(page).toMatchObject({ items: [draft], total: 1, limit: 50, offset: 0 });
    const [url, init] = fetchMock.mock.calls[0] as [RequestInfo, RequestInit];
    expect(String(url)).toBe(
      '/api/v1/games/demo/item-drafts?content_id=demo%3Aitem%2Fhealing-potion&limit=50&offset=0',
    );
    expect(init.method).toBe('GET');
    expect(init.credentials).toBe('same-origin');
  });

  it('rejects malformed draft pages and drafts outside the requested Game/content scope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: [],
          error: null,
          meta: { total: -1, limit: 50, offset: 0 },
        }),
      ),
    );
    await expect(adminApi.listItemDrafts('demo', itemDefinition.content_id)).rejects.toThrow(
      '管理服务返回了无效草稿分页',
    );

    const outOfScopeDraft = {
      draft_id: '0ad049e6-1234-4abc-8000-000000000001',
      game_id: 'other-game',
      content_id: itemDefinition.content_id,
      draft_version: '1',
      definition: {
        ...itemDefinition,
        game_id: 'other-game',
        public: { ...itemDefinition.public, game_id: 'other-game' },
      },
      updated_at: '2026-10-04T00:00:00Z',
    };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: [outOfScopeDraft],
          error: null,
          meta: { total: 1, limit: 50, offset: 0 },
        }),
      ),
    );
    await expect(adminApi.listItemDrafts('demo', itemDefinition.content_id)).rejects.toThrow(
      '管理服务返回了跨 Game 或内容 ID 的草稿',
    );
  });

  it('validates session and queue payloads at the API boundary', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(jsonResponse({ success: true, data: {}, error: null, meta: {} })),
    );
    await expect(adminApi.getSession()).rejects.toThrow('管理服务返回了无效会话');

    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(
          jsonResponse({ success: true, data: { entries: [] }, error: null, meta: {} }),
        ),
    );
    await expect(adminApi.listReviewQueue('demo')).rejects.toThrow('管理服务返回了无效审核队列');
  });

  it('rejects out-of-contract roles, revisions, and approval records', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: {
            authenticated: true,
            csrf_token: 'csrf',
            user: { admin_id: 'id', username: 'author' },
            game_roles: [{ game_id: 'demo', roles: ['Owner'] }],
          },
          error: null,
          meta: {},
        }),
      ),
    );
    await expect(adminApi.getSession()).rejects.toThrow('管理服务返回了未知角色');

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: [
            {
              game_id: 'demo',
              content_id: 'demo:item/x',
              revision: '1',
              body_hash: 'a'.repeat(64),
              definition: itemDefinition,
              submitted_at: '2026-10-04T00:00:00Z',
            },
          ],
          error: null,
          meta: { total: 1, limit: 50, offset: 0 },
        }),
      ),
    );
    await expect(adminApi.listReviewQueue('demo')).rejects.toThrow(
      '管理服务返回了无效不可变 revision',
    );

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: {
            game_id: 'demo',
            content_id: itemDefinition.content_id,
            revision: 1,
            body_hash: 'a'.repeat(64),
            decision: 'pending',
            reviewer_id: 'id',
            reviewed_at: '2026-10-04T00:00:00Z',
          },
          error: null,
          meta: {},
        }),
      ),
    );
    await expect(
      adminApi.reviewRevision(
        'demo',
        {
          content_id: itemDefinition.content_id,
          revision: 1,
          body_hash: 'a'.repeat(64),
          decision: 'approved',
        },
        'csrf',
      ),
    ).rejects.toThrow('管理服务返回了无效审核记录');
  });

  it('rejects review queue entries outside the requested Game', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: [
            {
              game_id: 'other-game',
              content_id: itemDefinition.content_id,
              revision: 1,
              body_hash: 'a'.repeat(64),
              definition: {
                ...itemDefinition,
                game_id: 'other-game',
                public: { ...itemDefinition.public, game_id: 'other-game' },
              },
              submitted_at: '2026-10-04T00:00:00Z',
            },
          ],
          error: null,
          meta: { total: 1, limit: 50, offset: 0 },
        }),
      ),
    );

    await expect(adminApi.listReviewQueue('demo')).rejects.toThrow();
  });

  it.each([
    [
      'game_id',
      { game_id: 'demo' },
      {
        ...itemDefinition,
        game_id: 'other-game',
        public: { ...itemDefinition.public, game_id: 'other-game' },
      },
    ],
    [
      'content_id',
      { content_id: itemDefinition.content_id },
      {
        ...itemDefinition,
        content_id: 'demo:item/other',
        public: { ...itemDefinition.public, content_id: 'demo:item/other' },
      },
    ],
    [
      'revision',
      { revision: 1 },
      {
        ...itemDefinition,
        revision: 2,
        public: { ...itemDefinition.public, revision: 2 },
      },
    ],
  ])(
    'rejects revisions whose outer %s disagrees with the Definition',
    async (_identity, outerIdentity, definition) => {
      vi.stubGlobal(
        'fetch',
        vi.fn().mockResolvedValueOnce(
          jsonResponse({
            success: true,
            data: [
              {
                game_id: 'demo',
                content_id: itemDefinition.content_id,
                revision: 1,
                ...outerIdentity,
                body_hash: 'a'.repeat(64),
                definition,
                submitted_at: '2026-10-04T00:00:00Z',
              },
            ],
            error: null,
            meta: { total: 1, limit: 50, offset: 0 },
          }),
        ),
      );

      await expect(adminApi.listReviewQueue('demo')).rejects.toThrow();
    },
  );

  it.each([
    ['response Game outside request scope', 'other-game', 'other-game'],
    ['outer/Definition Game mismatch', 'demo', 'other-game'],
  ])('rejects submitted revisions with %s', async (_case, responseGame, definitionGame) => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: {
            game_id: responseGame,
            content_id: itemDefinition.content_id,
            revision: 1,
            body_hash: 'a'.repeat(64),
            definition: {
              ...itemDefinition,
              game_id: definitionGame,
              public: { ...itemDefinition.public, game_id: definitionGame },
            },
            submitted_at: '2026-10-04T00:00:00Z',
          },
          error: null,
          meta: {},
        }),
      ),
    );

    await expect(
      adminApi.submitItemDraft('demo', '0ad049e6-1234-4abc-8000-000000000001', '1', 'csrf'),
    ).rejects.toThrow();
  });

  it.each([
    ['Game', { game_id: 'other-game' }],
    ['content ID', { content_id: 'demo:item/other' }],
    ['revision', { revision: 2 }],
    ['body hash', { body_hash: 'b'.repeat(64) }],
    ['decision', { decision: 'rejected' }],
  ])('binds approval response to request %s', async (_scope, override) => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(
        jsonResponse({
          success: true,
          data: {
            game_id: 'demo',
            content_id: itemDefinition.content_id,
            revision: 1,
            body_hash: 'a'.repeat(64),
            decision: 'approved',
            reviewer_id: 'reviewer-id',
            reviewed_at: '2026-10-04T00:00:00Z',
            ...override,
          },
          error: null,
          meta: {},
        }),
      ),
    );

    await expect(
      adminApi.reviewRevision(
        'demo',
        {
          content_id: itemDefinition.content_id,
          revision: 1,
          body_hash: 'a'.repeat(64),
          decision: 'approved',
        },
        'csrf',
      ),
    ).rejects.toThrow();
  });

  it('normalizes malformed HTTP errors and rejects missing CSRF before sending', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(
        {
          success: false,
          data: null,
          error: { code: 4, message: null, fields: ['bad'] },
          meta: {},
        },
        500,
      ),
    );
    vi.stubGlobal('fetch', fetchMock);
    await expect(adminApi.login('author', 'password', 'csrf')).rejects.toMatchObject({
      status: 500,
      code: 'http_error',
      message: '请求失败（500）',
    });

    fetchMock.mockClear();
    await expect(adminApi.logout('')).rejects.toThrow('需要当前会话的 CSRF token');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('reports transport and malformed JSON errors without leaking response data', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('private transport detail')));
    await expect(adminApi.getSession()).rejects.toThrow('无法连接管理服务');

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('not-json', { status: 200 })));
    await expect(adminApi.getSession()).rejects.toThrow('无法读取的响应');
  });

  it('returns stale draft data in a typed 409 error without changing local inputs', async () => {
    const currentDraft = { draft_id: '0ad049e6-1234-4abc-8000-000000000001', draft_version: '2' };
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(
        {
          success: false,
          data: currentDraft,
          error: { code: 'draft_version_conflict', message: 'stale', fields: [] },
          meta: {},
        },
        409,
      ),
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(
      adminApi.updateItemDraft(
        'demo',
        currentDraft.draft_id,
        {
          expected_draft_version: '1',
          definition: itemDefinition,
        },
        'csrf-token',
      ),
    ).rejects.toMatchObject<Partial<AdminApiError>>({
      status: 409,
      code: 'draft_version_conflict',
      data: currentDraft,
    });
  });
});
