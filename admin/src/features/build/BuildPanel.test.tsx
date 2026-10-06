import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import BuildPanel from './BuildPanel';

const hash = 'a'.repeat(64);
const candidate = {
  game_id: 'demo',
  content_id: 'demo:item/healing-potion',
  revision: 3,
  body_hash: hash,
  submitted_at: '2026-10-06T00:00:00Z',
};
const queuedJob = {
  job_id: '0ad049e6-1234-4abc-8000-000000000101',
  game_id: 'demo',
  content_id: candidate.content_id,
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

function ok(data: unknown, status = 200, meta: Record<string, unknown> = {}): Response {
  return new Response(JSON.stringify({ success: true, data, error: null, meta }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function failure(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({ success: false, data: null, error: { code, message, fields: [] }, meta: {} }),
    { status, headers: { 'Content-Type': 'application/json' } },
  );
}

const candidatePage = (): Response => ok([candidate], 200, { total: 1, limit: 50, offset: 0 });

afterEach(() => vi.unstubAllGlobals());

describe('BuildPanel', () => {
  it('starts an exact approved build and polls until built without implying activation', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(candidatePage())
      .mockResolvedValueOnce(ok(queuedJob, 202))
      .mockResolvedValueOnce(ok({ ...queuedJob, status: 'running' }))
      .mockResolvedValueOnce(
        ok({ ...queuedJob, status: 'built', public_artifact_sha256: 'b'.repeat(64) }),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<BuildPanel gameId="demo" csrfToken="csrf" pollIntervalMs={5} />);

    fireEvent.click(await screen.findByRole('button', { name: '构建 revision 3' }));
    expect(await screen.findByText(/已构建，尚未激活/)).toBeInTheDocument();
    expect(screen.getByText(new RegExp('b'.repeat(64)))).toBeInTheDocument();
    const [path, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(path).toBe('/api/v1/games/demo/builds');
    expect(JSON.parse(String(init.body))).toEqual({
      content_id: candidate.content_id,
      revision: 3,
      body_hash: hash,
    });
    expect(new Headers(init.headers).get('Idempotency-Key')).toMatch(/^[A-Za-z0-9._~-]{16,128}$/);
    expect(fetchMock.mock.calls[3]?.[0]).toBe(`/api/v1/games/demo/builds/${queuedJob.job_id}`);
  });

  it('keeps the original idempotency key and request after an unknown outcome', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(candidatePage())
      .mockResolvedValueOnce(failure(503, 'build_outcome_unknown', '构建请求结果未确认'))
      .mockRejectedValueOnce(new TypeError('network down'))
      .mockResolvedValueOnce(
        ok({ ...queuedJob, status: 'failed', error_code: 'invalid_frozen_input' }, 202),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<BuildPanel gameId="demo" csrfToken="csrf" pollIntervalMs={5} />);

    fireEvent.click(await screen.findByRole('button', { name: '构建 revision 3' }));
    expect(await screen.findByText(/结果未确认/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '构建 revision 3' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '用同一请求重试' }));
    expect(await screen.findByText(/无法连接管理服务/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '用同一请求重试' }));
    expect(await screen.findByText(/构建失败（invalid_frozen_input）/)).toBeInTheDocument();

    const keys = fetchMock.mock.calls
      .slice(1)
      .map(([, init]) => new Headers((init as RequestInit).headers).get('Idempotency-Key'));
    expect(new Set(keys).size).toBe(1);
    const bodies = fetchMock.mock.calls
      .slice(1)
      .map(([, init]) => String((init as RequestInit).body));
    expect(new Set(bodies).size).toBe(1);
  });

  it('allows a fresh request after a definitive rejection and reports candidate load failures', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(candidatePage())
      .mockResolvedValueOnce(failure(409, 'build_profile_unavailable', '该游戏缺少受控构建配置'))
      .mockResolvedValueOnce(ok(queuedJob, 202))
      .mockResolvedValue(
        ok({ ...queuedJob, status: 'built', public_artifact_sha256: 'c'.repeat(64) }),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<BuildPanel gameId="demo" csrfToken="csrf" pollIntervalMs={5} />);

    fireEvent.click(await screen.findByRole('button', { name: '构建 revision 3' }));
    expect(await screen.findByText('该游戏缺少受控构建配置')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '构建 revision 3' }));
    await waitFor(() => expect(screen.getByText(/已构建，尚未激活/)).toBeInTheDocument());
    const keys = fetchMock.mock.calls
      .slice(1, 3)
      .map(([, init]) => new Headers((init as RequestInit).headers).get('Idempotency-Key'));
    expect(keys[0]).not.toBe(keys[1]);
  });

  it('treats a server error as an unknown outcome and keeps the original request', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(candidatePage())
      .mockResolvedValueOnce(failure(500, 'internal_error', '管理服务暂时无法完成请求'))
      .mockResolvedValueOnce(
        ok({ ...queuedJob, status: 'built', public_artifact_sha256: 'd'.repeat(64) }, 202),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<BuildPanel gameId="demo" csrfToken="csrf" pollIntervalMs={5} />);

    fireEvent.click(await screen.findByRole('button', { name: '构建 revision 3' }));
    expect(await screen.findByText(/结果未确认/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '构建 revision 3' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '用同一请求重试' }));
    expect(await screen.findByText(/已构建，尚未激活/)).toBeInTheDocument();
    const keys = fetchMock.mock.calls
      .slice(1)
      .map(([, init]) => new Headers((init as RequestInit).headers).get('Idempotency-Key'));
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);
  });

  it('ignores poll responses whose identity drifts and offers a manual check after the poll limit', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(candidatePage())
      .mockResolvedValueOnce(ok(queuedJob, 202))
      .mockResolvedValueOnce(ok({ ...queuedJob, status: 'built', release_version: 99 }))
      .mockRejectedValueOnce(new TypeError('network down'))
      .mockResolvedValueOnce(
        ok({ ...queuedJob, status: 'built', public_artifact_sha256: 'e'.repeat(64) }),
      );
    vi.stubGlobal('fetch', fetchMock);
    render(<BuildPanel gameId="demo" csrfToken="csrf" pollIntervalMs={5} maxPolls={2} />);

    fireEvent.click(await screen.findByRole('button', { name: '构建 revision 3' }));
    expect(await screen.findByRole('button', { name: '检查状态' })).toBeInTheDocument();
    expect(screen.getByText(/状态：排队中/)).toBeInTheDocument();
    expect(screen.getByText(/状态读取失败/)).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(4);

    fireEvent.click(screen.getByRole('button', { name: '检查状态' }));
    expect(await screen.findByText(/已构建，尚未激活/)).toBeInTheDocument();
    expect(screen.queryByText(/状态读取失败/)).not.toBeInTheDocument();
  });

  it('stops polling after the panel unmounts', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(candidatePage())
      .mockResolvedValueOnce(ok(queuedJob, 202))
      .mockResolvedValue(ok({ ...queuedJob, status: 'running' }));
    vi.stubGlobal('fetch', fetchMock);
    const view = render(<BuildPanel gameId="demo" csrfToken="csrf" pollIntervalMs={5} />);

    fireEvent.click(await screen.findByRole('button', { name: '构建 revision 3' }));
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(3));
    view.unmount();
    const callsAtUnmount = fetchMock.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(fetchMock.mock.calls.length).toBeLessThanOrEqual(callsAtUnmount + 1);
    const settled = fetchMock.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(fetchMock.mock.calls.length).toBe(settled);
  });

  it('shows a load error when candidates are unavailable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(failure(403, 'forbidden', '当前账号没有此游戏的相应权限')),
    );
    render(<BuildPanel gameId="demo" csrfToken="csrf" />);
    expect(await screen.findByRole('alert')).toHaveTextContent('当前账号没有此游戏的相应权限');
  });
});
