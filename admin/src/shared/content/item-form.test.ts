import { describe, expect, it } from 'vitest';
import { createItemDefinition, definitionToForm, emptyItemForm } from './item-form';

describe('item form mapping', () => {
  it('preserves large prices and rewards as decimal strings', () => {
    const form = {
      ...emptyItemForm('demo:item/healing-potion'),
      displayName: '回复药剂',
      priceMinor: '9007199254740993',
      rewardValueMinor: '250000000000000001',
    };
    const result = createItemDefinition('demo', form);

    expect(result.errors).toEqual([]);
    expect(result.definition?.public.price_minor).toBe('9007199254740993');
    expect(result.definition?.server.reward_value_minor).toBe('250000000000000001');
    expect(typeof result.definition?.public.price_minor).toBe('string');
  });

  it('maps an immutable draft definition back to editable fields', () => {
    const result = createItemDefinition('demo', {
      ...emptyItemForm('demo:item/healing-potion'),
      displayName: '药剂',
      description: '覆盖 optional description 映射',
      priceMinor: '12345',
      rewardValueMinor: '900',
    });
    expect(result.definition).toBeDefined();
    expect(definitionToForm(result.definition!)).toMatchObject({
      contentId: 'demo:item/healing-potion',
      displayName: '药剂',
      priceMinor: '12345',
      rewardValueMinor: '900',
    });
  });

  it('rejects malformed money and bounded integer input without coercion', () => {
    const result = createItemDefinition('demo', {
      ...emptyItemForm('Demo:Invalid'),
      displayName: '',
      description: '过长'.repeat(300),
      priceMinor: '1.50',
      rewardValueMinor: '1e3',
      stackLimit: '1.5',
      dropWeight: '10001',
    });
    expect(result.definition).toBeUndefined();
    expect(result.errors.map((error) => error.path)).toEqual([
      '/content_id',
      '/public/description',
      '/public/display_name',
      '/public/price_minor',
      '/public/stack_limit',
      '/server/drop_weight_basis_points',
      '/server/reward_value_minor',
    ]);
  });

  it('rejects an overlong item name as a separate validation boundary', () => {
    const result = createItemDefinition('demo', {
      ...emptyItemForm('demo:item/healing-potion'),
      displayName: 'x'.repeat(81),
    });
    expect(result.errors.map((error) => error.path)).toContain('/public/display_name');
  });
});
