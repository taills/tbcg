import { chmodSync } from 'node:fs';
import { expect, firefox, test } from '@playwright/test';
import type { BrowserContext, Locator, Page } from '@playwright/test';

interface Credentials {
  username: string;
  password: string;
}

interface OwnedPage {
  context: BrowserContext;
  page: Page;
}

function credentials(prefix: string): Credentials {
  const username = process.env[`TBCG_ADMIN_E2E_${prefix}_USERNAME`];
  const password = process.env[`TBCG_ADMIN_E2E_${prefix}_PASSWORD`];
  if (username === undefined || password === undefined) {
    throw new Error(`Missing disposable E2E account variables for ${prefix}.`);
  }
  return { username, password };
}

function profile(name: string): string {
  const path = process.env[`TBCG_ADMIN_E2E_PROFILE_${name}`];
  if (path === undefined) {
    throw new Error(`Missing isolated Firefox NSS profile for ${name}.`);
  }
  return path;
}

function browserEnvironment(): NodeJS.ProcessEnv {
  const allowed = new Set([
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
    'XPC_FLAGS',
    'XPC_SERVICE_NAME',
    'DYLD_LIBRARY_PATH',
    'DYLD_FRAMEWORK_PATH',
    'DYLD_FALLBACK_LIBRARY_PATH',
    'DYLD_FALLBACK_FRAMEWORK_PATH',
    'LD_LIBRARY_PATH',
    'PLAYWRIGHT_BROWSERS_PATH',
  ]);
  return Object.fromEntries(Object.entries(process.env).filter(([key]) => allowed.has(key)));
}

async function selectGameWithDecision(
  page: Page,
  selector: Locator,
  gameID: string,
  decision: 'accept' | 'dismiss',
): Promise<void> {
  const dialogPromise = page.waitForEvent('dialog');
  const selectionPromise = selector.selectOption(gameID);
  const dialog = await dialogPromise;
  expect(dialog.type()).toBe('confirm');
  expect(dialog.message()).toContain('尚未保存的本地编辑');
  await dialog[decision]();
  await selectionPromise;
}

async function captureSafeInitialScreenshot(page: Page, browserEvents: string[]): Promise<void> {
  const path = process.env.TBCG_ADMIN_E2E_SAFE_SCREENSHOT_PATH;
  if (path === undefined) {
    throw new Error('Missing private path for the no-credential page screenshot.');
  }
  try {
    await expect(page.locator('#root')).not.toBeEmpty({ timeout: 10_000 });
  } catch {
    const pageState = await page.evaluate(() => ({
      title: document.title,
      readyState: document.readyState,
      rootChildren: document.querySelector('#root')?.childElementCount ?? -1,
      scriptPaths: [...document.scripts].map((script) => new URL(script.src).pathname),
    }));
    throw new Error(
      `Admin UI did not mount; page=${JSON.stringify(pageState)}; events=${browserEvents.join(',') || 'none'}.`,
    );
  }
  await page.screenshot({ path, fullPage: true });
  chmodSync(path, 0o600);
}

async function openAccount(
  baseURL: string | undefined,
  profileName: string,
  user: Credentials,
  captureInitialPage = false,
): Promise<OwnedPage> {
  const context = await firefox.launchPersistentContext(profile(profileName), {
    headless: true,
    env: browserEnvironment(),
  });
  try {
    const page = await context.newPage();
    const browserEvents: string[] = [];
    page.on('pageerror', (error) => browserEvents.push(`pageerror:${error.name}`));
    page.on('console', (message) => {
      if (message.type() === 'error') browserEvents.push('console:error');
    });
    page.on('requestfailed', (request) => {
      browserEvents.push(`requestfailed:${new URL(request.url()).pathname}`);
    });
    page.on('response', (response) => {
      if (response.status() >= 400) {
        browserEvents.push(`http${response.status()}:${new URL(response.url()).pathname}`);
      }
    });
    await page.goto(baseURL ?? 'https://127.0.0.1:8443');
    if (captureInitialPage) {
      await captureSafeInitialScreenshot(page, browserEvents);
    }
    await expect(page.locator('#root')).not.toBeEmpty({ timeout: 15_000 });
    await expect(page.getByLabel('用户名')).toBeVisible({ timeout: 15_000 });
    await page.getByLabel('用户名').fill(user.username);
    await page.getByLabel('密码').fill(user.password);
    await page.getByRole('button', { name: '登录' }).click();
    await expect(page.getByRole('heading', { name: '内容工作台' })).toBeVisible();
    const cookies = await context.cookies(baseURL);
    expect(cookies.find(({ name }) => name === 'tbcg_admin_session')).toMatchObject({
      secure: true,
      httpOnly: true,
      sameSite: 'Lax',
    });
    expect(cookies.find(({ name }) => name === 'tbcg_admin_csrf')).toMatchObject({
      secure: true,
      httpOnly: false,
      sameSite: 'Lax',
    });
    return { context, page };
  } catch (error: unknown) {
    await context.close();
    throw error;
  }
}

