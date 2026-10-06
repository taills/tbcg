import { useCallback, useEffect, useState } from 'react';
import type { ReactElement } from 'react';
import { AdminApiError } from '../../shared/admin-api';
import { contentApi, newIdempotencyKey } from '../../shared/content-api';
import type { ReleaseListing, SnapshotJob } from '../../shared/content-api';

interface ReleasePanelProps {
  gameId: string;
  csrfToken: string;
  canPublish: boolean;
  pollIntervalMs?: number;
}

const button =
  'rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50';
const DEFINITIVE = new Set([400, 401, 403, 404, 409, 422]);

/** 完整 Game 快照构建与 runtime owner 激活：激活只是请求，指针由游戏进程校验后确认。 */
export default function ReleasePanel({
  gameId,
  csrfToken,
  canPublish,
  pollIntervalMs = 2000,
}: ReleasePanelProps): ReactElement {
  const [listing, setListing] = useState<ReleaseListing | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [snapshotKey, setSnapshotKey] = useState<string | null>(null);
  const [job, setJob] = useState<SnapshotJob | null>(null);
  const [activationKey, setActivationKey] = useState<{ release: string; key: string } | null>(null);

  const refresh = useCallback(async (): Promise<void> => {
    try {
      setListing(await contentApi.listReleases(gameId));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '读取 Release 失败');
    }
  }, [gameId]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (job === null || job.status === 'built' || job.status === 'failed') return undefined;
    const timer = window.setTimeout(() => {
      void contentApi.getSnapshot(gameId, job.job_id).then((next) => {
        setJob(next);
        if (next.status === 'built' || next.status === 'failed') void refresh();
      });
    }, pollIntervalMs);
    return () => window.clearTimeout(timer);
  }, [gameId, job, pollIntervalMs, refresh]);

  const startSnapshot = async (): Promise<void> => {
    const key = snapshotKey ?? newIdempotencyKey();
    setSnapshotKey(key);
    setError(null);
    try {
      setJob(await contentApi.startSnapshot(gameId, key, csrfToken));
      setSnapshotKey(null);
    } catch (reason) {
      if (reason instanceof AdminApiError && DEFINITIVE.has(reason.status)) {
        setSnapshotKey(null);
      }
      setError(
        `${reason instanceof Error ? reason.message : '构建请求失败'}${snapshotKey !== null || !(reason instanceof AdminApiError) ? '（结果未知时可用同一请求重试）' : ''}`,
      );
    }
  };

  const activate = async (realmId: string, releaseId: string): Promise<void> => {
    const key = activationKey?.release === releaseId ? activationKey.key : newIdempotencyKey();
    setActivationKey({ release: releaseId, key });
    setError(null);
    try {
      await contentApi.requestActivation(gameId, realmId, releaseId, key, csrfToken);
      setActivationKey(null);
      await refresh();
    } catch (reason) {
      if (reason instanceof AdminApiError && DEFINITIVE.has(reason.status)) {
        setActivationKey(null);
      }
      setError(reason instanceof Error ? reason.message : '激活请求失败');
    }
  };

  return (
    <section
      className="rounded-xl border border-slate-200 bg-white p-5"
      aria-labelledby="releases-title"
    >
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold" id="releases-title">
          Release 与激活
        </h2>
        <button className={button} onClick={() => void refresh()} type="button">
          刷新
        </button>
      </div>
      {error !== null && (
        <p className="mt-2 text-sm text-red-700" role="alert">
          {error}
        </p>
      )}
      {canPublish && (
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <button className={button} onClick={() => void startSnapshot()} type="button">
            {snapshotKey !== null ? '用同一请求重试快照构建' : '构建完整 Game 快照'}
          </button>
          {job !== null && (
            <p className="text-sm" role="status">
              快照 v{job.release_version}（{job.entry_count} 条）：
              {job.status === 'built' ? '已构建，尚未激活' : job.status}
              {job.error_code !== null ? `（${job.error_code}）` : ''}
            </p>
          )}
        </div>
      )}
      {listing !== null && (
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <div>
            <h3 className="text-sm font-semibold">Realm 活动指针</h3>
            {listing.realms.map((realm) => (
              <p className="text-sm" key={realm.realm_id}>
                {realm.display_name}（{realm.realm_id}）：{realm.active_release_id ?? '未激活'}
              </p>
            ))}
            <h3 className="mt-3 text-sm font-semibold">激活请求</h3>
            {listing.activations.map((activation) => (
              <p className="text-xs" key={activation.request_id}>
                {activation.realm_id} ← {activation.release_id}：
                {activation.status === 'pending' ? '等待 runtime 确认' : activation.status}
                {activation.error_code !== null ? `（${activation.error_code}）` : ''}
              </p>
            ))}
          </div>
          <div>
            <h3 className="text-sm font-semibold">Release</h3>
            <ul className="space-y-1 text-xs" aria-label="Release 列表">
              {listing.releases.map((release) => (
                <li className="flex flex-wrap items-center gap-2" key={release.release_id}>
                  <span>
                    v{release.release_version} {release.release_id} · {release.scope ?? ''} ·{' '}
                    {release.status} · {release.entry_count} 条
                  </span>
                  {canPublish &&
                    release.scope === 'game_snapshot' &&
                    listing.realms.map((realm) => (
                      <button
                        className={button}
                        disabled={realm.active_release_id === release.release_id}
                        key={realm.realm_id}
                        onClick={() => void activate(realm.realm_id, release.release_id)}
                        type="button"
                      >
                        激活到 {realm.realm_id}
                      </button>
                    ))}
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}
    </section>
  );
}
