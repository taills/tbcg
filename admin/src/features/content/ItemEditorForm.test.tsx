import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { emptyItemForm } from '../../shared/content/item-form';
import ItemEditorForm from './ItemEditorForm';

describe('ItemEditorForm', () => {
  it('renders labelled fields, licensed icon selection, preview, and field errors', () => {
    const onChange = vi.fn();
    const form = {
      ...emptyItemForm('demo:item/healing-potion'),
      displayName: '回复药剂',
      priceMinor: '9007199254740993',
    };
    const { container } = render(
      <ItemEditorForm
        values={form}
        fieldErrors={{ '/public/icon_id': '图标引用无效' }}
        readOnly={false}
        lockContentId={false}
        onChange={onChange}
      />,
    );

    expect(screen.getByRole('textbox', { name: '物品名称' })).toHaveValue('回复药剂');
    expect(screen.getByRole('textbox', { name: '价格（最小货币单位）' })).toHaveValue(
      '9007199254740993',
    );
    expect(screen.getByRole('combobox', { name: '图标' })).toHaveValue('demo:icon/placeholder');
    expect(screen.getByRole('heading', { name: '公开预览' })).toBeInTheDocument();
    expect(container.querySelector('img')).toHaveAttribute(
      'src',
      '/assets/demo/icons/original-placeholder.svg',
    );
    expect(screen.getByText('图标引用无效')).toBeInTheDocument();

    fireEvent.change(screen.getByRole('textbox', { name: '物品名称' }), {
      target: { value: '新名称' },
    });
    expect(onChange).toHaveBeenCalledWith('displayName', '新名称');
  });

  it('shows empty preview fallbacks, every field error, and preserves select values', () => {
    const onChange = vi.fn();
    render(
      <ItemEditorForm
        values={{ ...emptyItemForm(''), priceMinor: '', stackLimit: '' }}
        fieldErrors={{
          '/content_id': '内容 ID 无效',
          '/public/display_name': '名称必填',
          '/public/description': '描述过长',
          '/public/icon_id': '图标无效',
          '/public/stack_limit': '堆叠数无效',
          '/public/price_minor': '价格无效',
          '/server/reward_value_minor': '奖励值无效',
          '/server/drop_weight_basis_points': '权重无效',
        }}
        readOnly={false}
        lockContentId={false}
        onChange={onChange}
      />,
    );

    expect(screen.getByText('未命名物品')).toBeInTheDocument();
    expect(screen.getByText('尚未填写描述')).toBeInTheDocument();
    for (const message of [
      '内容 ID 无效',
      '名称必填',
      '描述过长',
      '图标无效',
      '堆叠数无效',
      '价格无效',
      '奖励值无效',
      '权重无效',
    ]) {
      expect(screen.getByText(message)).toBeInTheDocument();
    }
    fireEvent.change(screen.getByRole('combobox', { name: '物品类型' }), {
      target: { value: 'equipment' },
    });
    expect(onChange).toHaveBeenCalledWith('itemType', 'equipment');
    fireEvent.change(screen.getByRole('combobox', { name: '图标' }), {
      target: { value: 'demo:icon/placeholder' },
    });
    expect(onChange).toHaveBeenCalledWith('iconId', 'demo:icon/placeholder');
  });

  it('disables every editor control in read-only mode', () => {
    render(
      <ItemEditorForm
        values={emptyItemForm('demo:item/healing-potion')}
        fieldErrors={{}}
        readOnly
        lockContentId
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByRole('textbox', { name: '物品名称' })).toBeDisabled();
    expect(screen.getByRole('textbox', { name: '内容 ID' })).toBeDisabled();
    expect(screen.getByRole('combobox', { name: '物品类型' })).toBeDisabled();
    expect(screen.getByRole('textbox', { name: '价格（最小货币单位）' })).toBeDisabled();
  });
});
