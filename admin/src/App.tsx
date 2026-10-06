import { useCallback, useEffect, useState } from 'react';
import type { ReactElement } from 'react';
import { AdminApiError, adminApi } from './shared/admin-api';
import type { AdminSession, GameRole } from './shared/admin-api';
import ItemWorkspace from './features/content/ItemWorkspace';
import type { WorkspaceStatus } from './features/content/ItemWorkspace';
import { clearPendingCreateRecovery } from './features/content/item-workspace-utils';
import LoginForm from './features/session/LoginForm';
import BuildPanel from './features/build/BuildPanel';
import ReviewQueue from './features/review/ReviewQueue';
import ContentLibrary from './features/library/ContentLibrary';
import AuthoringTools from './features/library/AuthoringTools';
import ReleasePanel from './features/releases/ReleasePanel';

function getMessage(error: unknown): string {
  if (error instanceof AdminApiError) {
    return error.message;
  }
  return error instanceof Error ? error.message : '管理服务暂不可用，请稍后重试';
}

function activeGameRole(session: AdminSession): GameRole | null {
  return session.game_roles.find((entry) => entry.roles.length > 0) ?? null;
}

export default function App(): ReactElement {
  const [session, setSession] = useState<AdminSession | null>(null);
  const [selectedGame, setSelectedGame] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showStudio, setShowStudio] = useState(false);
  const [workspaceStatuses, setWorkspaceStatuses] = useState<
    Readonly<Record<string, WorkspaceStatus>>
  >({});

  const reportWorkspaceStatus = useCallback((scopeId: string, status: WorkspaceStatus): void => {
    setWorkspaceStatuses((current) => {
      const previous = current[scopeId];
      if (previous?.dirty === status.dirty && previous.writePending === status.writePending) {
        return current;
      }
      return { ...current, [scopeId]: status };
    });
  }, []);

  useEffect(() => {
    let active = true;
    void adminApi
      .getSession()
      .then((value) => {
        if (active) {
          setSession(value);
          setError(null);
        }
      })
      .catch((reason: unknown) => {
        if (active) {
          setError(getMessage(reason));
        }
      })
      .finally(() => {
        if (active) {
          setLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, []);

  const login = async (username: string, password: string): Promise<void> => {
    if (session === null) {
      setError('安全会话尚未建立，请刷新页面后重试');
      return;
    }
    setPending(true);
    setError(null);
    try {
      const nextSession = await adminApi.login(username, password, session.csrf_token);
      setWorkspaceStatuses({});
      setSelectedGame(null);
      setSession(nextSession);
    } catch (reason: unknown) {
      setError(getMessage(reason));
    } finally {
      setPending(false);
    }
  };

  const logout = async (): Promise<void> => {
    if (session === null) {
      return;
    }
    const activeRole =
      selectedGame === null
        ? activeGameRole(session)
        : (session.game_roles.find((entry) => entry.game_id === selectedGame) ?? null);
    const activeWorkspaceStatus =
      activeRole === null ? undefined : workspaceStatuses[activeRole.game_id];
    if (activeWorkspaceStatus?.writePending) {
      return;
    }
    if (
      activeWorkspaceStatus?.dirty &&
      !window.confirm('当前游戏有尚未保存的本地编辑。退出登录将放弃这些编辑，是否继续？')
    ) {
      return;
    }
    setPending(true);
    setError(null);
    try {
      await adminApi.logout(session.csrf_token);
      const nextSession = await adminApi.getSession();
      if (activeRole !== null) {
        clearPendingCreateRecovery(activeRole.game_id, session.user?.admin_id ?? 'anonymous');
      }
      setWorkspaceStatuses({});
      setSelectedGame(null);
      setSession(nextSession);
    } catch (reason: unknown) {
      setError(getMessage(reason));
    } finally {
      setPending(false);
    }
  };

  if (loading) {
    return (
      <main className="min-h-screen bg-slate-50 px-4 py-8 text-slate-900 sm:px-6">
        <p className="mx-auto max-w-6xl text-sm text-slate-600" role="status">
          正在读取管理会话…
        </p>
      </main>
    );
  }

  if (session === null) {
    return (
      <main className="min-h-screen bg-slate-50 px-4 py-8 text-slate-900 sm:px-6">
        <p
          className="mx-auto max-w-md rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800"
          role="alert"
        >
          {error ?? '无法读取管理会话'}
        </p>
        <button
          className="mx-auto mt-4 block rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-medium"
          onClick={() => window.location.reload()}
          type="button"
        >
          重试
        </button>
      </main>
    );
  }

  if (!session.authenticated) {
    return (
      <main className="min-h-screen bg-slate-50 px-4 py-8 text-slate-900 sm:px-6">
        <LoginForm pending={pending} error={error} onLogin={login} />
      </main>
    );
  }

  const gameRole =
    selectedGame === null
      ? activeGameRole(session)
      : (session.game_roles.find((entry) => entry.game_id === selectedGame) ?? null);
  const roles = gameRole?.roles ?? [];
  const gameId = gameRole?.game_id ?? '';
  const canAuthor = roles.includes('Author');
  const canReview = roles.includes('Reviewer');
  const canPublish = roles.includes('Publisher');
  const storageOwnerId = session.user?.admin_id ?? 'anonymous';
  const currentWorkspaceStatus = workspaceStatuses[gameId] ?? {
    dirty: false,
    writePending: false,
  };

  return (
    <main className="min-h-screen bg-slate-50 text-slate-900">
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-4 px-4 py-4 sm:px-6">
          <div>
            <p className="text-xs font-semibold uppercase tracking-[0.16em] text-teal-800">
              TBCG · 内容控制面
            </p>
            <h1 className="mt-1 text-xl font-semibold tracking-tight text-slate-950">内容工作台</h1>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <div className="text-right">
              <p className="text-sm font-medium text-slate-900">{session.user?.username}</p>
              {session.game_roles.length > 1 ? (
                <label
                  className="mt-1 flex items-center justify-end gap-1.5 text-xs text-slate-600"
                  htmlFor="game-scope-select"
                >
                  游戏
                  <select
                    className="max-w-40 rounded border border-slate-300 bg-white px-1.5 py-1 text-xs text-slate-800 focus:outline-none focus:ring-2 focus:ring-teal-700"
                    id="game-scope-select"
                    disabled={currentWorkspaceStatus.writePending}
                    onChange={(event) => {
                      const nextGame = event.currentTarget.value;
                      if (nextGame === gameId || currentWorkspaceStatus.writePending) {
                        return;
                      }
                      if (
                        currentWorkspaceStatus.dirty &&
                        !window.confirm(
                          `游戏 ${gameId} 有尚未保存的本地编辑。离开将放弃这些编辑，是否继续？`,
                        )
                      ) {
                        return;
                      }
                      if (currentWorkspaceStatus.dirty) {
                        clearPendingCreateRecovery(gameId, storageOwnerId);
                        reportWorkspaceStatus(gameId, { dirty: false, writePending: false });
                      }
                      const url = new URL(window.location.href);
                      url.searchParams.delete('draftId');
                      window.history.replaceState(null, '', url);
                      setSelectedGame(nextGame);
                    }}
                    value={gameRole?.game_id ?? ''}
                  >
                    {session.game_roles.map((entry) => (
                      <option key={entry.game_id} value={entry.game_id}>
                        {entry.game_id}
                      </option>
                    ))}
                  </select>
                </label>
              ) : (
                <p className="text-xs text-slate-500">游戏：{gameId || '无授权游戏'}</p>
              )}
            </div>
            <div aria-label="当前角色" className="flex flex-wrap gap-1.5">
              {roles.map((role) => (
                <span
                  className="rounded-full bg-slate-100 px-2.5 py-1 text-xs font-medium text-slate-700"
                  key={role}
                >
                  {role}
                </span>
              ))}
            </div>
            <button
              className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700 focus:ring-offset-2 disabled:opacity-60"
              disabled={pending || currentWorkspaceStatus.writePending}
              onClick={() => void logout()}
              type="button"
            >
              {pending ? '处理中…' : '退出登录'}
            </button>
          </div>
        </div>
      </header>
      <div className="mx-auto max-w-7xl space-y-6 px-4 py-6 sm:px-6 sm:py-8">
        {error !== null && (
          <p
            className="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800"
            role="alert"
          >
            {error}
          </p>
        )}
        {gameRole === null ? (
          <section className="rounded-xl border border-slate-200 bg-white p-6">
            <h2 className="text-base font-semibold text-slate-900">当前账号没有游戏授权</h2>
            <p className="mt-2 text-sm leading-6 text-slate-600">
              请通过受控 bootstrap 为此账号绑定 Reader、Author、Reviewer 或 Publisher 角色。
            </p>
          </section>
        ) : (
          <>
            {canReview && (
              <ReviewQueue key={gameId} gameId={gameId} csrfToken={session.csrf_token} />
            )}
            {canPublish && (
              <BuildPanel
                key={`${gameId}:${session.user?.admin_id ?? 'anonymous'}`}
                gameId={gameId}
                csrfToken={session.csrf_token}
              />
            )}
            <section className="rounded-xl border border-slate-200 bg-white p-4">
              <button
                aria-expanded={showStudio}
                className="rounded-md border border-teal-700 px-3 py-1.5 text-sm font-medium text-teal-800 hover:bg-teal-50"
                onClick={() => setShowStudio((value) => !value)}
                type="button"
              >
                {showStudio ? '收起全类型内容库与发布工具' : '打开全类型内容库与发布工具'}
              </button>
            </section>
            {showStudio && (
              <>
                <ReleasePanel
                  key={`releases:${gameId}`}
                  gameId={gameId}
                  csrfToken={session.csrf_token}
                  canPublish={canPublish}
                />
                <ContentLibrary
                  key={`library:${gameId}`}
                  gameId={gameId}
                  csrfToken={session.csrf_token}
                  canEdit={canAuthor}
                />
                <AuthoringTools
                  key={`tools:${gameId}`}
                  gameId={gameId}
                  csrfToken={session.csrf_token}
                  canEdit={canAuthor}
                />
              </>
            )}
            <ItemWorkspace
              gameId={gameId}
              csrfToken={session.csrf_token}
              canEdit={canAuthor}
              canReview={canReview}
              storageOwnerId={storageOwnerId}
              onWorkspaceStatusChange={reportWorkspaceStatus}
            />
          </>
        )}
      </div>
    </main>
  );
}
