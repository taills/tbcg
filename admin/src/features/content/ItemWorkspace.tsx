import { useEffect, useRef, useState } from 'react';
import type { ReactElement } from 'react';
import { AdminApiError, adminApi } from '../../shared/admin-api';
import type { Draft, Revision } from '../../shared/admin-api';
import {
  createItemDefinition,
  definitionToForm,
  emptyItemForm,
} from '../../shared/content/item-form';
import type { ItemFormField, ItemFormValues } from '../../shared/content/item-form';
import ConflictPanel from './ConflictPanel';
import CreateRecoveryPanel from './CreateRecoveryPanel';
import ItemEditorForm from './ItemEditorForm';
import {
  apiFieldErrors,
  fieldToPointer,
  getDraftIdFromLocation,
  getRequestMessage,
  itemFormErrors,
  itemFormsEqual,
  parseDraft,
  readOnlyRoleMessage,
  readPendingCreateRecovery,
  rememberDraftId,
  savePendingCreateRecovery,
  clearPendingCreateRecovery,
  serverFieldErrors,
  validationIssuesFromDefinition,
} from './item-workspace-utils';
import type { DraftConflict } from './item-workspace-utils';
import type { CreateRecoveryState } from './CreateRecoveryPanel';

export interface WorkspaceStatus {
  dirty: boolean;
  writePending: boolean;
}

interface ItemWorkspaceProps {
  gameId: string;
  csrfToken: string;
  canEdit: boolean;
  canReview: boolean;
  storageOwnerId?: string;
  onWorkspaceStatusChange?: (gameId: string, status: WorkspaceStatus) => void;
}

interface ItemWorkspaceScopeProps extends ItemWorkspaceProps {
  initialDraftId: string | null;
}

const defaultContentId = 'demo:item/healing-potion';

