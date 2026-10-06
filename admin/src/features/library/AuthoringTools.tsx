import { useState } from 'react';
import type { ReactElement } from 'react';
import { AdminApiError } from '../../shared/admin-api';
import { contentApi } from '../../shared/content-api';
import type { ImportResult, ValidationReport } from '../../shared/content-api';

interface AuthoringToolsProps {
  gameId: string;
  csrfToken: string;
  canEdit: boolean;
}

const button =
  'rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50';

function messageOf(error: unknown): string {
  return error instanceof AdminApiError || error instanceof Error ? error.message : '操作失败';
}

/** 批量导入、全量校验报告与使用服务端同一 Go 规则的战斗预览。 */
export default function AuthoringTools({
  gameId,
  csrfToken,
  canEdit,
}: AuthoringToolsProps): ReactElement {
  const [importText, setImportText] = useState('[]');
  const [importResults, setImportResults] = useState<ImportResult[] | null>(null);
  const [report, setReport] = useState<ValidationReport | null>(null);
  const [encounter, setEncounter] = useState(`${gameId}:encounter/`);
  const [archetype, setArchetype] = useState(`${gameId}:archetype/`);
  const [level, setLevel] = useState(1);
  const [seed, setSeed] = useState(1);
  const [preview, setPreview] = useState<Record<string, unknown> | null>(null);
  const [error, setError] = useState<string | null>(null);

  const runImport = async (dryRun: boolean): Promise<void> => {
    setError(null);
    try {
      const parsed: unknown = JSON.parse(importText);
      if (!Array.isArray(parsed)) throw new Error('导入内容必须是定义数组');
      setImportResults(await contentApi.importDefinitions(gameId, parsed, dryRun, csrfToken));
    } catch (reason) {
      setError(messageOf(reason));
    }
  };

  const runReport = async (source: 'draft' | 'approved'): Promise<void> => {
    setError(null);
    try {
      setReport(await contentApi.validationReport(gameId, source, csrfToken));
    } catch (reason) {
      setError(messageOf(reason));
    }
  };

  const runPreview = async (): Promise<void> => {
    setError(null);
    try {
      setPreview(
        await contentApi.previewBattle(
          gameId,
          'draft',
          { encounter_id: encounter, seed, party: [{ archetype_id: archetype, level }] },
          csrfToken,
        ),
      );
    } catch (reason) {
      setError(messageOf(reason));
    }
  };

  const events = Array.isArray(preview?.events)
    ? (preview.events as Record<string, unknown>[])
    : [];

  return (
    <section
      className="rounded-xl border border-slate-200 bg-white p-5"
      aria-labelledby="tools-title"
    >
      <h2 className="text-base font-semibold" id="tools-title">
        创作工具
      </h2>
      {error !== null && (
        <p className="mt-2 text-sm text-red-700" role="alert">
          {error}
        </p>
      )}
      <div className="mt-3 grid gap-4 lg:grid-cols-3">
        <div className="space-y-2">
          <h3 className="text-sm font-semibold">批量导入（只创建新草稿，不覆盖）</h3>
          <textarea
            aria-label="批量导入 JSON"
            className="h-40 w-full rounded border p-2 font-mono text-xs"
            disabled={!canEdit}
            onChange={(e) => setImportText(e.currentTarget.value)}
            value={importText}
          />
          <div className="flex gap-2">
            <button
              className={button}
              disabled={!canEdit}
              onClick={() => void runImport(true)}
              type="button"
            >
              试运行
            </button>
            <button
              className={button}
              disabled={!canEdit}
              onClick={() => void runImport(false)}
              type="button"
            >
              导入
            </button>
          </div>
          {importResults?.map((result) => (
            <p className="text-xs" key={result.content_id}>
              {result.content_id}：{result.status}
              {result.errors.length > 0
                ? `（${result.errors.map((e) => e.pointer).join(', ')}）`
                : ''}
            </p>
          ))}
        </div>
        <div className="space-y-2">
          <h3 className="text-sm font-semibold">全量校验报告</h3>
          <div className="flex gap-2">
            <button className={button} onClick={() => void runReport('draft')} type="button">
              草稿视图
            </button>
            <button className={button} onClick={() => void runReport('approved')} type="button">
              已批准视图
            </button>
          </div>
          {report !== null && (
            <div className="text-xs" aria-label="校验报告">
              <p>
                {report.source}：{report.entry_count} 条，
                {report.valid ? '全部通过' : `${report.issues.length} 个问题`}
              </p>
              {report.issues.map((issue, index) => (
                <p className="text-red-700" key={index}>
                  {issue.content_id}
                  {issue.path} [{issue.code}] {issue.message}
                </p>
              ))}
            </div>
          )}
        </div>
        <div className="space-y-2">
          <h3 className="text-sm font-semibold">战斗预览（服务端同版本 Go 规则）</h3>
          <input
            aria-label="预览遭遇"
            className="w-full rounded border px-2 py-1 text-sm"
            onChange={(e) => setEncounter(e.currentTarget.value)}
            value={encounter}
          />
          <input
            aria-label="预览原型"
            className="w-full rounded border px-2 py-1 text-sm"
            onChange={(e) => setArchetype(e.currentTarget.value)}
            value={archetype}
          />
          <div className="flex gap-2">
            <input
              aria-label="预览等级"
              className="w-20 rounded border px-2 py-1 text-sm"
              min={1}
              onChange={(e) => setLevel(Number(e.currentTarget.value))}
              type="number"
              value={level}
            />
            <input
              aria-label="预览种子"
              className="w-28 rounded border px-2 py-1 text-sm"
              min={0}
              onChange={(e) => setSeed(Number(e.currentTarget.value))}
              type="number"
              value={seed}
            />
            <button className={button} onClick={() => void runPreview()} type="button">
              运行
            </button>
          </div>
          {preview !== null && (
            <div className="max-h-48 overflow-y-auto text-xs" aria-label="预览结果">
              <p>
                规则：{String(preview.rules_version)} · 结果：{String(preview.outcome)} · 回合{' '}
                {String(preview.rounds)}
              </p>
              {events.map((event) => (
                <p key={String(event.event_seq)}>
                  #{String(event.event_seq)} {String(event.kind)} {String(event.actor_id ?? '')} →{' '}
                  {String(event.target_id ?? '')}{' '}
                  {event.damage !== undefined ? `-${String(event.damage)}` : ''} HP{' '}
                  {String(event.hp_before)}→{String(event.hp_after)}
                  {event.crit === true ? ' 暴击' : ''}
                </p>
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
