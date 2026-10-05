import type { ItemDefinition } from './item-schema';

export type ItemType = ItemDefinition['public']['item_type'];
export type ItemFormField =
  | 'contentId'
  | 'itemType'
  | 'displayName'
  | 'description'
  | 'iconId'
  | 'stackLimit'
  | 'priceMinor'
  | 'rewardValueMinor'
  | 'dropWeight';

export interface ItemFormValues {
  contentId: string;
  itemType: ItemType;
  displayName: string;
  description: string;
  iconId: string;
  stackLimit: string;
  priceMinor: string;
  rewardValueMinor: string;
  dropWeight: string;
}

export interface ItemFormIssue {
  path: string;
  message: string;
}

export interface ItemDefinitionBuildResult {
  definition?: ItemDefinition;
  errors: readonly ItemFormIssue[];
}

export function emptyItemForm(contentId: string): ItemFormValues {
  return {
    contentId,
    itemType: 'consumable',
    displayName: '',
    description: '',
    iconId: 'demo:icon/placeholder',
    stackLimit: '20',
    priceMinor: '0',
    rewardValueMinor: '0',
    dropWeight: '0',
  };
}

export function definitionToForm(definition: ItemDefinition): ItemFormValues {
  return {
    contentId: definition.content_id,
    itemType: definition.public.item_type,
    displayName: definition.public.display_name,
    description: definition.public.description ?? '',
    iconId: definition.public.icon_id,
    stackLimit: String(definition.public.stack_limit),
    priceMinor: definition.public.price_minor,
    rewardValueMinor: definition.server.reward_value_minor,
    dropWeight: String(definition.server.drop_weight_basis_points),
  };
}

export function createItemDefinition(
  gameId: string,
  form: ItemFormValues,
): ItemDefinitionBuildResult {
  const errors: ItemFormIssue[] = [];
  const contentID = form.contentId.trim();
  if (!/^[a-z0-9][a-z0-9._:/-]{0,127}$/.test(contentID)) {
    errors.push({
      path: '/content_id',
      message: '内容 ID 必须以小写字母或数字开头，并使用受支持字符。',
    });
  }
  if (form.displayName.trim().length === 0 || form.displayName.length > 80) {
    errors.push({ path: '/public/display_name', message: '物品名称为必填项，最多 80 个字符。' });
  }
  if (form.description.length > 512) {
    errors.push({ path: '/public/description', message: '描述最多 512 个字符。' });
  }

  const stackLimit = parseInteger(form.stackLimit, 1, 9999);
  if (stackLimit === null) {
    errors.push({ path: '/public/stack_limit', message: '最大堆叠数必须是 1 到 9999 的整数。' });
  }
  const dropWeight = parseInteger(form.dropWeight, 0, 10000);
  if (dropWeight === null) {
    errors.push({
      path: '/server/drop_weight_basis_points',
      message: '掉落权重必须是 0 到 10000 的整数。',
    });
  }
  if (!/^(0|[1-9][0-9]{0,17})$/.test(form.priceMinor)) {
    errors.push({
      path: '/public/price_minor',
      message: '价格必须是最多 18 位的非负十进制整数，不含小数点。',
    });
  }
  if (!/^(0|[1-9][0-9]{0,17})$/.test(form.rewardValueMinor)) {
    errors.push({
      path: '/server/reward_value_minor',
      message: '奖励值必须是最多 18 位的非负十进制整数，不含小数点。',
    });
  }
  if (errors.length > 0 || stackLimit === null || dropWeight === null) {
    return { errors: errors.sort((left, right) => left.path.localeCompare(right.path, 'en')) };
  }

  return {
    definition: {
      game_id: gameId,
      content_id: contentID,
      revision: 1,
      schema_version: 1,
      public: {
        game_id: gameId,
        content_id: contentID,
        revision: 1,
        schema_version: 1,
        item_type: form.itemType,
        display_name: form.displayName.trim(),
        ...(form.description.length === 0 ? {} : { description: form.description }),
        icon_id: form.iconId,
        stack_limit: stackLimit,
        price_minor: form.priceMinor,
      },
      server: {
        reward_value_minor: form.rewardValueMinor,
        drop_weight_basis_points: dropWeight,
      },
    },
    errors: [],
  };
}

function parseInteger(value: string, minimum: number, maximum: number): number | null {
  if (!/^(0|[1-9][0-9]{0,4})$/.test(value)) {
    return null;
  }
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed >= minimum && parsed <= maximum ? parsed : null;
}
