import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ReviewQueue from './ReviewQueue';

function response(data: unknown): Response {
  return new Response(
    JSON.stringify({
      success: true,
      data,
      error: null,
      meta: { total: Array.isArray(data) ? data.length : 0, limit: 50, offset: 0 },
    }),
    {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    },
  );
}

const revision = {
  game_id: 'demo',
  content_id: 'demo:item/healing-potion',
  revision: 7,
  body_hash: 'a'.repeat(64),
  definition: {
    game_id: 'demo',
    content_id: 'demo:item/healing-potion',
    revision: 7,
    schema_version: 1,
    public: {
      game_id: 'demo',
      content_id: 'demo:item/healing-potion',
      revision: 7,
      schema_version: 1,
      item_type: 'consumable',
      display_name: '回复药剂',
      icon_id: 'demo:icon/placeholder',
      stack_limit: 20,
      price_minor: '900',
    },
    server: { reward_value_minor: '250', drop_weight_basis_points: 1000 },
  },
  submitted_at: '2026-10-04T00:00:00Z',
};

afterEach(() => vi.unstubAllGlobals());

describe('ReviewQueue', () => {
  it('paginates large review queues with a bounded offset', async () => {
    const firstPage = new Response(
      JSON.stringify({
        success: true,
        data: [revision],
        error: null,
        meta: { total: 51, limit: 50, offset: 0 },
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    );
    const secondRevision = {
      ...revision,
      content_id: 'demo:item/second',
      revision: 1,
      definition: {
        ...revision.definition,
        content_id: 'demo:item/second',
        revision: 1,
        public: {
          ...revision.definition.public,
          content_id: 'demo:item/second',
          revision: 1,
        },
      },
    };
    const secondPage = new Response(
      JSON.stringify({
        success: true,
        data: [secondRevision],
        error: null,
        meta: { total: 51, limit: 50, offset: 50 },
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    );
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(firstPage)
      .mockResolvedValueOnce(secondPage)
      .mockResolvedValueOnce(firstPage);
    vi.stubGlobal('fetch', fetchMock);
    render(<ReviewQueue gameId="demo" csrfToken="review-csrf" />);

    expect(await screen.findByText('显示 1–1 / 51')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('显示 51–51 / 51')).toBeInTheDocument();
    expect(screen.getByText('demo:item/second')).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('offset=50');
    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    expect(await screen.findByText('显示 1–1 / 51')).toBeInTheDocument();
  });

  it('renders a safe fallback for non-Error transport failures and recovers on refresh', async () => {
    const fetchMock = vi.fn().mockRejectedValueOnce('offline').mockResolvedValueOnce(response([]));
    vi.stubGlobal('fetch', fetchMock);
    render(<ReviewQueue gameId="demo" csrfToken="review-csrf" />);

    expect(await screen.findByRole('alert')).toHaveTextContent('无法连接管理服务');
    fireEvent.click(screen.getByRole('button', { name: '刷新队列' }));
    expect(await screen.findByText('当前没有待审核 revision。')).toBeInTheDocument();
  });

  it('shows a recoverable queue error and preserves revisions when review fails', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([revision]))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            success: false,
            data: null,
            error: { code: 'revision_conflict', message: 'hash conflict', fields: [] },
            meta: {},
          }),
          { status: 409, headers: { 'Content-Type': 'application/json' } },
        ),
      )
      .mockResolvedValueOnce(response([]));
    vi.stubGlobal('fetch', fetchMock);
    render(<ReviewQueue gameId="demo" csrfToken="review-csrf" />);

    expect(await screen.findByText('demo:item/healing-potion')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '批准 revision 7' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('hash conflict');
    expect(screen.getByText('demo:item/healing-potion')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '刷新队列' }));
    expect(await screen.findByText('当前没有待审核 revision。')).toBeInTheDocument();
  });

  it('ignores a slower review queue response after switching Game scope', async () => {
    let resolveDemo: ((value: Response) => void) | undefined;
    let resolveOther: ((value: Response) => void) | undefined;
    const demoRevision = {
      ...revision,
      game_id: 'demo',
      content_id: 'demo:item/old',
      definition: {
        ...revision.definition,
        game_id: 'demo',
        content_id: 'demo:item/old',
        public: {
          ...revision.definition.public,
          game_id: 'demo',
          content_id: 'demo:item/old',
        },
      },
    };
    const otherRevision = {
      ...revision,
      game_id: 'other-game',
      content_id: 'other:item/current',
      definition: {
        ...revision.definition,
        game_id: 'other-game',
        content_id: 'other:item/current',
        public: {
          ...revision.definition.public,
          game_id: 'other-game',
          content_id: 'other:item/current',
        },
      },
    };
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveDemo = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveOther = resolve;
          }),
      );
    vi.stubGlobal('fetch', fetchMock);
    const { rerender } = render(<ReviewQueue gameId="demo" csrfToken="csrf" />);

    rerender(<ReviewQueue gameId="other-game" csrfToken="csrf" />);
    resolveOther?.(response([otherRevision]));
    expect(await screen.findByText('other:item/current')).toBeInTheDocument();
    resolveDemo?.(response([demoRevision]));

    await waitFor(() => expect(screen.queryByText('demo:item/old')).not.toBeInTheDocument());
    expect(screen.getByText('other:item/current')).toBeInTheDocument();
  });

  it('does not show a completed review from the previous Game scope', async () => {
    let resolveReview: ((value: Response) => void) | undefined;
    const currentRevision = {
      ...revision,
      game_id: 'other-game',
      content_id: 'other:item/current',
      definition: {
        ...revision.definition,
        game_id: 'other-game',
        content_id: 'other:item/current',
        public: {
          ...revision.definition.public,
          game_id: 'other-game',
          content_id: 'other:item/current',
        },
      },
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([revision]))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveReview = resolve;
          }),
      )
      .mockResolvedValueOnce(response([currentRevision]));
    vi.stubGlobal('fetch', fetchMock);
    const { rerender } = render(<ReviewQueue gameId="demo" csrfToken="csrf" />);

    expect(await screen.findByText('demo:item/healing-potion')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '批准 revision 7' }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    rerender(<ReviewQueue gameId="other-game" csrfToken="csrf" />);
    expect(await screen.findByText('other:item/current')).toBeInTheDocument();
    await act(async () => {
      resolveReview?.(
        response({
          game_id: revision.game_id,
          content_id: revision.content_id,
          revision: revision.revision,
          body_hash: revision.body_hash,
          decision: 'approved',
          reviewer_id: '00000000-0000-4000-8000-000000000001',
          reviewed_at: '2026-10-04T00:00:00Z',
        }),
      );
    });

    expect(screen.queryByText(/已批准 revision 7/)).not.toBeInTheDocument();
    expect(screen.getByText('other:item/current')).toBeInTheDocument();
  });

  it('shows a real pending revision and approves only its exact integer revision and hash', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([revision]))
      .mockResolvedValueOnce(
        response({
          game_id: 'demo',
          content_id: revision.content_id,
          revision: 7,
          body_hash: revision.body_hash,
          decision: 'approved',
          reviewer_id: '00000000-0000-4000-8000-000000000001',
          reviewed_at: '2026-10-04T00:00:00Z',
        }),
      )
      .mockResolvedValueOnce(response([]));
    vi.stubGlobal('fetch', fetchMock);
    render(<ReviewQueue gameId="demo" csrfToken="review-csrf" />);

    expect(await screen.findByText('demo:item/healing-potion')).toBeInTheDocument();
    expect(screen.getByText('SHA-256：' + revision.body_hash)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '批准 revision 7' }));
    expect(await screen.findByText(/已批准 revision 7/)).toBeInTheDocument();
    expect(await screen.findByText('当前没有待审核 revision。')).toBeInTheDocument();

    const [, init] = fetchMock.mock.calls[1] as [RequestInfo, RequestInit];
    const sent = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(sent).toMatchObject({
      content_id: revision.content_id,
      revision: 7,
      body_hash: revision.body_hash,
      decision: 'approved',
    });
    expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('review-csrf');
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});
