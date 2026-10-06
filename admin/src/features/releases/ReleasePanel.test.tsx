import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ReleasePanel from './ReleasePanel';
import ContentLibrary from '../library/ContentLibrary';
import AuthoringTools from '../library/AuthoringTools';

function ok(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ success: true, data, error: null, meta: {} }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const listing = {
  releases: [
    {
      release_id: 'snapshot-1',
      release_version: 5,
      status: 'built',
      scope: 'game_snapshot',
      entry_count: 39,
      created_at: '2026-10-07T00:00:00Z',
      activated_at: null,
      public_artifact_sha256: 'a'.repeat(64),
    },
  ],
  realms: [{ realm_id: 'main', display_name: '主服', active_release_id: null }],
  snapshot_jobs: [],
  activations: [],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ReleasePanel', () => {
  it('requests runtime activation with an idempotency key and shows the pending state', async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const url = String(input);
        if (url.endsWith('/activations')) {
          const headers = new Headers(init?.headers);
          expect(headers.get('Idempotency-Key')).toMatch(/^adm-[0-9a-f]{36}$/);
          expect(init?.body).toBe(JSON.stringify({ release_id: 'snapshot-1' }));
          return ok({ request_id: 'r1', status: 'pending' }, 202);
        }
        return ok(
          fetchMock.mock.calls.length > 2
            ? {
                ...listing,
                activations: [
                  {
                    request_id: 'r1',
                    realm_id: 'main',
                    release_id: 'snapshot-1',
                    status: 'pending',
                    error_code: null,
                    previous_release_id: null,
                    created_at: '2026-10-07T00:00:00Z',
                    decided_at: null,
                  },
                ],
              }
            : listing,
        );
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    render(<ReleasePanel gameId="demo" csrfToken="csrf" canPublish />);
    fireEvent.click(await screen.findByRole('button', { name: '激活到 main' }));
    expect(await screen.findByText(/等待 runtime 确认/)).toBeInTheDocument();
  });

  it('hides publish controls for non-publishers', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(ok(listing)));
    render(<ReleasePanel gameId="demo" csrfToken="csrf" canPublish={false} />);
    expect(await screen.findByText(/snapshot-1/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '构建完整 Game 快照' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '激活到 main' })).not.toBeInTheDocument();
  });
});

describe('ContentLibrary', () => {
  it('opens a content entry and shows broken references', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
        const url = String(input);
        if (url === '/api/v1/content-types') return ok(['npc', 'quest']);
        if (url.includes('/content?')) {
          return ok([
            {
              content_id: 'demo:npc/guide',
              content_type: 'npc',
              display_name: '引路人',
              latest_revision: 1,
              approved_revision: 1,
              updated_at: '2026-10-07T00:00:00Z',
            },
          ]);
        }
        if (url.includes('/content-revisions?')) {
          return ok({
            content_id: 'demo:npc/guide',
            revision: 1,
            definition: {
              content_id: 'demo:npc/guide',
              server: {
                start_node: 'root',
                nodes: [{ node_id: 'root', text: '你好', options: [] }],
              },
            },
          });
        }
        if (url.includes('/content-references?')) {
          return ok({
            content_id: 'demo:npc/guide',
            incoming: [],
            outgoing: [],
            broken: [{ content_id: 'demo:quest/missing', pointer: '/server/x' }],
          });
        }
        return ok({});
      }),
    );
    render(<ContentLibrary gameId="demo" csrfToken="csrf" canEdit={false} />);
    fireEvent.click(await screen.findByRole('button', { name: /引路人/ }));
    expect(await screen.findByLabelText('节点 root 文本')).toHaveValue('你好');
    await waitFor(() => expect(screen.getByText(/断开引用（1）/)).toBeInTheDocument());
  });
});

describe('AuthoringTools', () => {
  it('runs an import dry run without creating drafts', async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        expect(JSON.parse(String(init?.body))).toMatchObject({ dry_run: true });
        return ok([{ content_id: 'demo:item/x', status: 'would_create', errors: [] }]);
      },
    );
    vi.stubGlobal('fetch', fetchMock);
    render(<AuthoringTools gameId="demo" csrfToken="csrf" canEdit />);
    fireEvent.change(screen.getByLabelText('批量导入 JSON'), {
      target: { value: '[{"content_id":"demo:item/x"}]' },
    });
    fireEvent.click(screen.getByRole('button', { name: '试运行' }));
    expect(await screen.findByText('demo:item/x：would_create')).toBeInTheDocument();
  });
});
