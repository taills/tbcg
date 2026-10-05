import type { ValidateFunction } from 'ajv';
import type { ItemDefinition, ItemPublic } from './item-schema.js';

export const validateDefinition: ValidateFunction<ItemDefinition>;
export const validatePublic: ValidateFunction<ItemPublic>;
