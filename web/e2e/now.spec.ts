import { expect, test } from "@playwright/test";

// 「いま進められること」を、偽のデータ（mock モード。エージェント alpha）で確かめる。#805

test("AC-429 #805: 幅 360px で、「いま進められること」の画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/#/a/alpha/now");
  await expect(
    page.getByRole("heading", { name: "いま進められること" }),
  ).toBeVisible();
  // 取り込めるものが読めてから測る（長いコマンドの行が出た状態）
  await expect(page.getByTestId("ingestable-list")).toBeVisible();
  await expect(page.getByTestId("waiting-list")).toBeVisible();
  const overflow = await page.evaluate(() => ({
    doc:
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
    body: document.body.scrollWidth - document.body.clientWidth,
  }));
  expect(overflow.doc).toBeLessThanOrEqual(0);
  expect(overflow.body).toBeLessThanOrEqual(0);
});

test("#805: 偽のデータで、取り込めるものと待っているものと「サイクルを回す」が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/now");
  await expect(page.getByTestId("ingestable-list")).toContainText(
    "取り込める例",
  );
  await expect(page.getByTestId("waiting-list")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "サイクルを回す" }),
  ).toBeVisible();
});
