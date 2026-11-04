// The options page in the real, loaded extension: the service worker refuses an
// endpoint off loopback, the page says why, and nothing is saved.
import { test, expect } from './fixtures.js';

test('options page refuses a non-loopback endpoint and keeps the saved one', async ({
  context,
  extensionId,
  serviceWorker,
}) => {
  const page = await context.newPage();
  await page.goto(`chrome-extension://${extensionId}/options.html`);
  const endpoint = page.locator('[data-testid="endpoint"]');
  await expect(endpoint).toHaveValue('http://127.0.0.1:4318/v1/logs');

  await endpoint.fill('https://collector.example.com:4318/v1/logs');
  await page.locator('#save').click();
  await expect(page.locator('[data-testid="settings-error"]')).toBeVisible();
  await expect(page.locator('[data-testid="settings-error"]')).toContainText('127.0.0.1');

  const stored = await serviceWorker.evaluate(async () => (await chrome.storage.local.get('settings')).settings);
  expect(stored.endpoint).toBe('http://127.0.0.1:4318/v1/logs');

  await endpoint.fill('http://localhost:4319/v1/logs');
  await page.locator('#save').click();
  await expect(page.locator('#saved')).toBeVisible();
  await expect(page.locator('[data-testid="settings-error"]')).toBeHidden();
  await expect
    .poll(() => serviceWorker.evaluate(async () => (await chrome.storage.local.get('settings')).settings.endpoint))
    .toBe('http://localhost:4319/v1/logs');
});

// Regression for 68b99ad0: the site checkboxes had no label, so neither a screen reader nor a
// click on the site name reached them.
test('options page site toggles are named by their site and save what the user clicked', async ({
  context,
  extensionId,
  serviceWorker,
}) => {
  const page = await context.newPage();
  await page.goto(`chrome-extension://${extensionId}/options.html`);
  const claude = page.getByRole('checkbox', { name: 'claude.ai', exact: true });
  const chatgpt = page.getByRole('checkbox', { name: 'chatgpt.com', exact: true });
  await expect(claude).toBeChecked();
  await expect(chatgpt).toBeChecked();

  await page.getByText('claude.ai', { exact: true }).click();
  await expect(claude).not.toBeChecked();
  await expect(chatgpt).toBeChecked();
  await page.locator('#save').click();
  await expect(page.locator('#saved')).toBeVisible();
  await expect
    .poll(() => serviceWorker.evaluate(async () => (await chrome.storage.local.get('settings')).settings.sites))
    .toEqual({ claude_web: false, chatgpt_web: true });
});
