import { useCallback, useEffect, useState } from 'react';
import type { ReactElement } from 'react';
import { AdminApiError } from '../../shared/admin-api';
import { contentApi } from '../../shared/content-api';
import type {
  ContentSummary,
  DiffChange,
  GenericDraft,
  References,
} from '../../shared/content-api';
import { DialogTreeEditor, DungeonStageEditor, QuestStageEditor } from './VisualEditors';

interface ContentLibraryProps {
  gameId: string;
  csrfToken: string;
  canEdit: boolean;
}

type Json = Record<string, unknown>;

const button =
  'rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50';

function messageOf(error: unknown): string {
  if (error instanceof AdminApiError) {
    const fields = error.fields
      .map((field) => `${field.pointer || '/'}：${field.message}`)
      .join('；');
    return fields === '' ? error.message : `${error.message}（${fields}）`;
  }
  return error instanceof Error ? error.message : '操作失败';
}

function typeOf(contentId: string): string {
  const match = /^[^:]+:([^/]+)\//.exec(contentId);
  return match?.[1] ?? '';
}

function skeleton(gameId: string, contentId: string): Json {
  const identity = { game_id: gameId, content_id: contentId, revision: 1, schema_version: 1 };
  return { ...identity, public: { ...identity }, server: {} };
}

