import { expect, test } from '@playwright/test';

test('files changed: tree, counts, diffs in order, reads and missing diffs', async ({ page }) => {
  await page.goto('/session.html?id=rich&lens=files-changed');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  // Three files written (two edits to retry.go), one read that is not listed.
  await expect(frame.locator('#summary')).toHaveText('3 files changed in 4 edits · +4 −2 · 1 read');
  const files = frame.locator('#tree button.row');
  await expect(files).toHaveCount(3);
  await expect(files.nth(0)).toContainText('retry.md');
  await expect(files.nth(0)).toContainText('new');
  await expect(frame.locator('#tree')).not.toContainText('README.md');

  // The first file is selected; paths are relative to the working directory.
  await expect(frame.locator('#detail h2')).toHaveText('docs/retry.md');

  await files.filter({ hasText: 'retry.go' }).click();
  await expect(frame.locator('#detail h2')).toHaveText('internal/retry/retry.go');
  const edits = frame.locator('#detail .edit');
  await expect(edits).toHaveCount(2);
  await expect(edits.nth(0)).toContainText('#3 · modify');
  await expect(edits.nth(0).locator('pre .del')).toContainText('rand.Intn');
  await expect(edits.nth(0).locator('pre .add')).toContainText('time.Duration(n)');
  await expect(edits.nth(1)).toContainText('#7 · modify');

  await files.filter({ hasText: 'jitter.go' }).click();
  await expect(files.filter({ hasText: 'jitter.go' })).toContainText('deleted');
  await expect(frame.locator('#detail')).toContainText('Diff not retained · 900 bytes · sha256:ddd');
});

test('files changed: a trace without writes says so', async ({ page }) => {
  await page.goto('/session.html?id=s1&lens=files-changed');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#summary')).toHaveText('No files were changed in this trace.');
});
