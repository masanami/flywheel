import { expect, test } from "@playwright/test";

// 課題の詳細の「経緯」を、偽のデータ（mock モード。alpha の C-28 は各種の経路・知らない action・長い発言の要旨・実行の記録を含む）で確かめる。#809

test("AC-435 #809: 幅 360px で、課題の詳細の「経緯」は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/#/a/alpha/challenges/C-28/history");
  await expect(page.getByTestId("detail-history")).toBeVisible();
  // 作業ログの記録の行と実行の記録の行の両方が出ている（空の画面で通らない）
  await expect(page.getByTestId("history-row").first()).toBeVisible();
  expect(await page.getByTestId("history-row").count()).toBeGreaterThan(8);
  await expect(page.getByTestId("history-quote")).toBeVisible();
  const overflows = await page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );
  expect(overflows).toBe(false);
  const body = await page
    .locator(".shell-body")
    .evaluate((el) => el.scrollWidth <= el.clientWidth);
  expect(body).toBe(true);
});