/** 稳定 ID 搜索、交叉引用、revision 差异与全类型 JSON/可视化编辑（任务 0013）。 */
export default function ContentLibrary({
  gameId,
  csrfToken,
  canEdit,
}: ContentLibraryProps): ReactElement {
  const [types, setTypes] = useState<string[]>([]);
  const [query, setQuery] = useState('');
  const [typeFilter, setTypeFilter] = useState('');
  const [results, setResults] = useState<ContentSummary[]>([]);
  const [selected, setSelected] = useState<string>('');
  const [draft, setDraft] = useState<GenericDraft | null>(null);
  const [text, setText] = useState('');
  const [references, setReferences] = useState<References | null>(null);
  const [diff, setDiff] = useState<DiffChange[] | null>(null);
  const [diffFrom, setDiffFrom] = useState('1');
  const [diffTo, setDiffTo] = useState('draft');
  const [message, setMessage] = useState<string | null>(null);
  const [newId, setNewId] = useState('');
  const [busy, setBusy] = useState(false);

  const search = useCallback(async (): Promise<void> => {
    try {
      setResults(await contentApi.search(gameId, query, typeFilter));
    } catch (error) {
      setMessage(messageOf(error));
    }
  }, [gameId, query, typeFilter]);

  useEffect(() => {
    void contentApi
      .contentTypes()
      .then(setTypes)
      .catch(() => setTypes([]));
  }, []);

  useEffect(() => {
    void search();
  }, [search]);

  let parsed: Json | null = null;
  try {
    const value: unknown = JSON.parse(text);
    parsed =
      typeof value === 'object' && value !== null && !Array.isArray(value) ? (value as Json) : null;
  } catch {
    parsed = null;
  }

  const open = async (summary: ContentSummary): Promise<void> => {
    setSelected(summary.content_id);
    setMessage(null);
    setDiff(null);
    try {
      if (summary.draft_id !== undefined && summary.draft_id !== '') {
        const loaded = await contentApi.getDraft(gameId, summary.draft_id);
        setDraft(loaded);
        setText(JSON.stringify(loaded.definition, null, 2));
      } else {
        setDraft(null);
        const base = await contentApi.revision(gameId, summary.content_id, summary.latest_revision);
        setText(JSON.stringify(base, null, 2));
      }
      setReferences(await contentApi.references(gameId, summary.content_id));
      setDiffFrom(String(Math.max(1, summary.approved_revision)));
    } catch (error) {
      setMessage(messageOf(error));
    }
  };

  const run = async (action: () => Promise<void>): Promise<void> => {
    setBusy(true);
    setMessage(null);
    try {
      await action();
    } catch (error) {
      setMessage(messageOf(error));
    } finally {
      setBusy(false);
    }
  };

  const save = (): Promise<void> =>
    run(async () => {
      if (parsed === null) throw new Error('JSON 无效，无法保存');
      const saved =
        draft === null
          ? await contentApi.createDraft(gameId, selected, parsed, csrfToken)
          : await contentApi.updateDraft(gameId, draft, parsed, csrfToken);
      setDraft(saved);
      setText(JSON.stringify(saved.definition, null, 2));
      setMessage('草稿已保存');
      await search();
    });

  const validate = (): Promise<void> =>
    run(async () => {
      if (draft === null) throw new Error('请先保存草稿');
      const result = await contentApi.validateDraft(gameId, draft.draft_id, csrfToken);
      setMessage(
        result.valid
          ? '校验通过'
          : `校验失败：${result.errors.map((e) => `${e.pointer} ${e.message}`).join('；')}`,
      );
    });

  const submit = (): Promise<void> =>
    run(async () => {
      if (draft === null) throw new Error('请先保存草稿');
      await contentApi.submitDraft(gameId, draft, csrfToken);
      setMessage('已提交审核，生成不可变 revision');
      await search();
    });

  const compare = (): Promise<void> =>
    run(async () => {
      setDiff(await contentApi.diff(gameId, selected, diffFrom, diffTo));
    });

  const contentType = typeOf(selected);
  const visual =
    parsed === null
      ? null
      : (() => {
          const props = {
            value: parsed,
            onChange: (next: Json) => setText(JSON.stringify(next, null, 2)),
            disabled: !canEdit,
          };
          switch (contentType) {
            case 'npc':
              return <DialogTreeEditor {...props} />;
            case 'quest':
              return <QuestStageEditor {...props} />;
            case 'dungeon':
              return <DungeonStageEditor {...props} />;
            default:
              return null;
          }
        })();

  return (
    <section
      className="rounded-xl border border-slate-200 bg-white p-5"
      aria-labelledby="library-title"
    >
      <h2 className="text-base font-semibold text-slate-900" id="library-title">
        内容库（全部类型）
      </h2>
      <div className="mt-3 flex flex-wrap gap-2">
        <input
          aria-label="搜索稳定 ID 或名称"
          className="rounded border border-slate-300 px-2 py-1 text-sm"
          onChange={(e) => setQuery(e.currentTarget.value)}
          placeholder="搜索稳定 ID / 名称"
          value={query}
        />
        <select
          aria-label="内容类型"
          className="rounded border border-slate-300 px-2 py-1 text-sm"
          onChange={(e) => setTypeFilter(e.currentTarget.value)}
          value={typeFilter}
        >
          <option value="">全部类型</option>
          {types.map((type) => (
            <option key={type} value={type}>
              {type}
            </option>
          ))}
        </select>
        {canEdit && (
          <>
            <input
              aria-label="新内容 ID"
              className="rounded border border-slate-300 px-2 py-1 text-sm"
              onChange={(e) => setNewId(e.currentTarget.value)}
              placeholder={`${gameId}:npc/new-id`}
              value={newId}
            />
            <button
              className={button}
              disabled={newId === ''}
              onClick={() => {
                setSelected(newId);
                setDraft(null);
                setReferences(null);
                setText(JSON.stringify(skeleton(gameId, newId), null, 2));
              }}
              type="button"
            >
              新建
            </button>
          </>
        )}
      </div>
      <div className="mt-4 grid gap-4 lg:grid-cols-[18rem_1fr]">
        <ul className="max-h-[32rem] space-y-1 overflow-y-auto" aria-label="内容搜索结果">
          {results.map((summary) => (
            <li key={summary.content_id}>
              <button
                className={`w-full rounded px-2 py-1 text-left text-sm ${summary.content_id === selected ? 'bg-teal-50 text-teal-900' : 'hover:bg-slate-50'}`}
                onClick={() => void open(summary)}
                type="button"
              >
                <span className="block font-medium">
                  {summary.display_name || summary.content_id}
                </span>
                <span className="block text-xs text-slate-500">
                  {summary.content_id} · r{summary.latest_revision} · 已批准 r
                  {summary.approved_revision}
                  {summary.draft_id ? ' · 有草稿' : ''}
                </span>
              </button>
            </li>
          ))}
        </ul>
        <div className="space-y-3">
          {message !== null && (
            <p
              className="rounded border border-slate-200 bg-slate-50 px-3 py-2 text-sm"
              role="status"
            >
              {message}
            </p>
          )}
          {selected !== '' && (
            <>
              <p className="text-sm font-semibold">{selected}</p>
              {visual}
              <textarea
                aria-label="内容定义 JSON"
                className="h-72 w-full rounded border border-slate-300 p-2 font-mono text-xs"
                disabled={!canEdit}
                onChange={(e) => setText(e.currentTarget.value)}
                value={text}
              />
              {parsed === null && <p className="text-sm text-red-700">JSON 语法错误</p>}
              {canEdit && (
                <div className="flex gap-2">
                  <button
                    className={button}
                    disabled={busy || parsed === null}
                    onClick={() => void save()}
                    type="button"
                  >
                    保存草稿
                  </button>
                  <button
                    className={button}
                    disabled={busy || draft === null}
                    onClick={() => void validate()}
                    type="button"
                  >
                    校验
                  </button>
                  <button
                    className={button}
                    disabled={busy || draft === null}
                    onClick={() => void submit()}
                    type="button"
                  >
                    提交审核
                  </button>
                </div>
              )}
              {references !== null && (
                <div className="grid gap-2 text-sm sm:grid-cols-3" aria-label="交叉引用">
                  <div>
                    <p className="font-semibold">被引用（{references.incoming.length}）</p>
                    {references.incoming.map((hit) => (
                      <p className="text-xs" key={`${hit.content_id}${hit.pointer}`}>
                        {hit.content_id} {hit.pointer}
                      </p>
                    ))}
                  </div>
                  <div>
                    <p className="font-semibold">引用（{references.outgoing.length}）</p>
                    {references.outgoing.map((hit) => (
                      <p className="text-xs" key={hit.pointer}>
                        {hit.content_id} {hit.pointer}
                      </p>
                    ))}
                  </div>
                  <div>
                    <p className="font-semibold text-red-700">
                      断开引用（{references.broken.length}）
                    </p>
                    {references.broken.map((hit) => (
                      <p className="text-xs text-red-700" key={hit.pointer}>
                        {hit.content_id} {hit.pointer}
                      </p>
                    ))}
                  </div>
                </div>
              )}
              <div className="flex flex-wrap items-center gap-2 text-sm">
                差异：
                <input
                  aria-label="差异起点"
                  className="w-20 rounded border px-1"
                  onChange={(e) => setDiffFrom(e.currentTarget.value)}
                  value={diffFrom}
                />
                →{' '}
                <input
                  aria-label="差异终点"
                  className="w-20 rounded border px-1"
                  onChange={(e) => setDiffTo(e.currentTarget.value)}
                  value={diffTo}
                />
                <button
                  className={button}
                  disabled={busy}
                  onClick={() => void compare()}
                  type="button"
                >
                  比较
                </button>
              </div>
              {diff !== null && (
                <ul className="space-y-1 text-xs" aria-label="revision 差异">
                  {diff.length === 0 && <li>无差异</li>}
                  {diff.map((change) => (
                    <li key={change.pointer}>
                      <span className="font-mono">{change.pointer}</span> {change.kind}：
                      {JSON.stringify(change.before)} → {JSON.stringify(change.after)}
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
        </div>
      </div>
    </section>
  );
}
