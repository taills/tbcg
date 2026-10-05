import { createHash } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import Ajv2020 from 'ajv/dist/2020.js';
import standaloneCode from 'ajv/dist/standalone/index.js';

const adminDirectory = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const publicSchemaPath = resolve(adminDirectory, '../contracts/content/item-public.schema.json');
const definitionSchemaPath = resolve(adminDirectory, '../contracts/content/item.schema.json');
const outputPath = resolve(
  adminDirectory,
  'src/shared/content/item-schema-validators.generated.mjs',
);
const publicSchemaText = await readFile(publicSchemaPath, 'utf8');
const definitionSchemaText = await readFile(definitionSchemaPath, 'utf8');
const publicSchema = JSON.parse(publicSchemaText);
const definitionSchema = JSON.parse(definitionSchemaText);
const schemaHash = createHash('sha256')
  .update(publicSchemaText)
  .update('\0')
  .update(definitionSchemaText)
  .digest('hex');

const ajv = new Ajv2020({
  allErrors: true,
  strict: true,
  ownProperties: true,
  code: { source: true, esm: true },
});
ajv.addSchema(publicSchema);
ajv.compile(definitionSchema);

let validatorModule = standaloneCode(ajv, {
  validatePublic: publicSchema.$id,
  validateDefinition: definitionSchema.$id,
});
let helperCount = 0;
validatorModule = validatorModule.replace(
  /const ([A-Za-z_$][\w$]*) = require\("ajv\/dist\/runtime\/ucs2length"\)\.default;/g,
  (_match, helperName) => {
    helperCount += 1;
    return `const ${helperName} = ucs2length;`;
  },
);
const ucs2lengthImplementation = `function ucs2length(str) {
  const len = str.length;
  let length = 0;
  let pos = 0;
  let value;
  while (pos < len) {
    length++;
    value = str.charCodeAt(pos++);
    if (value >= 0xd800 && value <= 0xdbff && pos < len) {
      // high surrogate and next character
      value = str.charCodeAt(pos);
      if ((value & 0xfc00) === 0xdc00) {
        pos++; // low surrogate
      }
    }
  }
  return length;
}`;
validatorModule = `${ucs2lengthImplementation}\n${validatorModule}`;
if (helperCount !== 1 || /\brequire\(|new Function|eval\(/.test(validatorModule)) {
  throw new Error('Generated validators contain unsupported runtime code or helpers');
}

const ajvLicense = `/*
 * The MIT License (MIT)
 * Copyright (c) 2015-2021 Evgeny Poberezkin
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */`;

const generatedFile = [
  ajvLicense,
  `// Generated with Ajv 8.20.0 from shared content schemas; SHA-256 ${schemaHash}.`,
  '// ucs2length is inlined verbatim from ajv/dist/runtime/ucs2length.js for CSP compatibility.',
  '// Regenerate with pnpm generate:item-validators; do not edit manually.',

  validatorModule,
  '',
].join('\n');

if (process.argv.includes('--check')) {
  let currentFile;
  try {
    currentFile = await readFile(outputPath, 'utf8');
  } catch {
    console.error('Generated validators are missing; run pnpm generate:item-validators.');
    process.exitCode = 1;
  }
  if (currentFile !== undefined && currentFile !== generatedFile) {
    console.error('Generated validators are stale; run pnpm generate:item-validators.');
    process.exitCode = 1;
  }
  if (process.exitCode !== 1) {
    console.log('Generated schema validators match the shared contracts.');
  }
} else {
  await writeFile(outputPath, generatedFile);
  console.log(`Wrote CSP-safe validators for schema SHA-256 ${schemaHash}.`);
}
