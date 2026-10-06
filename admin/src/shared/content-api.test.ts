import { afterEach, describe, expect, it, vi } from 'vitest';
import { contentApi, newIdempotencyKey } from './content-api';

function ok(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ success: true, data, error: null, meta: {} }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function stub(data: unknown): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn().mockResolvedValue(ok(data));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('contentApi', () => {
  it('builds stable request paths and headers', async () => {
    let fetchMock = stub([]);
    await contentApi.search('demo', 'guide', 'npc');
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/games/demo/content?q=guide&type=npc');

    fetchMock = stub({ changes: [{ pointer: '/public/text', kind: 'changed' }] });
    expect(await contentApi.diff('demo', 'demo:npc/guide', '1', 'draft')).toHaveLength(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('from=1&to=draft');

    fetchMock = stub({ content_id: 'demo:npc/guide', revision: 2, definition: { a: 1 } });
    expect(await contentApi.revision('demo', 'demo:npc/guide', 2)).toEqual({ a: 1 });

    fetchMock = stub({ draft_id: 'd1', draft_version: '1', definition: {} });
    await contentApi.createDraft('demo', 'demo:npc/x', { a: 1 }, 'csrf');
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('csrf');
    expect(JSON.parse(String(init.body))).toMatchObject({
      content_id: 'demo:npc/x',
      expected_draft_version: '0',
    });

    fetchMock = stub({ draft_id: 'd1', draft_version: '2', definition: {} });
    await contentApi.updateDraft(
      'demo',
      {
        draft_id: 'd1',
        draft_version: '1',
        game_id: 'demo',
        content_id: 'x',
        definition: {},
        updated_at: '',
      },
      { b: 2 },
      'csrf',
    );
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).method).toBe('PUT');

    stub({ valid: true, errors: [] });
    expect((await contentApi.validateDraft('demo', 'd1', 'csrf')).valid).toBe(true);
    stub({ revision: 1 });
    await contentApi.submitDraft(
      'demo',
      {
        draft_id: 'd1',
        draft_version: '2',
        game_id: 'demo',
        content_id: 'x',
        definition: {},
        updated_at: '',
      },
      'csrf',
    );
    stub({ source: 'draft', entry_count: 3, valid: true, issues: [] });
    expect((await contentApi.validationReport('demo', 'draft', 'csrf')).entry_count).toBe(3);
    stub({ outcome: 'victory' });
    expect((await contentApi.previewBattle('demo', 'draft', {}, 'csrf')).outcome).toBe('victory');
    stub(['item', 'npc']);
    expect(await contentApi.contentTypes()).toContain('npc');
    stub({ draft_id: 'd9', definition: {} });
    expect((await contentApi.getDraft('demo', 'd9')).draft_id).toBe('d9');
  });

  it('binds snapshot jobs and activations to the requested Game', async () => {
    const job = {
      job_id: 'j1',
      game_id: 'demo',
      kind: 'game_snapshot',
      release_id: 'r',
      release_version: 1,
      entry_count: 1,
      status: 'queued',
      public_artifact_sha256: null,
      error_code: null,
      created_at: '',
    };
    const fetchMock = stub(job);
    const key = newIdempotencyKey();
    await contentApi.startSnapshot('demo', key, 'csrf');
    expect(
      new Headers((fetchMock.mock.calls[0]?.[1] as RequestInit).headers).get('Idempotency-Key'),
    ).toBe(key);
    stub({ ...job, game_id: 'other' });
    await expect(contentApi.startSnapshot('demo', key, 'csrf')).rejects.toThrow(
      '快照任务与当前游戏不一致',
    );
    stub(job);
    await expect(contentApi.getSnapshot('demo', 'j2')).rejects.toThrow('快照任务身份不一致');
    stub({ request_id: 'a', status: 'pending' });
    await contentApi.requestActivation('demo', 'main', 'r', key, 'csrf');
    stub({ releases: 'bad' });
    await expect(contentApi.listReleases('demo')).rejects.toThrow();
    stub({ content_id: 'other', incoming: [], outgoing: [], broken: [] });
    await expect(contentApi.references('demo', 'demo:npc/guide')).rejects.toThrow(
      '引用结果与请求内容不一致',
    );
    stub({ notAnArray: true });
    await expect(contentApi.search('demo', '', '')).rejects.toThrow();
  });

  it('creates idempotency keys accepted by the server pattern', () => {
    const key = newIdempotencyKey();
    expect(key).toMatch(/^[A-Za-z0-9._~-]{16,128}$/);
    expect(newIdempotencyKey()).not.toBe(key);
  });
});
