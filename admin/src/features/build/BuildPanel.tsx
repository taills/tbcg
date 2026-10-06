import { useCallback, useEffect, useRef, useState } from 'react';
import type { ReactElement } from 'react';
import { AdminApiError, adminApi } from '../../shared/admin-api';
import type { BuildCandidate, BuildJob, StartBuildRequest } from '../../shared/admin-api';

const PAGE_SIZE = 50;
const DEFAULT_POLL_INTERVAL_MS = 2000;
const DEFAULT_MAX_POLLS = 90;
// Only these HTTP statuses are definitive rejections; anything else may have committed.
const DEFINITIVE_REJECTION_STATUSES = new Set([400, 401, 403, 404, 409]);

interface BuildPanelProps {
  gameId: string;
  csrfToken: string;
  pollIntervalMs?: number;
  maxPolls?: number;
}

interface PendingRequest {
  body: StartBuildRequest;
  idempotencyKey: string;
}

interface TrackedJob {
  kind: 'job';
  job: BuildJob;
  polls: number;
  pollError: string | null;
}

type CandidateState =
  | { kind: 'idle' }
  | { kind: 'sending'; request: PendingRequest }
  | { kind: 'unknown'; request: PendingRequest; message: string }
  | { kind: 'rejected'; message: string }
  | TrackedJob;

function candidateKey(candidate: BuildCandidate): string {
  return `${candidate.content_id}:${candidate.revision}:${candidate.body_hash}`;
}

function isTerminal(job: BuildJob): boolean {
  return job.status === 'built' || job.status === 'failed';
}

function statusLabel(job: BuildJob): string {
  switch (job.status) {
    case 'queued':
      return '排队中';
    case 'running':
      return '构建中';
    case 'retry_wait':
      return `等待重试${job.error_code === null ? '' : `（${job.error_code}）`}`;
    case 'built':
      return '已构建，尚未激活';
    case 'failed':
      return `构建失败（${job.error_code ?? 'unknown'}）`;
  }
}

/** A request may only be abandoned for a fresh key after a definitive client-side rejection. */
function isDefinitiveRejection(reason: unknown): boolean {
  return (
    reason instanceof AdminApiError &&
    DEFINITIVE_REJECTION_STATUSES.has(reason.status) &&
    reason.code !== 'build_outcome_unknown'
  );
}

