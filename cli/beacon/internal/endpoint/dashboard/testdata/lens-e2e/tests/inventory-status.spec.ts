import { expect, test, type Page } from '@playwright/test';

function snapshot(path = '') {
  return { generated_at: '2026-10-20T00:00:00Z', harnesses: [], mcp_servers: [], skills: [], hooks: [],
    user_scope: { home_path: '/Users/owned-user' },
    configs: path ? [{ runtime: 'owned-runtime', exists: true, scope: 'user', config_kind: 'mcp_config',
      parser_status: 'parsed', path, path_hash: path }] : [] };
}

async function inventoryFixture(page: Page) {
  await page.clock.install();
  const response = { failure: null as string | null, inventory: snapshot() };
  await page.route('**/api/status', (route) => route.fulfill({ json: { log_path: '/owned/runtime.jsonl' } }));
  await page.route('**/api/inventory', (route) => response.failure === null
    ? route.fulfill({ json: response.inventory })
    : route.fulfill({ status: 503, json: { error: response.failure } }));
  return response;
}

async function poll(page: Page) {
  const refreshed = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/inventory');
  await page.clock.runFor(15_000);
  await (await refreshed).finished();
}

test('inventory initial failure is visible and clears when the poll recovers', async ({ page }) => {
  const response = await inventoryFixture(page);
  response.failure = 'Owned initial inventory failure';
  await page.goto('/inventory.html');
  const status = page.locator('#inventory-meta');
  await expect(status).toBeVisible();
  await expect(status).toHaveText('Failed to load inventory: Owned initial inventory failure');
  response.failure = null;
  response.inventory = snapshot('/owned/recovered.json');
  await poll(page);
  await expect(page.locator('#inventory-active-body')).toContainText('/owned/recovered.json');
  await expect(status).toContainText('Scanned ');
  await expect(status).not.toContainText('Failed');
});

test('inventory refresh failure warns while retaining the last snapshot, then recovers', async ({ page }) => {
  const response = await inventoryFixture(page);
  response.inventory = snapshot('/owned/previous.json');
  await page.goto('/inventory.html');
  const body = page.locator('#inventory-active-body');
  await expect(body).toContainText('/owned/previous.json');
  response.failure = 'Owned refresh failure';
  await poll(page);
  await expect(page.locator('#inventory-meta')).toHaveText('Failed to load inventory: Owned refresh failure');
  await expect(body).toContainText('/owned/previous.json');
  response.failure = null;
  response.inventory = snapshot('/owned/current.json');
  await poll(page);
  await expect(body).toContainText('/owned/current.json');
  await expect(body).not.toContainText('/owned/previous.json');
  await expect(page.locator('#inventory-meta')).toContainText('Scanned ');
  await expect(page.locator('#inventory-meta')).not.toContainText('Failed');
});

test('inventory failure text remains plain text', async ({ page }) => {
  const response = await inventoryFixture(page);
  response.failure = '<em>Owned inventory failure</em>';
  await page.goto('/inventory.html');
  await expect(page.locator('#inventory-meta')).toHaveText('Failed to load inventory: <em>Owned inventory failure</em>');
  await expect(page.locator('#inventory-meta em')).toHaveCount(0);
});

test('inventory healthy empty snapshot stays distinct from a failed read', async ({ page }) => {
  await inventoryFixture(page);
  await page.goto('/inventory.html');
  await expect(page.locator('#inventory-active-body')).toContainText('No matching rows were found');
  await expect(page.locator('#inventory-active-body')).not.toContainText('Loading');
});

test('inventory healthy configurations still render', async ({ page }) => {
  const response = await inventoryFixture(page);
  response.inventory = snapshot('/owned/healthy.json');
  await page.goto('/inventory.html');
  await expect(page.locator('#inventory-active-body')).toContainText('/owned/healthy.json');
  await expect(page.locator('#inventory-active-body')).not.toContainText('Loading');
});

test('existing hooks inventory failure status still works', async ({ page }) => {
  const response = await inventoryFixture(page);
  response.failure = 'Owned hooks inventory failure';
  await page.goto('/inventory-hooks.html');
  await expect(page.locator('#inventory-meta')).toHaveText('Failed to load inventory: Owned hooks inventory failure');
});
