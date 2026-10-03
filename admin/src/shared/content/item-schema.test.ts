import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { projectPublicItem, validateItemDefinition, validatePublicItem } from './item-schema';

const fixtureDirectory = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../../tests/fixtures/content',
);

function loadFixture(name: string): unknown {
  const fixturePath = resolve(fixtureDirectory, name);
  return JSON.parse(readFileSync(fixturePath, 'utf8')) as unknown;
}

function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('Expected object fixture');
  }

  return value as Record<string, unknown>;
}

describe('item content schema', () => {
  it('accepts a valid full definition and preserves large integer strings', () => {
    const result = validateItemDefinition(loadFixture('item.valid.json'));

    expect(result.valid).toBe(true);
    expect(result.errors).toEqual([]);
    expect(result.data?.public.price_minor).toBe('9007199254740993');
    expect(result.data?.server.reward_value_minor).toBe('250000000000000001');
  });

  it('requires public identity fields to match the full Definition', () => {
    const definition = asRecord(loadFixture('item.valid.json'));
    const publicItem = asRecord(definition.public);
    const mismatched = {
      ...definition,
      game_id: 'other-game',
      content_id: 'other:item/healing-potion',
      revision: 2,
      schema_version: 1,
      public: {
        ...publicItem,
        game_id: 'demo',
        content_id: 'demo:item/healing-potion',
        revision: 1,
        schema_version: 1,
      },
    };
    const result = validateItemDefinition(mismatched);

    expect(result.valid).toBe(false);
    expect(result.errors).toEqual(
      expect.arrayContaining([
        { path: '/public/game_id', code: 'invalid_value' },
        { path: '/public/content_id', code: 'invalid_value' },
        { path: '/public/revision', code: 'invalid_value' },
      ]),
    );
  });

  it('returns Schema issues without identity issues when Schema validation fails', () => {
    const definition = asRecord(loadFixture('item.invalid-enum.json'));
    const invalidDefinition = { ...definition, game_id: 'other-game' };
    const result = validateItemDefinition(invalidDefinition);

    expect(result.valid).toBe(false);
    expect(result.errors).toEqual([{ path: '/public/item_type', code: 'invalid_value' }]);
  });

  it('orders issue paths by UTF-8 bytes rather than locale', () => {
    const publicItem = asRecord(loadFixture('item.public.valid.json'));
    const result = validatePublicItem({ ...publicItem, z: true, ä: true });

    expect(result.errors).toEqual([
      { path: '/z', code: 'unknown_field' },
      { path: '/ä', code: 'unknown_field' },
    ]);
  });

  it('reports enum violations with the public field path and schema code', () => {
    const result = validateItemDefinition(loadFixture('item.invalid-enum.json'));

    expect(result.valid).toBe(false);
    expect(result.errors).toContainEqual({ path: '/public/item_type', code: 'invalid_value' });
  });

  it('reports missing required fields using the shared code and field path', () => {
    const definition = asRecord(loadFixture('item.valid.json'));
    const { revision: _revision, ...missingRevision } = definition;
    const result = validateItemDefinition(missingRevision);

    expect(result.valid).toBe(false);
    expect(result.errors).toContainEqual({ path: '/revision', code: 'missing_field' });
  });

  it('rejects null for an optional but non-nullable field', () => {
    const result = validateItemDefinition(loadFixture('item.invalid-null-optional.json'));

    expect(result.valid).toBe(false);
    expect(result.errors).toContainEqual({ path: '/public/description', code: 'invalid_value' });
  });

  it('rejects numeric values for string-encoded large integers', () => {
    const result = validatePublicItem(loadFixture('item.public.invalid-bigint-number.json'));

    expect(result.valid).toBe(false);
    expect(result.errors).toContainEqual({ path: '/price_minor', code: 'invalid_value' });
  });

  it('validates a public projection and omits server-private fields without mutation', () => {
    const fullItem = loadFixture('item.valid.json');
    const original = JSON.stringify(fullItem);
    const projection = projectPublicItem(fullItem);

    expect(projection.valid).toBe(true);
    expect(JSON.stringify(fullItem)).toBe(original);
    expect(projection.data).toMatchObject(asRecord(loadFixture('item.public.valid.json')));
    expect(projection.data?.description).toBe('一瓶用于冒险的药剂。');
    expect(projection.data).not.toHaveProperty('server');
    expect(projection.data).not.toHaveProperty('reward_value_minor');
    expect(validatePublicItem(projection.data).valid).toBe(true);
  });

  it('rejects server fields in a public projection', () => {
    const publicItem = asRecord(loadFixture('item.public.valid.json'));
    const withPrivateField = { ...publicItem, server: { reward_value_minor: '1' } };
    const result = validatePublicItem(withPrivateField);

    expect(result.valid).toBe(false);
    expect(result.errors).toContainEqual({ path: '/server', code: 'unknown_field' });
  });

  it('不把原型链属性当作必需的 JSON 自有字段', () => {
    const definition = asRecord(loadFixture('item.valid.json'));
    const inherited: unknown = Object.create(definition);
    const result = validateItemDefinition(inherited);
    expect(result.valid).toBe(false);
    expect(result.errors).toContainEqual({ path: '/game_id', code: 'missing_field' });
    expect(projectPublicItem(inherited).data).toBeUndefined();
  });

  it('不为非法定义生成公开数据', () => {
    const result = projectPublicItem(loadFixture('item.invalid-enum.json'));
    expect(result.valid).toBe(false);
    expect(result.data).toBeUndefined();
    expect(result.errors).toContainEqual({ path: '/public/item_type', code: 'invalid_value' });
  });

  it('缺省描述不会被投影补写，也不会改变输入', () => {
    const definition = asRecord(loadFixture('item.valid.json'));
    const { description: _description, ...publicItem } = asRecord(definition.public);
    const input = { ...definition, public: publicItem };
    const projection = projectPublicItem(input);
    expect(projection.valid).toBe(true);
    expect(projection.data).not.toHaveProperty('description');
    expect(input.public).not.toHaveProperty('description');
  });

  it('按规范转义带斜杠和波浪线的字段路径', () => {
    const item = asRecord(loadFixture('item.public.valid.json'));
    const result = validatePublicItem({ ...item, 'a/~': true, a: true });
    expect(result.errors).toEqual([
      { path: '/a', code: 'unknown_field' },
      { path: '/a~1~0', code: 'unknown_field' },
    ]);
  });

  it('rejects unknown input types without throwing', () => {
    expect(validateItemDefinition(null).valid).toBe(false);
    expect(validateItemDefinition('item').valid).toBe(false);
    expect(validatePublicItem(42).valid).toBe(false);
  });
});
