import { expect, test } from '@playwright/test';

test('skills: each skill, where it entered the run, and what the agent was told', async ({ page }) => {
  await page.goto('/session.html?id=skilled&lens=skills');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#root')).toBeVisible();

  // A Skill tool call (twice), two shell reads of a SKILL.md and a file read of one. Edits of a SKILL.md
  // are not loads, whether the event names the write in its operation or only in its action.
  await expect(frame.locator('#summary')).toHaveText('3 skills loaded 5 times · instructions captured for 2 of 5.');
  const skills = frame.locator('#list button.row');
  await expect(skills).toHaveCount(3);
  await expect(skills.nth(0)).toContainText('pdf');
  await expect(skills.nth(0)).toContainText('×2');
  await expect(skills.nth(1)).toContainText('brand-voice');
  await expect(skills.nth(1)).toContainText('×2');
  await expect(skills.nth(2)).toContainText('release-notes');

  // Claude Code's unnamed OTLP activation is counted, not listed.
  await expect(frame.locator('#notes')).toContainText('also reported 1 skill activation over telemetry');

  // pdf: the call and its result are one load, entered at the call, with the result's text verbatim.
  const detail = frame.locator('#detail');
  await expect(detail.locator('h2')).toHaveText('pdf');
  const loads = detail.locator('.load');
  await expect(loads).toHaveCount(2);
  await expect(loads.nth(0)).toContainText('Event #3');
  await expect(loads.nth(0)).toContainText('Turn the report into a PDF');
  await expect(loads.nth(0)).toContainText('Tool call: Skill');
  await expect(loads.nth(0)).toContainText('#3, #4');
  await expect(loads.nth(0).locator('pre')).toContainText('Never embed fonts you cannot license.');
  // Skill text is shown as text, never parsed as markup.
  await expect(loads.nth(0).locator('pre')).toContainText('<script>window.pwned = 1</script>');
  expect(await page.frameLocator('iframe.lens-frame').locator('body').evaluate(() => (window as any).pwned)).toBeUndefined();
  // The second load returned only a status object: the lens says so and shows it.
  await expect(loads.nth(1)).toContainText('Now check the release notes');
  await expect(loads.nth(1)).toContainText('carried no skill text');
  await expect(loads.nth(1).locator('pre')).toContainText('"commandName": "pdf"');

  // A shell command that printed a SKILL.md: its output is what the agent was told.
  await skills.nth(1).click();
  await expect(detail.locator('h2')).toHaveText('brand-voice');
  await expect(loads.nth(0)).toContainText('Shell command: cat ~/.codex/skills/brand-voice/SKILL.md');
  await expect(loads.nth(0).locator('pre')).toContainText('No exclamation marks.');
  // A shell read whose output was not retained keeps its hash and size, from the event's content.
  await expect(loads.nth(1)).toContainText('The text was not retained · retention metadata · 512 bytes · sha256:fff.');

  // A read whose content was not retained: hash and size instead of text.
  await skills.nth(2).click();
  await expect(detail).toContainText('Read of SKILL.md');
  await expect(detail).toContainText('/home/u/.agents/skills/release-notes/SKILL.md');
  await expect(detail).toContainText('The text was not retained · retention metadata · 2,048 bytes · sha256:eee.');
});

test('skills: a trace without skills says so and how loads are recognised', async ({ page }) => {
  await page.goto('/session.html?id=rich&lens=skills');
  const frame = page.frameLocator('iframe.lens-frame');
  await expect(frame.locator('#summary')).toHaveText('No skills were loaded in this trace.');
  await expect(frame.locator('#notes')).toContainText('a read of a SKILL.md file');
  await expect(frame.locator('#layout')).toBeHidden();
});
