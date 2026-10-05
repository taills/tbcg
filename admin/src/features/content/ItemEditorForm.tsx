import type { ReactElement } from 'react';
import type { ItemFormField, ItemFormValues } from '../../shared/content/item-form';

interface ItemEditorFormProps {
  values: ItemFormValues;
  fieldErrors: Readonly<Record<string, string>>;
  readOnly: boolean;
  lockContentId: boolean;
  onChange: (field: ItemFormField, value: string) => void;
}

function FieldError({
  id,
  message,
}: {
  id: string;
  message: string | undefined;
}): ReactElement | null {
  if (message === undefined) {
    return null;
  }
  return (
    <p className="mt-1 text-xs text-red-700" id={id}>
      {message}
    </p>
  );
}

export default function ItemEditorForm({
  values,
  fieldErrors,
  readOnly,
  lockContentId,
  onChange,
}: ItemEditorFormProps): ReactElement {
  const errorFor = (path: string): string | undefined => fieldErrors[path];

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1.3fr)_minmax(280px,0.7fr)]">
      <fieldset className="min-w-0 space-y-5" disabled={readOnly}>
        <legend className="sr-only">物品 Definition 字段</legend>
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="sm:col-span-2">
            <label
              className="mb-1.5 block text-sm font-medium text-slate-800"
              htmlFor="item-content-id"
            >
              内容 ID
            </label>
            <input
              autoComplete="off"
              className="w-full rounded-md border border-slate-300 bg-slate-50 px-3 py-2.5 font-mono text-sm text-slate-700 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:opacity-70"
              disabled={lockContentId}
              id="item-content-id"
              maxLength={128}
              onChange={(event) => onChange('contentId', event.currentTarget.value)}
              value={values.contentId}
            />
            <FieldError id="content-id-error" message={errorFor('/content_id')} />
          </div>
          <div>
            <label
              className="mb-1.5 block text-sm font-medium text-slate-800"
              htmlFor="item-display-name"
            >
              物品名称
            </label>
            <input
              aria-describedby={
                errorFor('/public/display_name') === undefined ? undefined : 'display-name-error'
              }
              aria-invalid={errorFor('/public/display_name') !== undefined}
              className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-display-name"
              maxLength={80}
              onChange={(event) => onChange('displayName', event.currentTarget.value)}
              required
              value={values.displayName}
            />
            <FieldError id="display-name-error" message={errorFor('/public/display_name')} />
          </div>
          <div>
            <label className="mb-1.5 block text-sm font-medium text-slate-800" htmlFor="item-type">
              物品类型
            </label>
            <select
              className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-type"
              onChange={(event) => onChange('itemType', event.currentTarget.value)}
              value={values.itemType}
            >
              <option value="consumable">消耗品</option>
              <option value="equipment">装备</option>
              <option value="material">材料</option>
              <option value="quest">任务物品</option>
            </select>
          </div>
          <div className="sm:col-span-2">
            <label
              className="mb-1.5 block text-sm font-medium text-slate-800"
              htmlFor="item-description"
            >
              描述
            </label>
            <textarea
              aria-describedby={
                errorFor('/public/description') === undefined ? undefined : 'item-description-error'
              }
              aria-invalid={errorFor('/public/description') !== undefined}
              className="min-h-24 w-full resize-y rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-description"
              maxLength={512}
              onChange={(event) => onChange('description', event.currentTarget.value)}
              value={values.description}
            />
            <FieldError id="item-description-error" message={errorFor('/public/description')} />
          </div>
          <div>
            <label className="mb-1.5 block text-sm font-medium text-slate-800" htmlFor="item-icon">
              图标
            </label>
            <select
              aria-describedby={
                errorFor('/public/icon_id') === undefined ? undefined : 'item-icon-error'
              }
              aria-invalid={errorFor('/public/icon_id') !== undefined}
              className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-icon"
              onChange={(event) => onChange('iconId', event.currentTarget.value)}
              value={values.iconId}
            >
              <option value="demo:icon/placeholder">原创占位图标</option>
            </select>
            <FieldError id="item-icon-error" message={errorFor('/public/icon_id')} />
          </div>
          <div>
            <label
              className="mb-1.5 block text-sm font-medium text-slate-800"
              htmlFor="item-stack-limit"
            >
              最大堆叠数
            </label>
            <input
              aria-describedby={
                errorFor('/public/stack_limit') === undefined ? undefined : 'stack-limit-error'
              }
              aria-invalid={errorFor('/public/stack_limit') !== undefined}
              className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-stack-limit"
              inputMode="numeric"
              onChange={(event) => onChange('stackLimit', event.currentTarget.value)}
              value={values.stackLimit}
            />
            <FieldError id="stack-limit-error" message={errorFor('/public/stack_limit')} />
          </div>
          <div>
            <label className="mb-1.5 block text-sm font-medium text-slate-800" htmlFor="item-price">
              价格（最小货币单位）
            </label>
            <input
              aria-describedby={
                errorFor('/public/price_minor') === undefined
                  ? 'item-price-hint'
                  : 'item-price-error'
              }
              aria-invalid={errorFor('/public/price_minor') !== undefined}
              className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 font-mono text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-price"
              inputMode="numeric"
              onChange={(event) => onChange('priceMinor', event.currentTarget.value)}
              value={values.priceMinor}
            />
            <p className="mt-1 text-xs text-slate-500" id="item-price-hint">
              以十进制字符串保存，不进行浮点换算。
            </p>
            <FieldError id="item-price-error" message={errorFor('/public/price_minor')} />
          </div>
          <div>
            <label
              className="mb-1.5 block text-sm font-medium text-slate-800"
              htmlFor="item-reward-value"
            >
              奖励值（最小货币单位）
            </label>
            <input
              aria-describedby={
                errorFor('/server/reward_value_minor') === undefined
                  ? 'item-reward-hint'
                  : 'item-reward-error'
              }
              aria-invalid={errorFor('/server/reward_value_minor') !== undefined}
              className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 font-mono text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-reward-value"
              inputMode="numeric"
              onChange={(event) => onChange('rewardValueMinor', event.currentTarget.value)}
              value={values.rewardValueMinor}
            />
            <p className="mt-1 text-xs text-slate-500" id="item-reward-hint">
              仅服务端规则字段，不进入公开预览。
            </p>
            <FieldError id="item-reward-error" message={errorFor('/server/reward_value_minor')} />
          </div>
          <div>
            <label
              className="mb-1.5 block text-sm font-medium text-slate-800"
              htmlFor="item-drop-weight"
            >
              掉落权重（基点）
            </label>
            <input
              aria-describedby={
                errorFor('/server/drop_weight_basis_points') === undefined
                  ? 'drop-weight-hint'
                  : 'drop-weight-error'
              }
              aria-invalid={errorFor('/server/drop_weight_basis_points') !== undefined}
              className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20 disabled:bg-slate-50"
              id="item-drop-weight"
              inputMode="numeric"
              onChange={(event) => onChange('dropWeight', event.currentTarget.value)}
              value={values.dropWeight}
            />
            <p className="mt-1 text-xs text-slate-500" id="drop-weight-hint">
              0–10000，属于服务端私有规则。
            </p>
            <FieldError
              id="drop-weight-error"
              message={errorFor('/server/drop_weight_basis_points')}
            />
          </div>
        </div>
      </fieldset>

      <aside
        aria-labelledby="item-preview-heading"
        className="min-w-0 rounded-lg border border-slate-200 bg-slate-50 p-4 sm:p-5"
      >
        <h2 className="text-sm font-semibold text-slate-900" id="item-preview-heading">
          公开预览
        </h2>
        <div className="mt-4 flex items-start gap-3 rounded-lg border border-slate-200 bg-white p-3">
          <img
            alt=""
            className="h-14 w-14 shrink-0 rounded-md border border-slate-200 bg-slate-100 object-contain p-1"
            src="/assets/demo/icons/original-placeholder.svg"
          />
          <div className="min-w-0">
            <p className="break-words font-semibold text-slate-950">
              {values.displayName || '未命名物品'}
            </p>
            <p className="mt-1 break-words text-sm leading-5 text-slate-600">
              {values.description || '尚未填写描述'}
            </p>
          </div>
        </div>
        <dl className="mt-4 divide-y divide-slate-200 rounded-md border border-slate-200 bg-white px-3">
          <div className="flex items-start justify-between gap-3 py-2.5 text-sm">
            <dt className="text-slate-500">价格</dt>
            <dd className="break-all text-right font-mono text-slate-900">
              {values.priceMinor || '—'}
            </dd>
          </div>
          <div className="flex items-start justify-between gap-3 py-2.5 text-sm">
            <dt className="text-slate-500">堆叠上限</dt>
            <dd className="text-right text-slate-900">{values.stackLimit || '—'}</dd>
          </div>
          <div className="flex items-start justify-between gap-3 py-2.5 text-sm">
            <dt className="text-slate-500">类型</dt>
            <dd className="text-right text-slate-900">{values.itemType}</dd>
          </div>
          <div className="flex items-start justify-between gap-3 py-2.5 text-sm">
            <dt className="text-slate-500">图标 ID</dt>
            <dd className="break-all text-right font-mono text-xs text-slate-700">
              {values.iconId}
            </dd>
          </div>
        </dl>
        <p className="mt-3 text-xs leading-5 text-slate-500">
          奖励值与掉落权重不属于公开预览数据。
        </p>
      </aside>
    </div>
  );
}
