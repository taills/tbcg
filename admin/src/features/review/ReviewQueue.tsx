import { useCallback, useEffect, useRef, useState } from 'react';
import type { ReactElement } from 'react';
import { adminApi } from '../../shared/admin-api';
import type { Page, Revision } from '../../shared/admin-api';

interface ReviewQueueProps {
  gameId: string;
  csrfToken: string;
}

function ReviewQueueScope({ gameId, csrfToken }: ReviewQueueProps): ReactElement {
  const [revisions, setRevisions] = useState<readonly Revision[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [pendingRevision, setPendingRevision] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const loadGeneration = useRef(0);

  const reload = useCallback(async (): Promise<void> => {
    const generation = ++loadGeneration.current;
    setLoading(true);
    setError(null);
    try {
      const page: Page<Revision> = await adminApi.listReviewQueue(gameId, 50, offset);
      if (generation === loadGeneration.current) {
        setRevisions(page.items);
        setTotal(page.total);
      }
    } catch (reason: unknown) {
      if (generation === loadGeneration.current) {
        setError(reason instanceof Error ? reason.message : '无法读取审核队列');
      }
    } finally {
      if (generation === loadGeneration.current) {
        setLoading(false);
      }
    }
  }, [gameId, offset]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const review = async (revision: Revision, decision: 'approved' | 'rejected'): Promise<void> => {
    const key = `${revision.content_id}:${revision.revision}`;
    setPendingRevision(key);
    setError(null);
    setNotice(null);
    try {
      await adminApi.reviewRevision(
        gameId,
        {
          content_id: revision.content_id,
          revision: revision.revision,
          body_hash: revision.body_hash,
          decision,
        },
        csrfToken,
      );
      setRevisions((current) =>
        current.filter(
          (item) => item.content_id !== revision.content_id || item.revision !== revision.revision,
        ),
      );
      setTotal((current) => Math.max(0, current - 1));
      setNotice(
        `已${decision === 'approved' ? '批准' : '拒绝'} revision ${revision.revision}（${revision.content_id}）。`,
      );
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : '审核操作失败');
    } finally {
      setPendingRevision(null);
    }
  };

  return (
    <section
      aria-labelledby="review-queue-heading"
      className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm sm:p-6"
    >
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 pb-4">
        <div>
          <p className="text-xs font-semibold uppercase tracking-[0.14em] text-amber-800">
            Reviewer · {gameId}
          </p>
          <h2 className="mt-1 text-lg font-semibold text-slate-950" id="review-queue-heading">
            待审核 revision
          </h2>
        </div>
        <button
          className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700"
          onClick={() => void reload()}
          type="button"
        >
          刷新队列
        </button>
      </div>
      {error !== null && (
        <p
          className="mt-4 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800"
          role="alert"
        >
          {error}
        </p>
      )}
      {notice !== null && (
        <p
          className="mt-4 rounded-md border border-teal-200 bg-teal-50 px-3 py-2 text-sm text-teal-900"
          role="status"
        >
          {notice}
        </p>
      )}
      {loading ? (
        <p className="py-6 text-sm text-slate-600" role="status">
          正在读取待审核内容…
        </p>
      ) : revisions.length === 0 ? (
        <p className="py-6 text-sm text-slate-600">当前没有待审核 revision。</p>
      ) : (
        <ul className="mt-4 divide-y divide-slate-100">
          {revisions.map((revision) => {
            const key = `${revision.content_id}:${revision.revision}`;
            const pending = pendingRevision === key;
            return (
              <li
                className="flex flex-col gap-3 py-4 first:pt-1 sm:flex-row sm:items-start sm:justify-between"
                key={key}
              >
                <div className="min-w-0">
                  <h3 className="break-all text-sm font-semibold text-slate-900">
                    {revision.content_id}
                  </h3>
                  <p className="mt-1 text-xs text-slate-600">
                    revision <span className="font-mono">{revision.revision}</span> ·{' '}
                    {new Date(revision.submitted_at).toLocaleString()}
                  </p>
                  <p className="mt-1 break-all font-mono text-xs text-slate-500">
                    SHA-256：{revision.body_hash}
                  </p>
                  <p className="mt-2 text-sm text-slate-700">
                    {revision.definition.public.display_name}
                  </p>
                </div>
                <div className="flex shrink-0 flex-wrap gap-2">
                  <button
                    className="rounded-md bg-teal-800 px-3 py-2 text-sm font-semibold text-white hover:bg-teal-900 focus:outline-none focus:ring-2 focus:ring-teal-700 focus:ring-offset-2 disabled:opacity-60"
                    disabled={pendingRevision !== null}
                    onClick={() => void review(revision, 'approved')}
                    type="button"
                  >
                    {pending ? '处理中…' : `批准 revision ${revision.revision}`}
                  </button>
                  <button
                    className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700 disabled:opacity-60"
                    disabled={pendingRevision !== null}
                    onClick={() => void review(revision, 'rejected')}
                    type="button"
                  >
                    拒绝
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
      {!loading && total > 50 && (
        <nav
          aria-label="审核队列分页"
          className="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-slate-100 pt-4"
        >
          <p className="text-xs text-slate-600">
            显示 {offset + 1}–{Math.min(offset + revisions.length, total)} / {total}
          </p>
          <div className="flex gap-2">
            <button
              className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 disabled:opacity-50"
              disabled={offset === 0}
              onClick={() => setOffset((current) => Math.max(0, current - 50))}
              type="button"
            >
              上一页
            </button>
            <button
              className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 disabled:opacity-50"
              disabled={offset + revisions.length >= total || offset >= 1000000}
              onClick={() => setOffset((current) => Math.min(1000000, current + 50))}
              type="button"
            >
              下一页
            </button>
          </div>
        </nav>
      )}
    </section>
  );
}

export default function ReviewQueue(props: ReviewQueueProps): ReactElement {
  return <ReviewQueueScope key={props.gameId} {...props} />;
}
