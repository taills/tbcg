import { afterEach, describe, expect, it, vi } from 'vitest';
import { AdminApiError, adminApi } from './admin-api';
import type { BuildJob } from './admin-api';

const hash = 'a'.repeat(64);
const job: BuildJob = {
  job_id: '0ad049e6-1234-4abc-8000-000000000101',
  game_id: 'demo',
  content_id: 'demo:item/healing-potion',
  revision: 3,
  body_hash: hash,
  release_id: 'build-0123456789abcdef0123456789abcdef',
  release_version: 4,
  status: 'queued',
  created_at: '2026-10-06T00:00:00Z',
  updated_at: '2026-10-06T00:00:00Z',
  public_artifact_sha256: null,
  error_code: null,
};

function envelope(data: unknown, meta: Record<string, unknown> = {}, status = 200): Response {
  return new Response(JSON.stringify({ success: status < 400, data, error: null, meta }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('adminApi build client', () => {
  it('lists Publisher build candidates with validated page metadata and Game scope', async () => {
    const candidate = {
      game_id: 'demo',
      content_id: 'demo:item/healing-potion',
      revision: 3,
      body_hash: hash,
      submitted_at: '2026-10-06T00:00:00Z',
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(envelope([candidate], { total: 1, limit: 20, offset: 0 }))
      .mockResolvedValueOnce(
        envelope([{ ...candidate, game_id: 'other' }], { total: 1, limit: 20, offset: 0 }),
      )
      .mockResolvedValueOnce(
        envelope([{ ...candidate, body_hash: 'A'.repeat(64) }], { total: 1, limit: 20, offset: 0 }),
      );
    vi.stubGlobal('fetch', fetchMock);

    const page = await adminApi.listBuildCandidates('demo', 20, 0);
    expect(page.items).toEqual([candidate]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      '/api/v1/games/demo/build-candidates?limit=20&offset=0',
    );
    await expect(adminApi.listBuildCandidates('demo', 20, 0)).rejects.toThrow('跨 Game');
    await expect(adminApi.listBuildCandidates('demo', 20, 0)).rejects.toThrow('构建候选');
  });

  it('sends the idempotency key and rejects jobs outside the exact requested revision', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(envelope(job, {}, 202))
      .mockResolvedValueOnce(envelope({ ...job, revision: 2 }, {}, 202));
    vi.stubGlobal('fetch', fetchMock);
    const input = { content_id: job.content_id, revision: job.revision, body_hash: hash };

    await expect(
      adminApi.startBuild('demo', input, 'build-request-key-0001', 'csrf-token'),
    ).resolves.toEqual(job);
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(path).toBe('/api/v1/games/demo/builds');
    expect(init.method).toBe('POST');
    expect(headers.get('Idempotency-Key')).toBe('build-request-key-0001');
    expect(headers.get('X-CSRF-Token')).toBe('csrf-token');
    expect(JSON.parse(String(init.body))).toEqual(input);
    await expect(
      adminApi.startBuild('demo', input, 'build-request-key-0001', 'csrf-token'),
    ).rejects.toThrow('超出构建请求范围');
    await expect(adminApi.startBuild('demo', input, 'short', 'csrf-token')).rejects.toThrow(
      'Idempotency-Key',
    );
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('reads one build job and rejects private fields or any identity drift', async () => {
    const built = { ...job, status: 'built', public_artifact_sha256: 'b'.repeat(64) };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(envelope(built))
      .mockResolvedValueOnce(envelope({ ...built, input_snapshot: { definition: {} } }))
      .mockResolvedValueOnce(envelope({ ...built, job_id: '0ad049e6-1234-4abc-8000-000000000999' }))
      .mockResolvedValueOnce(envelope({ ...built, status: 'activated' }))
      .mockResolvedValueOnce(envelope({ ...built, revision: 2 }))
      .mockResolvedValueOnce(envelope({ ...built, body_hash: 'c'.repeat(64) }))
      .mockResolvedValueOnce(envelope({ ...built, release_id: 'build-other' }))
      .mockResolvedValueOnce(envelope({ ...built, release_version: 9 }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(adminApi.getBuildJob('demo', job)).resolves.toEqual(built);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/games/demo/builds/${job.job_id}`);
    await expect(adminApi.getBuildJob('demo', job)).rejects.toThrow('构建任务');
    for (let index = 0; index < 6; index += 1) {
      await expect(adminApi.getBuildJob('demo', job)).rejects.toThrow('构建任务');
    }
  });

  it('surfaces outcome-unknown as a typed error so the caller can retry with the same key', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            success: false,
            data: null,
            error: {
              code: 'build_outcome_unknown',
              message: '请使用同一 Idempotency-Key 重试',
              fields: [],
            },
            meta: {},
          }),
          { status: 503, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    );
    const error = await adminApi
      .startBuild(
        'demo',
        { content_id: job.content_id, revision: 3, body_hash: hash },
        'build-request-key-0001',
        'csrf',
      )
      .catch((reason: unknown) => reason);
    expect(error).toBeInstanceOf(AdminApiError);
    expect((error as AdminApiError).code).toBe('build_outcome_unknown');
    expect((error as AdminApiError).status).toBe(503);
  });
});