function ItemWorkspaceScope({
  gameId,
  csrfToken,
  canEdit,
  canReview,
  storageOwnerId = 'local',
  onWorkspaceStatusChange,
  initialDraftId,
}: ItemWorkspaceScopeProps): ReactElement {
  const initialContentId =
    new URLSearchParams(window.location.search).get('contentId') ?? defaultContentId;
  const [restoredRecovery] = useState<CreateRecoveryState | null>(() => {
    if (!canEdit || initialDraftId !== null) {
      return null;
    }
    const stored = readPendingCreateRecovery(gameId, storageOwnerId);
    return stored === null ? null : { ...stored, candidates: [], status: 'pending' };
  });
  const [form, setForm] = useState<ItemFormValues>(
    () => restoredRecovery?.local ?? emptyItemForm(initialContentId),
  );
  const [draft, setDraft] = useState<Draft | null>(null);
  const [revision, setRevision] = useState<Revision | null>(null);
  const [conflict, setConflict] = useState<DraftConflict | null>(null);
  const [createRecovery, setCreateRecovery] = useState<CreateRecoveryState | null>(
    restoredRecovery,
  );
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [validationSummary, setValidationSummary] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(initialDraftId !== null);
  const [pending, setPending] = useState(false);
  const [writePending, setWritePending] = useState(false);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    if (!canEdit || initialDraftId !== null) {
      clearPendingCreateRecovery(gameId, storageOwnerId);
    }
  }, [canEdit, gameId, initialDraftId, storageOwnerId]);

  const currentDraftLinkId = useRef<string | null>(initialDraftId);

  useEffect(() => {
    if (initialDraftId === null) {
      if (currentDraftLinkId.current !== null) {
        currentDraftLinkId.current = null;
        setDraft(null);
        setRevision(null);
        setConflict(null);
        setCreateRecovery(null);
        setForm(emptyItemForm(initialContentId));
        setFieldErrors({});
        setValidationSummary(null);
        setError(null);
      }
      setLoading(false);
      return;
    }
    if (draft?.draft_id === initialDraftId) {
      currentDraftLinkId.current = initialDraftId;
      return;
    }
    currentDraftLinkId.current = initialDraftId;
    let active = true;
    setLoading(true);
    setDraft(null);
    setRevision(null);
    setConflict(null);
    setCreateRecovery(null);
    setForm(emptyItemForm(initialContentId));
    setFieldErrors({});
    setValidationSummary(null);
    setError(null);
    void adminApi
      .getItemDraft(gameId, initialDraftId)
      .then((value) => {
        const loaded = parseDraft(value);
        if (loaded.game_id !== gameId || loaded.draft_id !== initialDraftId) {
          throw new Error('管理服务返回了不属于当前 Game 草稿链接的内容');
        }
        if (active) {
          rememberDraftId(gameId, loaded.draft_id, false);
          setDraft(loaded);
          setForm(definitionToForm(loaded.definition));
          setError(null);
        }
      })
      .catch((reason: unknown) => {
        if (active) {
          setError(reason instanceof Error ? reason.message : '无法读取草稿');
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
  }, [gameId, initialDraftId]);

  const savedForm =
    draft === null ? emptyItemForm(initialContentId) : definitionToForm(draft.definition);
  const dirty = !itemFormsEqual(form, savedForm);

  useEffect(() => {
    onWorkspaceStatusChange?.(gameId, { dirty, writePending });
  }, [dirty, gameId, onWorkspaceStatusChange, writePending]);

  useEffect(() => {
    if (!dirty && !writePending) {
      return;
    }
    const warnBeforeLeaving = (event: BeforeUnloadEvent): void => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', warnBeforeLeaving);
    return () => window.removeEventListener('beforeunload', warnBeforeLeaving);
  }, [dirty, writePending]);

  const updateField = (field: ItemFormField, value: string): void => {
    setForm((current) => ({ ...current, [field]: value }));
    setFieldErrors((current) => {
      const path = fieldToPointer(field);
      if (path === null || current[path] === undefined) {
        return current;
      }
      const { [path]: _removed, ...remaining } = current;
      return remaining;
    });
    setError(null);
    setValidationSummary(null);
  };

  const captureConflict = (reason: unknown, local: ItemFormValues): boolean => {
    if (!(reason instanceof AdminApiError) || reason.status !== 409) {
      return false;
    }
    try {
      const latest = parseDraft(reason.data);
      setConflict({ current: latest, local: { ...local } });
      return true;
    } catch {
      setError('草稿已发生冲突，但服务器没有返回可比较的最新版本。请重新打开草稿。');
      return true;
    }
  };

  const findExistingDrafts = async (): Promise<void> => {
    if (createRecovery === null || createRecovery.status === 'loading') {
      return;
    }
    const contentId = createRecovery.contentId;
    setError(null);
    setCreateRecovery({ ...createRecovery, status: 'loading', candidates: [] });
    try {
      const page = await adminApi.listItemDrafts(gameId, contentId, 50, 0);
      if (mounted.current) {
        setCreateRecovery((current) =>
          current === null
            ? null
            : {
                ...current,
                candidates: page.items,
                status: page.items.length === 0 ? 'empty' : 'found',
              },
        );
      }
    } catch (reason: unknown) {
      if (mounted.current) {
        setCreateRecovery((current) =>
          current === null ? null : { ...current, candidates: [], status: 'error' },
        );
        setError(getRequestMessage(reason));
      }
    }
  };

  const compareExistingDraft = (candidate: Draft): void => {
    if (createRecovery === null) {
      return;
    }
    rememberDraftId(gameId, candidate.draft_id, mounted.current);
    clearPendingCreateRecovery(gameId, storageOwnerId);
    setConflict({ current: candidate, local: { ...createRecovery.local } });
    setCreateRecovery(null);
    setError(null);
  };

  const persistDraft = async (values: ItemFormValues): Promise<Draft | null> => {
    const built = createItemDefinition(gameId, values);
    if (built.definition === undefined) {
      setFieldErrors(itemFormErrors(built.errors));
      setValidationSummary('请先修正标记的字段。');
      return null;
    }
    const schemaIssues = validationIssuesFromDefinition(built.definition);
    if (Object.keys(schemaIssues).length > 0) {
      setFieldErrors(schemaIssues);
      setValidationSummary('字段不符合共享物品 Schema。');
      return null;
    }
    if (draft !== null && itemFormsEqual(values, definitionToForm(draft.definition))) {
      return draft;
    }

    setPending(true);
    setWritePending(true);
    setError(null);
    try {
      const saved =
        draft === null
          ? await adminApi.createItemDraft(
              gameId,
              {
                content_id: values.contentId.trim(),
                expected_draft_version: '0',
                definition: built.definition,
              },
              csrfToken,
            )
          : await adminApi.updateItemDraft(
              gameId,
              draft.draft_id,
              {
                expected_draft_version: draft.draft_version,
                definition: built.definition,
              },
              csrfToken,
            );
      const parsed = parseDraft(saved);
      setDraft(parsed);
      setForm(definitionToForm(parsed.definition));
      setConflict(null);
      setCreateRecovery(null);
      clearPendingCreateRecovery(gameId, storageOwnerId);
      setFieldErrors({});
      if (draft === null) {
        rememberDraftId(gameId, parsed.draft_id, mounted.current);
      }
      return parsed;
    } catch (reason: unknown) {
      if (draft !== null && captureConflict(reason, values)) {
        return null;
      }
      if (draft === null && isUnknownCreateOutcome(reason)) {
        const recovery = { contentId: values.contentId.trim(), local: { ...values } };
        savePendingCreateRecovery(gameId, storageOwnerId, recovery);
        setCreateRecovery({ ...recovery, candidates: [], status: 'pending' });
      }
      if (reason instanceof AdminApiError && reason.fields.length > 0) {
        setFieldErrors(apiFieldErrors(reason));
      }
      setError(getRequestMessage(reason));
      return null;
    } finally {
      setWritePending(false);
      setPending(false);
    }
  };

  const save = async (): Promise<void> => {
    const saved = await persistDraft(form);
    if (saved !== null) {
      setValidationSummary(`草稿已保存，版本 ${saved.draft_version}。`);
    }
  };

  const validate = async (): Promise<void> => {
    const currentDraft = await persistDraft(form);
    if (currentDraft === null) {
      return;
    }
    setPending(true);
    try {
      const result = await adminApi.validateItemDraft(gameId, currentDraft.draft_id, csrfToken);
      setFieldErrors(serverFieldErrors(result));
      setValidationSummary(
        result.valid ? '校验通过。可以提交当前草稿 revision。' : '校验未通过，请修正对应字段。',
      );
    } catch (reason: unknown) {
      setError(getRequestMessage(reason));
    } finally {
      setPending(false);
    }
  };

  const submit = async (): Promise<void> => {
    const saved = await persistDraft(form);
    if (saved === null) {
      return;
    }
    setPending(true);
    setWritePending(true);
    setError(null);
    try {
      const submitted = await adminApi.submitItemDraft(
        gameId,
        saved.draft_id,
        saved.draft_version,
        csrfToken,
      );
      setRevision(submitted);
      setValidationSummary(`已提交不可变 revision ${submitted.revision}；审批将绑定此 hash。`);
    } catch (reason: unknown) {
      if (!captureConflict(reason, form)) {
        if (reason instanceof AdminApiError && reason.fields.length > 0) {
          setFieldErrors(apiFieldErrors(reason));
        }
        setError(getRequestMessage(reason));
      }
    } finally {
      setWritePending(false);
      setPending(false);
    }
  };

  const useServerVersion = (): void => {
    if (conflict === null) {
      return;
    }
    setDraft(conflict.current);
    setForm(definitionToForm(conflict.current.definition));
    setConflict(null);
    setCreateRecovery(null);
    clearPendingCreateRecovery(gameId, storageOwnerId);
    setFieldErrors({});
    setError(null);
    setValidationSummary(`已载入服务器草稿版本 ${conflict.current.draft_version}。`);
  };

  const reapplyLocalChanges = async (): Promise<void> => {
    if (conflict === null) {
      return;
    }
    const currentConflict = conflict;
    const built = createItemDefinition(gameId, currentConflict.local);
    if (built.definition === undefined) {
      setFieldErrors(itemFormErrors(built.errors));
      setValidationSummary('本地输入仍有字段错误，修正后再重应用。');
      return;
    }
    const schemaErrors = validationIssuesFromDefinition(built.definition);
    if (Object.keys(schemaErrors).length > 0) {
      setFieldErrors(schemaErrors);
      setValidationSummary('本地内容不符合共享物品 Schema，修正后再重应用。');
      return;
    }
    setPending(true);
    setWritePending(true);
    try {
      const saved = await adminApi.updateItemDraft(
        gameId,
        currentConflict.current.draft_id,
        {
          expected_draft_version: currentConflict.current.draft_version,
          definition: built.definition,
        },
        csrfToken,
      );
      const parsed = parseDraft(saved);
      setDraft(parsed);
      setForm(definitionToForm(parsed.definition));
      setConflict(null);
      setCreateRecovery(null);
      clearPendingCreateRecovery(gameId, storageOwnerId);
      setFieldErrors({});
      setValidationSummary(`本地更改已明确重应用，当前版本 ${parsed.draft_version}。`);
    } catch (reason: unknown) {
      if (!captureConflict(reason, currentConflict.local)) {
        if (reason instanceof AdminApiError && reason.fields.length > 0) {
          setFieldErrors(apiFieldErrors(reason));
        }
        setError(getRequestMessage(reason));
      }
    } finally {
      setWritePending(false);
      setPending(false);
    }
  };

  if (loading) {
    return (
      <section
        className="rounded-xl border border-slate-200 bg-white p-5 text-sm text-slate-600"
        role="status"
      >
        正在读取物品草稿…
      </section>
    );
  }

  return (
    <section
      aria-labelledby="item-editor-heading"
      className="min-w-0 rounded-xl border border-slate-200 bg-white p-4 shadow-sm sm:p-6"
    >
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-slate-100 pb-4">
        <div>
          <p className="text-xs font-semibold uppercase tracking-[0.14em] text-teal-800">
            物品 Definition
          </p>
          <h2 className="mt-1 text-lg font-semibold text-slate-950" id="item-editor-heading">
            {draft === null ? '新建物品草稿' : '编辑物品草稿'}
          </h2>
          <p className="mt-1 text-sm text-slate-600">
            {draft === null ? (
              '内容 ID 由作者提供，revision 由服务端分配。'
            ) : (
              <>
                草稿版本 <span className="font-mono">{draft.draft_version}</span> · ID{' '}
                <span className="font-mono">{draft.draft_id}</span>
              </>
            )}
          </p>
        </div>
        {draft !== null && (
          <a
            className="max-w-full break-all text-xs text-teal-800 underline underline-offset-2"
            href={`?draftId=${encodeURIComponent(draft.draft_id)}`}
          >
            复制草稿链接
          </a>
        )}
      </div>

      {!canEdit && (
        <p className="mt-4 rounded-md bg-slate-100 px-3 py-2 text-sm text-slate-700">
          {readOnlyRoleMessage(canReview)}
        </p>
      )}
      {error !== null && (
        <p
          className="mt-4 rounded-md border border-red-200 bg-red-50 px-3 py-2.5 text-sm text-red-800"
          role="alert"
        >
          {error}
        </p>
      )}
      {createRecovery !== null && (
        <CreateRecoveryPanel
          recovery={createRecovery}
          onFind={() => void findExistingDrafts()}
          onCompare={compareExistingDraft}
        />
      )}
      {validationSummary !== null && (
        <p
          className="mt-4 rounded-md border border-teal-200 bg-teal-50 px-3 py-2.5 text-sm text-teal-900"
          role="status"
        >
          {validationSummary}
        </p>
      )}
      {conflict !== null && (
        <div className="mt-4">
          <ConflictPanel
            conflict={conflict}
            pending={pending}
            onUseServer={useServerVersion}
            onReapply={() => void reapplyLocalChanges()}
          />
        </div>
      )}

      <div className="mt-5">
        <ItemEditorForm
          values={form}
          fieldErrors={fieldErrors}
          readOnly={!canEdit || pending || conflict !== null || createRecovery !== null}
          lockContentId={draft !== null}
          onChange={updateField}
        />
      </div>

      {canEdit && (
        <div className="mt-6 flex flex-wrap gap-2 border-t border-slate-100 pt-4">
          <button
            className="rounded-md bg-teal-800 px-4 py-2.5 text-sm font-semibold text-white hover:bg-teal-900 focus:outline-none focus:ring-2 focus:ring-teal-700 focus:ring-offset-2 disabled:cursor-wait disabled:opacity-60"
            disabled={pending || conflict !== null || createRecovery !== null}
            onClick={() => void save()}
            type="button"
          >
            {pending ? '正在保存…' : '保存草稿'}
          </button>
          <button
            className="rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700 focus:ring-offset-2 disabled:cursor-wait disabled:opacity-60"
            disabled={pending || conflict !== null || createRecovery !== null}
            onClick={() => void validate()}
            type="button"
          >
            校验草稿
          </button>
          <button
            className="rounded-md border border-slate-300 bg-white px-4 py-2.5 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700 focus:ring-offset-2 disabled:cursor-wait disabled:opacity-60"
            disabled={pending || draft === null || conflict !== null || createRecovery !== null}
            onClick={() => void submit()}
            type="button"
          >
            提交不可变 revision
          </button>
        </div>
      )}

      {revision !== null && (
        <section
          aria-label="已提交 revision"
          className="mt-5 rounded-lg border border-teal-200 bg-teal-50 p-4"
        >
          <h3 className="text-sm font-semibold text-teal-950">
            revision {revision.revision} 已提交
          </h3>
          <p className="mt-1 break-all font-mono text-xs text-teal-900">
            SHA-256：{revision.body_hash}
          </p>
          <p className="mt-2 text-xs text-teal-800">
            此 revision 不可原地修改；审核将针对以上内容 hash。
          </p>
        </section>
      )}
    </section>
  );
}

function isUnknownCreateOutcome(reason: unknown): boolean {
  if (!(reason instanceof AdminApiError)) {
    return true;
  }
  return reason.status === 408 || reason.status === 409 || reason.status >= 500;
}

export default function ItemWorkspace(props: ItemWorkspaceProps): ReactElement {
  const initialDraftId = getDraftIdFromLocation(props.gameId);
  return (
    <ItemWorkspaceScope
      key={`${props.gameId}:${props.canEdit ? 'author' : 'read-only'}`}
      {...props}
      initialDraftId={initialDraftId}
    />
  );
}
