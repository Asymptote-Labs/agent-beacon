import { test, expect, seedSettings } from './fixtures.js';

test('rapid popup changes preserve both independently edited settings', async ({ context, extensionId, serviceWorker }) => {
  await seedSettings(serviceWorker, { enabled: false, retention: 'full' });
  const page = await context.newPage();
  await page.goto(`chrome-extension://${extensionId}/popup.html`);
  await expect(page.locator('#endpoint')).toHaveText('http://127.0.0.1:4318/v1/logs');
  await expect(page.locator('#enabled')).not.toBeChecked();
  await expect(page.locator('#retention')).toHaveValue('full');
  await page.evaluate(() => {
    (window as unknown as { refreshes: number }).refreshes = 0;
    chrome.storage.onChanged.addListener((changes, area) => {
      if (area === 'local' && changes.settings) {
        (window as unknown as { refreshes: number }).refreshes += 1;
      }
    });
    const enabled = document.getElementById('enabled') as HTMLInputElement;
    const retention = document.getElementById('retention') as HTMLSelectElement;
    enabled.checked = true;
    enabled.dispatchEvent(new Event('change', { bubbles: true }));
    retention.value = 'metadata';
    retention.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await expect.poll(() => page.evaluate(() => (window as unknown as { refreshes: number }).refreshes)).toBeGreaterThanOrEqual(2);
  await page.reload();
  await expect(page.locator('#endpoint')).toHaveText('http://127.0.0.1:4318/v1/logs');
  await expect(page.locator('#enabled')).toBeChecked();
  await expect(page.locator('#retention')).toHaveValue('metadata');
});