test('untrusted Firefox profile rejects the private CA certificate', async () => {
  const context = await firefox.launchPersistentContext(profile('UNTRUSTED'), {
    headless: true,
    env: browserEnvironment(),
  });
  try {
    const page = await context.newPage();
    const baseURL = process.env.TBCG_ADMIN_E2E_BASE_URL ?? 'https://127.0.0.1:8443';
    let navigationError: unknown;
    try {
      await page.goto(baseURL);
    } catch (error: unknown) {
      navigationError = error;
    }
    expect(navigationError).toBeInstanceOf(Error);
    expect((navigationError as Error).message).toContain('SEC_ERROR_UNKNOWN_ISSUER');
  } finally {
    await context.close();
  }
});

test('trusted Firefox sessions cover draft CAS, immutable review, role denial, and Game scope safety', async ({
  baseURL,
}) => {
  test.setTimeout(240_000);
  const author = credentials('AUTHOR');
  const reviewer = credentials('REVIEWER');
  const reader = credentials('READER');
  const publisher = credentials('PUBLISHER');
  const contentID = `demo:item/e2e-${crypto.randomUUID()}`;
  const owned: OwnedPage[] = [];

  try {
    const authorA = await openAccount(baseURL, 'AUTHOR_A', author, true);
    owned.push(authorA);
    const authorB = await openAccount(baseURL, 'AUTHOR_B', author);
    owned.push(authorB);
    const reviewClient = await openAccount(baseURL, 'REVIEWER', reviewer);
    owned.push(reviewClient);
    const readerClient = await openAccount(baseURL, 'READER', reader);
    owned.push(readerClient);
    const publisherClient = await openAccount(baseURL, 'PUBLISHER', publisher);
    owned.push(publisherClient);
    await expect(authorA.page.getByLabel('游戏')).toHaveValue('demo');

    const authorSession = await authorA.page.evaluate(async () => {
      const response = await fetch('/api/v1/session', { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`Session lookup returned HTTP ${response.status}`);
      return (await response.json()) as { data: { csrf_token: string } };
    });
    const originRejected = await authorA.context.request.post(
      new URL('/api/v1/logout', authorA.page.url()).toString(),
      {
        headers: {
          Origin: 'https://attacker.invalid',
          'X-CSRF-Token': authorSession.data.csrf_token,
        },
      },
    );
    expect(originRejected.status()).toBe(403);
    const csrfRejected = await authorA.context.request.post(
      new URL('/api/v1/logout', authorA.page.url()).toString(),
      {
        headers: {
          Origin: new URL(authorA.page.url()).origin,
          'X-CSRF-Token': 'invalid-e2e-csrf-token',
        },
      },
    );
    expect(csrfRejected.status()).toBe(403);

    expect(new URL(authorA.page.url()).protocol).toBe('https:');
    await authorA.page.getByLabel('内容 ID').fill(contentID);
    await authorA.page.getByRole('textbox', { name: '物品名称' }).fill('独立浏览器物品');
    await authorA.page
      .getByRole('textbox', { name: '价格（最小货币单位）' })
      .fill('9007199254740993');
    await authorA.page.getByRole('button', { name: '保存草稿' }).click();
    await expect(authorA.page.getByRole('status')).toContainText('草稿已保存');

    const draftLink = await authorA.page
      .getByRole('link', { name: '复制草稿链接' })
      .getAttribute('href');
    expect(draftLink).not.toBeNull();
    await authorB.page.goto(new URL(draftLink ?? '/', authorA.page.url()).toString());
    await expect(authorB.page.getByRole('textbox', { name: '物品名称' })).toHaveValue(
      '独立浏览器物品',
    );

    await authorA.page.getByRole('textbox', { name: '物品名称' }).fill('浏览器 A 更新');
    await authorA.page.getByRole('button', { name: '保存草稿' }).click();
    await expect(authorA.page.getByRole('status')).toContainText('草稿已保存');

    await authorB.page.getByRole('textbox', { name: '物品名称' }).fill('浏览器 B 本地输入');
    await authorB.page.getByRole('button', { name: '保存草稿' }).click();
    await expect(authorB.page.getByRole('heading', { name: '草稿版本冲突' })).toBeVisible();
    await expect(authorB.page.getByRole('textbox', { name: '物品名称' })).toHaveValue(
      '浏览器 B 本地输入',
    );
    const conflict = authorB.page.getByRole('region', { name: '草稿版本冲突' });
    await expect(conflict).toContainText('浏览器 A 更新');
    await expect(conflict).toContainText('浏览器 B 本地输入');

    await authorB.page.getByRole('button', { name: '将本地更改重应用到最新版本' }).click();
    await expect(authorB.page.getByRole('status')).toContainText('本地更改已明确重应用');
    await authorA.page.reload();
    await expect(authorA.page.getByRole('textbox', { name: '物品名称' })).toHaveValue(
      '浏览器 B 本地输入',
    );
    await authorA.page.getByRole('button', { name: '校验草稿' }).click();
    await expect(authorA.page.getByRole('status')).toContainText('校验通过');
    await authorA.page.getByRole('button', { name: '提交不可变 revision' }).click();
    await expect(authorA.page.getByRole('heading', { name: /revision 1 已提交/ })).toBeVisible();
    const submittedHash = await authorA.page
      .getByRole('region', { name: '已提交 revision' })
      .locator('p')
      .nth(0)
      .innerText();
    await reviewClient.page.getByRole('button', { name: '刷新队列' }).click();

    const reviewRow = reviewClient.page.getByRole('listitem').filter({ hasText: contentID });
    await expect(reviewRow).toBeVisible();
    const hashLabel = await reviewRow.locator('p').nth(1).innerText();
    const bodyHash = hashLabel.replace('SHA-256：', '').trim();
    expect(bodyHash).toMatch(/^[a-f0-9]{64}$/);
    expect(submittedHash).toContain(bodyHash);
    await reviewRow.getByRole('button', { name: '批准 revision 1' }).click();
    await expect(reviewClient.page.getByRole('status')).toContainText(
      `已批准 revision 1（${contentID}）`,
    );

    const gameSelector = authorA.page.getByLabel('游戏');
    await expect(gameSelector).toBeVisible();
    const originalGame = await gameSelector.inputValue();
    const games = await gameSelector
      .locator('option')
      .evaluateAll((options) => options.map((option) => (option as HTMLOptionElement).value));
    const otherGame = games.find((game) => game !== originalGame);
    expect(otherGame).toBeDefined();
    if (otherGame !== undefined) {
      const nameField = authorA.page.getByRole('textbox', { name: '物品名称' });
      await nameField.fill('确认切换后应丢弃的本地输入');
      await selectGameWithDecision(authorA.page, gameSelector, otherGame, 'dismiss');
      await expect(gameSelector).toHaveValue(originalGame);
      await expect(nameField).toHaveValue('确认切换后应丢弃的本地输入');

      await selectGameWithDecision(authorA.page, gameSelector, otherGame, 'accept');
      await expect(gameSelector).toHaveValue(otherGame);
      await expect(nameField).toHaveValue('');
      await gameSelector.selectOption(originalGame);
      await expect(gameSelector).toHaveValue(originalGame);
      await expect(nameField).toHaveValue('浏览器 B 本地输入');
    }

    for (const readOnlyClient of [readerClient, publisherClient]) {
      await expect(readOnlyClient.page.getByRole('textbox', { name: '物品名称' })).toBeDisabled();
      await expect(readOnlyClient.page.getByRole('button', { name: '保存草稿' })).toHaveCount(0);
    }
    const csrfSession = await publisherClient.page.evaluate(async () => {
      const response = await fetch('/api/v1/session', { credentials: 'same-origin' });
      if (!response.ok) throw new Error(`Session lookup returned HTTP ${response.status}`);
      return (await response.json()) as { data: { csrf_token: string } };
    });
    const forbidden = await publisherClient.page.evaluate(
      async ({ contentID, bodyHash, csrfToken }) => {
        const response = await fetch('/api/v1/games/demo/revision-approvals', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
          body: JSON.stringify({
            content_id: contentID,
            revision: 1,
            body_hash: bodyHash,
            decision: 'approved',
          }),
        });
        return response.status;
      },
      { contentID, bodyHash, csrfToken: csrfSession.data.csrf_token },
    );
    expect(forbidden).toBe(403);

    const readerForbidden = await readerClient.page.evaluate(async () => {
      const sessionResponse = await fetch('/api/v1/session', { credentials: 'same-origin' });
      if (!sessionResponse.ok)
        throw new Error(`Session lookup returned HTTP ${sessionResponse.status}`);
      const session = (await sessionResponse.json()) as { data: { csrf_token: string } };
      const response = await fetch('/api/v1/games/demo/item-drafts', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': session.data.csrf_token },
        body: JSON.stringify({}),
      });
      return response.status;
    });
    expect(readerForbidden).toBe(403);

    const publisherDraftForbidden = await publisherClient.page.evaluate(async (csrfToken) => {
      const response = await fetch('/api/v1/games/demo/item-drafts', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({}),
      });
      return response.status;
    }, csrfSession.data.csrf_token);
    expect(publisherDraftForbidden).toBe(403);

    // Publisher builds the exact approved revision through the real restricted worker process.
    await expect(reviewClient.page.getByRole('heading', { name: '单物品构建' })).toHaveCount(0);
    const buildPanel = publisherClient.page.getByRole('region', { name: '单物品构建' });
    await buildPanel.getByRole('button', { name: '刷新候选' }).click();
    const buildRow = buildPanel.getByRole('listitem').filter({ hasText: contentID });
    await expect(buildRow).toContainText(bodyHash);
    await buildRow.getByRole('button', { name: '构建 revision 1' }).click();
    const buildStatus = buildRow.getByRole('status');
    await expect(buildStatus).toContainText('已构建，尚未激活', { timeout: 90_000 });
    await expect(buildRow).toContainText('公开包 SHA-256：');
    const releaseID = /Release (\S+)（/.exec(await buildStatus.innerText())?.[1];
    expect(releaseID).toMatch(/^build-[a-f0-9]{32}$/);
    // Private packages live outside every served root: SPA routes fall back to the HTML shell
    // and the demo asset route returns 404, never package JSON.
    for (const privatePath of [`/${releaseID}.private.json`, `/demo/${releaseID}.private.json`]) {
      const exposed = await publisherClient.context.request.get(
        new URL(privatePath, publisherClient.page.url()).toString(),
      );
      expect(exposed.status()).toBe(200);
      expect(exposed.headers()['content-type']).toContain('text/html');
      const body = await exposed.text();
      expect(body).not.toContain(String(releaseID));
      expect(body).not.toContain('"server"');
    }
    const assetProbe = await publisherClient.context.request.get(
      new URL(`/assets/demo/${releaseID}.private.json`, publisherClient.page.url()).toString(),
    );
    expect(assetProbe.status()).toBe(404);

    const nonPublisherBuildStatuses = await Promise.all(
      [authorA, reviewClient, readerClient].map(({ page }) =>
        page.evaluate(
          async ({ contentID, bodyHash }) => {
            const sessionResponse = await fetch('/api/v1/session', { credentials: 'same-origin' });
            const session = (await sessionResponse.json()) as { data: { csrf_token: string } };
            const candidates = await fetch('/api/v1/games/demo/build-candidates', {
              credentials: 'same-origin',
            });
            const start = await fetch('/api/v1/games/demo/builds', {
              method: 'POST',
              credentials: 'same-origin',
              headers: {
                'Content-Type': 'application/json',
                'X-CSRF-Token': session.data.csrf_token,
                'Idempotency-Key': crypto.randomUUID(),
              },
              body: JSON.stringify({ content_id: contentID, revision: 1, body_hash: bodyHash }),
            });
            return [candidates.status, start.status];
          },
          { contentID, bodyHash },
        ),
      ),
    );
    expect(nonPublisherBuildStatuses).toEqual([
      [403, 403],
      [403, 403],
      [403, 403],
    ]);
  } finally {
    await Promise.all(owned.map(({ context }) => context.close()));
  }
});
