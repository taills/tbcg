import type { ErrorObject, ValidateFunction } from 'ajv';
import { validateDefinition, validatePublic } from './item-schema-validators.generated.mjs';

export interface ItemPublic {
  game_id: string;
  content_id: string;
  revision: number;
  schema_version: 1;
  item_type: 'consumable' | 'equipment' | 'material' | 'quest';
  display_name: string;
  description?: string;
  icon_id: string;
  stack_limit: number;
  price_minor: string;
}

export interface ItemDefinition {
  game_id: string;
  content_id: string;
  revision: number;
  schema_version: 1;
  public: ItemPublic;
  server: {
    reward_value_minor: string;
    drop_weight_basis_points: number;
  };
}

export interface ValidationIssue {
  path: string;
  code: string;
}

export interface ValidationResult<T> {
  valid: boolean;
  data?: T;
  errors: readonly ValidationIssue[];
}

const IDENTITY_FIELDS = ['game_id', 'content_id', 'revision', 'schema_version'] as const;
const utf8Encoder = new TextEncoder();

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function escapePointerSegment(segment: string): string {
  return segment.replaceAll('~', '~0').replaceAll('/', '~1');
}

function getPath(error: ErrorObject): string {
  const path = error.instancePath;
  const params: unknown = error.params;

  if (!isRecord(params)) {
    return path;
  }

  const field = error.keyword === 'required' ? params.missingProperty : params.additionalProperty;
  return typeof field === 'string' ? `${path}/${escapePointerSegment(field)}` : path;
}

function getCode(keyword: string): string {
  if (keyword === 'required') {
    return 'missing_field';
  }
  if (keyword === 'additionalProperties') {
    return 'unknown_field';
  }

  return 'invalid_value';
}

function compareBinaryStrings(left: string, right: string): number {
  const leftBytes = utf8Encoder.encode(left);
  const rightBytes = utf8Encoder.encode(right);
  const sharedLength = Math.min(leftBytes.length, rightBytes.length);

  for (let index = 0; index < sharedLength; index += 1) {
    const leftByte = leftBytes[index] ?? 0;
    const rightByte = rightBytes[index] ?? 0;
    if (leftByte !== rightByte) {
      return leftByte - rightByte;
    }
  }

  return leftBytes.length - rightBytes.length;
}

function sortAndDeduplicateIssues(issues: readonly ValidationIssue[]): ValidationIssue[] {
  const sortedIssues = [...issues].sort(
    (left, right) =>
      compareBinaryStrings(left.path, right.path) || compareBinaryStrings(left.code, right.code),
  );

  return sortedIssues.filter(
    (issue, index, sorted) =>
      index === 0 ||
      issue.path !== sorted[index - 1]?.path ||
      issue.code !== sorted[index - 1]?.code,
  );
}

function getValidationIssues(errors: readonly ErrorObject[] | null | undefined): ValidationIssue[] {
  const issues = (errors ?? []).map((error) => ({
    path: getPath(error),
    code: getCode(error.keyword),
  }));

  return sortAndDeduplicateIssues(issues);
}

function getIdentityIssues(value: unknown): ValidationIssue[] {
  if (!isRecord(value)) {
    return [];
  }

  const publicValue = value.public;
  if (!isRecord(publicValue)) {
    return [];
  }

  return IDENTITY_FIELDS.flatMap((field) => {
    if (
      !Object.hasOwn(value, field) ||
      !Object.hasOwn(publicValue, field) ||
      Object.is(value[field], publicValue[field])
    ) {
      return [];
    }

    return [{ path: `/public/${field}`, code: 'invalid_value' }];
  });
}

function validateValue<T>(value: unknown, validator: ValidateFunction<T>): ValidationResult<T> {
  if (!validator(value)) {
    return { valid: false, errors: getValidationIssues(validator.errors) };
  }

  return { valid: true, data: value, errors: [] };
}

export function validateItemDefinition(value: unknown): ValidationResult<ItemDefinition> {
  const result = validateValue(value, validateDefinition);
  if (!result.valid) {
    return result;
  }

  const identityIssues = getIdentityIssues(value);
  if (identityIssues.length === 0) {
    return result;
  }

  return { valid: false, errors: sortAndDeduplicateIssues(identityIssues) };
}

export function validatePublicItem(value: unknown): ValidationResult<ItemPublic> {
  return validateValue(value, validatePublic);
}

export function projectPublicItem(value: unknown): ValidationResult<ItemPublic> {
  const result = validateItemDefinition(value);
  if (!result.valid || result.data === undefined) {
    return { valid: false, errors: result.errors };
  }

  const definition = result.data;
  const projection: ItemPublic = {
    game_id: definition.public.game_id,
    content_id: definition.public.content_id,
    revision: definition.public.revision,
    schema_version: definition.public.schema_version,
    item_type: definition.public.item_type,
    display_name: definition.public.display_name,
    icon_id: definition.public.icon_id,
    stack_limit: definition.public.stack_limit,
    price_minor: definition.public.price_minor,
    ...(definition.public.description === undefined
      ? {}
      : { description: definition.public.description }),
  };

  return { valid: true, data: projection, errors: [] };
}
