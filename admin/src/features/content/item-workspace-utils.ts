import { AdminApiError } from '../../shared/admin-api';
import type { Draft, ValidationResult } from '../../shared/admin-api';
import { validateItemDefinition } from '../../shared/content/item-schema';
import type { ItemDefinition } from '../../shared/content/item-schema';
import type { ItemFormField, ItemFormIssue, ItemFormValues } from '../../shared/content/item-form';

export interface DraftConflict {
  current: Draft;
  local: ItemFormValues;
}

export interface PendingCreateRecovery {
  contentId: string;
  local: ItemFormValues;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function parseDraft(value: unknown): Draft {
  if (
    !isRecord(value) ||
    typeof value.draft_id !== 'string' ||
    typeof value.game_id !== 'string' ||
    typeof value.content_id !== 'string' ||
    typeof value.draft_version !== 'string' ||
    !/^(0|[1-9][0-9]{0,18})$/.test(value.draft_version) ||
    typeof value.definition !== 'object' ||
    value.definition === null ||
    Array.isArray(value.definition)
  ) {
    throw new Error('管理服务返回的草稿格式无效');
  }
  const validation = validateItemDefinition(value.definition);
  if (!validation.valid || validation.data === undefined) {
    throw new Error('草稿与当前物品 Schema 不兼容，暂不能安全编辑');
  }
  if (
    validation.data.game_id !== value.game_id ||
    validation.data.content_id !== value.content_id
  ) {
    throw new Error('草稿的 Game 或内容 ID 与 Definition 不匹配');
  }
  return { ...value, definition: validation.data } as Draft;
}

export function serverFieldErrors(result: ValidationResult): Record<string, string> {
  return result.errors.reduce<Record<string, string>>(
    (errors, issue) => ({ ...errors, [issue.pointer]: issue.message }),
    {},
  );
}

export function itemFormErrors(issues: readonly ItemFormIssue[]): Record<string, string> {
  return issues.reduce<Record<string, string>>(
    (errors, issue) => ({ ...errors, [issue.path]: issue.message }),
    {},
  );
}

export function getDraftIdFromLocation(gameId: string): string | null {
  const queryID = new URLSearchParams(window.location.search).get('draftId');
  if (queryID !== null) {
    return queryID;
  }
  try {
    return window.sessionStorage.getItem(`tbcg:${gameId}:item-draft`);
  } catch {
    return null;
  }
}

const pendingCreateRecoveryKey = (gameId: string, ownerId: string): string =>
  `tbcg:${encodeURIComponent(ownerId)}:${encodeURIComponent(gameId)}:pending-item-draft-create`;

function isItemFormValues(value: unknown): value is ItemFormValues {
  return (
    isRecord(value) &&
    typeof value.contentId === 'string' &&
    (value.itemType === 'consumable' ||
      value.itemType === 'equipment' ||
      value.itemType === 'material' ||
      value.itemType === 'quest') &&
    typeof value.displayName === 'string' &&
    typeof value.description === 'string' &&
    typeof value.iconId === 'string' &&
    typeof value.stackLimit === 'string' &&
    typeof value.priceMinor === 'string' &&
    typeof value.rewardValueMinor === 'string' &&
    typeof value.dropWeight === 'string'
  );
}

export function readPendingCreateRecovery(
  gameId: string,
  ownerId: string,
): PendingCreateRecovery | null {
  try {
    const stored = window.sessionStorage.getItem(pendingCreateRecoveryKey(gameId, ownerId));
    if (stored === null) {
      return null;
    }
    const value: unknown = JSON.parse(stored);
    if (
      !isRecord(value) ||
      typeof value.contentId !== 'string' ||
      !isItemFormValues(value.local) ||
      value.contentId !== value.local.contentId.trim()
    ) {
      window.sessionStorage.removeItem(pendingCreateRecoveryKey(gameId, ownerId));
      return null;
    }
    return { contentId: value.contentId, local: value.local };
  } catch {
    return null;
  }
}

export function savePendingCreateRecovery(
  gameId: string,
  ownerId: string,
  recovery: PendingCreateRecovery,
): void {
  try {
    window.sessionStorage.setItem(
      pendingCreateRecoveryKey(gameId, ownerId),
      JSON.stringify(recovery),
    );
  } catch {
    // 存储不可用时仍保留当前页面内的待核验输入。
  }
}

export function clearPendingCreateRecovery(gameId: string, ownerId: string): void {
  try {
    window.sessionStorage.removeItem(pendingCreateRecoveryKey(gameId, ownerId));
  } catch {
    // 存储不可用时不会影响当前页面的草稿恢复操作。
  }
}

export function rememberDraftId(gameId: string, draftId: string, updateUrl: boolean): void {
  if (updateUrl) {
    const url = new URL(window.location.href);
    url.searchParams.set('draftId', draftId);
    window.history.replaceState(null, '', url);
  }
  try {
    window.sessionStorage.setItem(`tbcg:${gameId}:item-draft`, draftId);
  } catch {
    // sessionStorage 不可用时，仅当前可见 scope 可依靠 URL 保留草稿引用。
  }
}

export function readOnlyRoleMessage(canReview: boolean): string {
  return canReview
    ? 'Reviewer 可查看草稿并审批确切 revision；编辑需另外绑定 Author。'
    : '当前角色可以查看内容，但没有 Author 编辑权限。';
}

export function validationIssuesFromDefinition(definition: ItemDefinition): Record<string, string> {
  const result = validateItemDefinition(definition);
  return result.valid
    ? {}
    : result.errors.reduce<Record<string, string>>(
        (errors, issue) => ({
          ...errors,
          [issue.path]: validationMessage(issue.code),
        }),
        {},
      );
}

export function itemFormsEqual(left: ItemFormValues, right: ItemFormValues): boolean {
  return (Object.keys(left) as (keyof ItemFormValues)[]).every(
    (field) => left[field] === right[field],
  );
}

export function apiFieldErrors(error: AdminApiError): Record<string, string> {
  return error.fields.reduce<Record<string, string>>(
    (result, issue) => ({ ...result, [issue.pointer]: issue.message }),
    {},
  );
}

export function fieldToPointer(field: ItemFormField): string | null {
  const pointers: Partial<Record<ItemFormField, string>> = {
    contentId: '/content_id',
    displayName: '/public/display_name',
    description: '/public/description',
    iconId: '/public/icon_id',
    stackLimit: '/public/stack_limit',
    priceMinor: '/public/price_minor',
    rewardValueMinor: '/server/reward_value_minor',
    dropWeight: '/server/drop_weight_basis_points',
  };
  return pointers[field] ?? null;
}

export function getRequestMessage(reason: unknown): string {
  if (reason instanceof Error) {
    return reason.message;
  }
  return '管理服务暂不可用，请稍后重试';
}

function validationMessage(code: string): string {
  if (code === 'missing_field') {
    return '必填字段缺失。';
  }
  if (code === 'unknown_field') {
    return '字段不受支持。';
  }
  return '字段值不符合共享内容 Schema。';
}
