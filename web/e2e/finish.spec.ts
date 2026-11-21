import { expect, test } from "@playwright/test";

// 区切り S2c の仕上げ（#814）。実際のブラウザで、どの画面にも入力欄が無いこと（AC-118）と、
// 画面の本文に「ワークスペース」「1 周」の語が出ないこと（AC-443・AC-444）を、偽のデータ（mock モード）の全画面で確かめる。
const SCREENS = [
  "#/all",
  "#/a/alpha/now",
  "#/a/alpha/challenges",
  "#/a/alpha/followups",
  "#/a/alpha/cycle",
];

for (const hash of SCREENS) {
  test(`AC-118・AC-443・AC-444 #814: ${hash} に、入力欄と禁止の語が無い`, async ({
    page,
  }) => {
    await page.goto(`/${hash}`);
    await expect(page.getByRole("heading", { level: 2 }).first()).toBeVisible();
    await expect(page.locator("input, textarea, select")).toHaveCount(0);
    await expect(page.locator("[contenteditable]")).toHaveCount(0);
    const text = await page.locator("body").innerText();
    expect(text).not.toContain("ワークスペース");
    expect(text).not.toMatch(/[1１]\s*周/);
  });
}
