import { defineConfig } from '@playwright/test';

const baseURL = process.env.TBCG_ADMIN_E2E_BASE_URL ?? 'https://127.0.0.1:8443';
const serverCommand = 'node ../tools/admin-e2e/start-server.mjs';
const serverEnvironmentKeys = new Set([
  'PATH',
  'HOME',
  'TMPDIR',
  'TMP',
  'TEMP',
  'LANG',
  'LC_ALL',
  'LC_CTYPE',
  'USER',
  'LOGNAME',
  'SHELL',
  'GOTOOLCHAIN',
  'GOROOT',
  'GOMODCACHE',
  'GOCACHE',
  'GOPROXY',
  'GOSUMDB',
  'TBCG_ADMIN_E2E_SERVER_ENV_FILE',
  'TBCG_ADMIN_BIND',
  'TBCG_ADMIN_ORIGIN',
  'TBCG_ADMIN_TLS_CERT',
  'TBCG_ADMIN_TLS_KEY',
  'TBCG_PROJECT_ROOT',
]);
const serverEnvironment = {
  ...Object.fromEntries(
    Object.entries(process.env).filter(
      (entry): entry is [string, string] =>
        serverEnvironmentKeys.has(entry[0]) && entry[1] !== undefined,
    ),
  ),
  ...Object.fromEntries(
    Object.keys(process.env)
      .filter(
        (key) => key.startsWith('TBCG_ADMIN_E2E_') && key !== 'TBCG_ADMIN_E2E_SERVER_ENV_FILE',
      )
      .map((key) => [key, '']),
  ),
  NODE_EXTRA_CA_CERTS: '',
};

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  reporter: 'list',
  outputDir: process.env.TBCG_ADMIN_E2E_OUTPUT_DIR ?? './test-results/secure-e2e-artifacts',
  use: {
    baseURL,
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
  projects: [{ name: 'firefox', use: { browserName: 'firefox' } }],
  webServer: {
    command: serverCommand,
    cwd: '../server',
    env: serverEnvironment,
    url: `${baseURL}/api/v1/session`,
    reuseExistingServer: false,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 15_000 },
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
