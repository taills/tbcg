import type { ReactElement } from 'react';
import type { Draft } from '../../shared/admin-api';
import type { PendingCreateRecovery } from './item-workspace-utils';

export interface CreateRecoveryState extends PendingCreateRecovery {
  candidates: readonly Draft[];
  status: 'pending' | 'loading' | 'empty' | 'found' | 'error';
}

interface CreateRecoveryPanelProps {
  recovery: CreateRecoveryState;
  onFind: () => void;
  onCompare: (candidate: Draft) => void;
}

export default function CreateRecoveryPanel({
  recovery,
  onFind,
  onCompare,
}: CreateRecoveryPanelProps): ReactElement {
  return (
    <section
      aria-labelledby="create-recovery-heading"
      className="mt-4 rounded-lg border border-amber-300 bg-amber-50 p-4"
    >
      <h3 className="font-semibold text-amber-950" id="create-recovery-heading">
        草稿创建结果待核验
      </h3>
      <p className="mt-1 text-sm leading-5 text-amber-900">
        内容 ID：<span className="font-mono">{recovery.contentId}</span>
        。本地输入已保留；列表为空也不能证明先前请求未提交或已回滚。
      </p>
      {recovery.status === 'empty' && (
        <p className="mt-2 text-sm text-amber-900" role="status">
          当前没有找到可访问草稿，创建结果仍待核验。请稍后重新查询，不要据此认定创建失败。
        </p>
      )}
      {recovery.status === 'error' && (
        <p className="mt-2 text-sm text-red-800" role="alert">
          无法读取草稿列表；创建结果仍待核验，请重试查询。
        </p>
      )}
      {recovery.candidates.length > 0 && (
        <ul className="mt-3 space-y-2">
          {recovery.candidates.map((candidate) => (
            <li
              className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-200 bg-white p-3"
              key={candidate.draft_id}
            >
              <span className="text-sm text-slate-800">
                版本 <span className="font-mono">{candidate.draft_version}</span> ·{' '}
                {candidate.content_id}
              </span>
              <button
                className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700"
                onClick={() => onCompare(candidate)}
                type="button"
              >
                比较已有草稿版本 {candidate.draft_version}
              </button>
            </li>
          ))}
        </ul>
      )}
      <button
        className="mt-3 rounded-md bg-amber-800 px-3 py-2 text-sm font-semibold text-white hover:bg-amber-900 focus:outline-none focus:ring-2 focus:ring-amber-800 focus:ring-offset-2 disabled:opacity-60"
        disabled={recovery.status === 'loading'}
        onClick={onFind}
        type="button"
      >
        {recovery.status === 'loading' ? '正在查找草稿…' : '查找已有草稿'}
      </button>
    </section>
  );
}