function BuildPanelScope({
  gameId,
  csrfToken,
  pollIntervalMs = DEFAULT_POLL_INTERVAL_MS,
  maxPolls = DEFAULT_MAX_POLLS,
}: BuildPanelProps): ReactElement {
  const [candidates, setCandidates] = useState<readonly BuildCandidate[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [states, setStates] = useState<Readonly<Record<string, CandidateState>>>({});
  const [pollTick, setPollTick] = useState(0);
  const active = useRef(true);
  const pollInFlight = useRef(false);
  const loadGeneration = useRef(0);

  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);

  const setState = useCallback((key: string, next: CandidateState): void => {
    if (active.current) {
      setStates((current) => ({ ...current, [key]: next }));
    }
  }, []);

  const reload = useCallback(async (): Promise<void> => {
    const generation = ++loadGeneration.current;
    setLoading(true);
    setError(null);
    try {
      const page = await adminApi.listBuildCandidates(gameId, PAGE_SIZE, 0);
      if (active.current && generation === loadGeneration.current) {
        setCandidates(page.items);
        setTotal(page.total);
      }
    } catch (reason: unknown) {
      if (active.current && generation === loadGeneration.current) {
        setError(reason instanceof Error ? reason.message : '无法读取构建候选');
      }
    } finally {
      if (active.current && generation === loadGeneration.current) {
        setLoading(false);
      }
    }
  }, [gameId]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const send = async (key: string, request: PendingRequest): Promise<void> => {
    setState(key, { kind: 'sending', request });
    try {
      const job = await adminApi.startBuild(
        gameId,
        request.body,
        request.idempotencyKey,
        csrfToken,
      );
      setState(key, { kind: 'job', job, polls: 0, pollError: null });
    } catch (reason: unknown) {
      const message = reason instanceof Error ? reason.message : '构建请求失败';
      setState(
        key,
        isDefinitiveRejection(reason)
          ? { kind: 'rejected', message }
          : { kind: 'unknown', request, message },
      );
    }
  };

  const startBuild = (candidate: BuildCandidate): void => {
    const request: PendingRequest = {
      body: {
        content_id: candidate.content_id,
        revision: candidate.revision,
        body_hash: candidate.body_hash,
      },
      idempotencyKey: crypto.randomUUID(),
    };
    void send(candidateKey(candidate), request);
  };

  // Applies a poll result only to the same tracked job and never moves a terminal job backwards.
  const applyPoll = useCallback(
    (key: string, jobId: string, update: (tracked: TrackedJob) => TrackedJob): void => {
      if (!active.current) {
        return;
      }
      setStates((current) => {
        const tracked = current[key];
        if (tracked?.kind !== 'job' || tracked.job.job_id !== jobId || isTerminal(tracked.job)) {
          return current;
        }
        return { ...current, [key]: update(tracked) };
      });
    },
    [],
  );

  const resumePolling = (key: string, tracked: TrackedJob): void => {
    setState(key, { ...tracked, polls: 0, pollError: null });
  };

  useEffect(() => {
    if (pollInFlight.current) {
      return undefined;
    }
    const running = Object.entries(states).filter(
      (entry): entry is [string, TrackedJob] =>
        entry[1].kind === 'job' && !isTerminal(entry[1].job) && entry[1].polls < maxPolls,
    );
    if (running.length === 0) {
      return undefined;
    }
    const timer = window.setTimeout(() => {
      pollInFlight.current = true;
      void Promise.all(
        running.map(async ([key, tracked]) => {
          try {
            const job = await adminApi.getBuildJob(gameId, tracked.job);
            applyPoll(key, tracked.job.job_id, (current) => ({
              ...current,
              job,
              polls: current.polls + 1,
              pollError: null,
            }));
          } catch (reason: unknown) {
            const message = reason instanceof Error ? reason.message : '无法读取构建状态';
            applyPoll(key, tracked.job.job_id, (current) => ({
              ...current,
              polls: current.polls + 1,
              pollError: message,
            }));
          }
        }),
      ).finally(() => {
        pollInFlight.current = false;
        if (active.current) {
          setPollTick((current) => current + 1);
        }
      });
    }, pollIntervalMs);
    return () => window.clearTimeout(timer);
  }, [states, pollTick, gameId, pollIntervalMs, maxPolls, applyPoll]);

  return (
    <section
      aria-labelledby="build-panel-heading"
      className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm sm:p-6"
    >
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-100 pb-4">
        <div>
          <p className="text-xs font-semibold uppercase tracking-[0.14em] text-indigo-800">
            Publisher · {gameId}
          </p>
          <h2 className="mt-1 text-lg font-semibold text-slate-950" id="build-panel-heading">
            单物品构建
          </h2>
          <p className="mt-1 text-xs text-slate-600">
            仅构建已批准的确切 revision；构建结果不会激活，也不是完整游戏快照。
          </p>
        </div>
        <button
          className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700"
          onClick={() => void reload()}
          type="button"
        >
          刷新候选
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
      {loading ? (
        <p className="py-6 text-sm text-slate-600" role="status">
          正在读取已批准 revision…
        </p>
      ) : candidates.length === 0 ? (
        <p className="py-6 text-sm text-slate-600">当前没有可构建的已批准 revision。</p>
      ) : (
        <ul className="mt-4 divide-y divide-slate-100">
          {candidates.map((candidate, index) => {
            const key = candidateKey(candidate);
            const state = states[key] ?? { kind: 'idle' };
            const headingId = `build-candidate-${index}`;
            return (
              <li className="flex flex-col gap-3 py-4 first:pt-1" key={key}>
                <div className="min-w-0">
                  <h3 className="break-all text-sm font-semibold text-slate-900" id={headingId}>
                    {candidate.content_id}
                  </h3>
                  <p className="mt-1 text-xs text-slate-600">
                    revision <span className="font-mono">{candidate.revision}</span>
                  </p>
                  <p className="mt-1 break-all font-mono text-xs text-slate-500">
                    SHA-256：{candidate.body_hash}
                  </p>
                </div>
                <BuildState
                  describedBy={headingId}
                  maxPolls={maxPolls}
                  onResume={(tracked) => resumePolling(key, tracked)}
                  onRetry={(request) => void send(key, request)}
                  onStart={() => startBuild(candidate)}
                  revision={candidate.revision}
                  state={state}
                />
              </li>
            );
          })}
        </ul>
      )}
      {!loading && total > PAGE_SIZE && (
        <p className="mt-4 text-xs text-slate-600">
          显示最近 {PAGE_SIZE} / {total} 个已批准 revision。
        </p>
      )}
    </section>
  );
}

function BuildState({
  state,
  revision,
  describedBy,
  maxPolls,
  onStart,
  onRetry,
  onResume,
}: {
  state: CandidateState;
  revision: number;
  describedBy: string;
  maxPolls: number;
  onStart: () => void;
  onRetry: (request: PendingRequest) => void;
  onResume: (tracked: TrackedJob) => void;
}): ReactElement {
  const buttonClass =
    'rounded-md bg-indigo-800 px-3 py-2 text-sm font-semibold text-white hover:bg-indigo-900 focus:outline-none focus:ring-2 focus:ring-indigo-700 focus:ring-offset-2 disabled:opacity-60';
  const secondaryClass =
    'rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700';
  return (
    <div aria-live="polite" className="flex flex-col gap-2">
      {state.kind === 'sending' && (
        <p className="text-sm text-slate-600" role="status">
          正在提交构建请求…
        </p>
      )}
      {state.kind === 'unknown' && (
        <>
          <p className="text-sm text-amber-800" role="alert">
            构建请求结果未确认：{state.message}
          </p>
          <div>
            <button
              aria-describedby={describedBy}
              className={buttonClass}
              onClick={() => onRetry(state.request)}
              type="button"
            >
              用同一请求重试
            </button>
          </div>
        </>
      )}
      {state.kind === 'job' && (
        <div className="rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-800">
          <p role="status">
            状态：{statusLabel(state.job)} · Release {state.job.release_id}（版本{' '}
            {state.job.release_version}）
          </p>
          {state.job.public_artifact_sha256 !== null && (
            <p className="mt-1 break-all font-mono text-xs text-slate-600">
              公开包 SHA-256：{state.job.public_artifact_sha256}
            </p>
          )}
          {state.pollError !== null && (
            <p className="mt-1 text-xs text-amber-800">状态读取失败：{state.pollError}</p>
          )}
          {!isTerminal(state.job) && state.polls >= maxPolls && (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <p className="text-xs text-slate-600">已停止自动刷新。</p>
              <button
                aria-describedby={describedBy}
                className={secondaryClass}
                onClick={() => onResume(state)}
                type="button"
              >
                检查状态
              </button>
            </div>
          )}
        </div>
      )}
      {(state.kind === 'idle' || state.kind === 'rejected') && (
        <>
          {state.kind === 'rejected' && (
            <p className="text-sm text-red-800" role="alert">
              {state.message}
            </p>
          )}
          <div>
            <button
              aria-describedby={describedBy}
              className={buttonClass}
              onClick={onStart}
              type="button"
            >
              构建 revision {revision}
            </button>
          </div>
        </>
      )}
    </div>
  );
}

export default function BuildPanel(props: BuildPanelProps): ReactElement {
  return <BuildPanelScope key={props.gameId} {...props} />;
}
