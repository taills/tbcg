import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

const adminDirectory = dirname(fileURLToPath(import.meta.url));
const contentSchemaDirectory = resolve(adminDirectory, '../contracts/content');

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    fs: {
      allow: [adminDirectory, contentSchemaDirectory],
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test-setup.ts',
    globals: true,
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/**/*.test.{ts,tsx}', 'src/test-setup.ts', 'src/main.tsx'],
      thresholds: { statements: 80, branches: 80, functions: 80, lines: 80 },
    },
  },
});
