import { type Page, expect, test } from "@playwright/test";

// 課題の詳細の画面を、偽のデータ（mock モード。alpha の C-28 は 8 列のツリー・長い URL・表とコードを含む計画）で確かめる。#808

const pageOverflows = (page: Page) =>
  page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );

const C28 = "請求データの夜間集計を新しい基盤へ載せ替える";

test("AC-104 #808: 課題の詳細の画面で再読み込みすると、同じ課題の詳細の画面が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-28/plan");
  await expect(page.getByRole("heading", { name: C28 })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: C28 })).toBeVisible();
  expect(page.url()).toContain("#/a/alpha/challenges/C-28/plan");
  await expect(page.getByTestId("detail-plan-view")).toBeVisible();
});

test("AC-105 #808: 課題の画面から課題の詳細へ移った後、ブラウザの「戻る」を押すと、課題の画面に戻る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges");
  await expect(page.getByTestId("challenge-frame").first()).toBeVisible();
  await page.getByRole("link", { name: C28 }).click();
  await expect(page.getByTestId("challenge-detail")).toBeVisible();
  expect(page.url()).toContain("/challenges/C-28");
  await page.goBack();
  await expect(page.getByTestId("challenge-frame").first()).toBeVisible();
  expect(page.url()).toMatch(/#\/a\/alpha\/challenges$/);
});

test("AC-431 #808: 幅 360px で、課題の詳細の画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/#/a/alpha/challenges/C-28");
  await expect(page.getByTestId("detail-source-children")).toBeVisible();
  await expect(page.getByTestId("task-tree")).toBeVisible();
  expect(await pageOverflows(page)).toBe(false);
  // 8 列のツリーは、枠の中で横にスクロールする
  const sizes = await page
    .getByTestId("task-tree")
    .evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
  expect(sizes.scroll).toBeGreaterThan(sizes.client);
  // 画面の中身（shell-body）も、枠より広くならない
  const body = await page
    .locator(".shell-body")
    .evaluate((el) => el.scrollWidth <= el.clientWidth);
  expect(body).toBe(true);
});

test("AC-436 #808: 幅 360px で、課題の詳細の「計画の本文」（表とコードのブロックを含む本文）は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/#/a/alpha/challenges/C-28/plan");
  const plan = page.getByTestId("plan-body");
  await expect(plan.locator("table")).toBeVisible();
  await expect(plan.locator("pre")).toBeVisible();
  expect(await pageOverflows(page)).toBe(false);
  // 長い 1 行のコードは、そのブロックの中で横にスクロールする（表は枠の幅に収まるよう折り返す）
  const code = await plan
    .locator("pre")
    .evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
  expect(code.scroll).toBeGreaterThan(code.client);
  expect(code.client).toBeLessThanOrEqual(360);
  const body = await page
    .locator(".shell-body")
    .evaluate((el) => el.scrollWidth <= el.clientWidth);
  expect(body).toBe(true);
});
