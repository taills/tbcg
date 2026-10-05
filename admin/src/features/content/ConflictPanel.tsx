import type { ReactElement } from 'react';
import { definitionToForm } from '../../shared/content/item-form';
import type { ItemFormValues } from '../../shared/content/item-form';
import type { DraftConflict } from './item-workspace-utils';

interface ConflictPanelProps {
  conflict: DraftConflict;
  pending: boolean;
  onUseServer: () => void;
  onReapply: () => void;
}

export default function ConflictPanel({
  conflict,
  pending,
  onUseServer,
  onReapply,
}: ConflictPanelProps): ReactElement {
  const latest = definitionToForm(conflict.current.definition);
  const comparedFields: readonly { label: string; key: keyof ItemFormValues }[] = [
    { label: '物品名称', key: 'displayName' },
    { label: '价格', key: 'priceMinor' },
    { label: '奖励值', key: 'rewardValueMinor' },
    { label: '最大堆叠数', key: 'stackLimit' },
  ];

  return (
    <section
      aria-labelledby="conflict-heading"
      className="rounded-lg border border-amber-300 bg-amber-50 p-4"
    >
      <h3 className="font-semibold text-amber-950" id="conflict-heading">
        草稿版本冲突
      </h3>
      <p className="mt-1 text-sm leading-5 text-amber-900">
        服务端当前版本为 <span className="font-mono">{conflict.current.draft_version}</span>
        。本地编辑尚未覆盖；请比较后明确选择。
      </p>
      <div className="mt-3 overflow-x-auto rounded-md border border-amber-200 bg-white">
        <table className="w-full min-w-[520px] text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase text-slate-600">
            <tr>
              <th className="px-3 py-2">字段</th>
              <th className="px-3 py-2">本地输入</th>
              <th className="px-3 py-2">服务端最新</th>
            </tr>
          </thead>
          <tbody>
            {comparedFields.map(({ label, key }) => (
              <tr className="border-t border-slate-100" key={key}>
                <th className="px-3 py-2 font-medium text-slate-700">{label}</th>
                <td className="max-w-48 break-all px-3 py-2 font-mono text-slate-900">
                  {conflict.local[key]}
                </td>
                <td className="max-w-48 break-all px-3 py-2 font-mono text-slate-700">
                  {latest[key]}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <button
          className="rounded-md border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 focus:outline-none focus:ring-2 focus:ring-teal-700"
          onClick={onUseServer}
          type="button"
        >
          使用服务器版本
        </button>
        <button
          className="rounded-md bg-amber-800 px-3 py-2 text-sm font-semibold text-white hover:bg-amber-900 focus:outline-none focus:ring-2 focus:ring-amber-800 focus:ring-offset-2 disabled:opacity-60"
          disabled={pending}
          onClick={onReapply}
          type="button"
        >
          {pending ? '正在保存…' : '将本地更改重应用到最新版本'}
        </button>
      </div>
    </section>
  );
}
