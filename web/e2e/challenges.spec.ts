import { expect, test } from "@playwright/test";

// 課題の画面の横スクロールを、偽のデータ（mock モード。alpha の C-28 は 8 列のツリー）で確かめる。#807

const pageOverflows = (page: import("@playwright/test").Page) =>
  page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );

async function openChallenges(page: import("@playwright/test").Page) {
  await page.goto("/#/a/alpha/challenges");
  await expect(page.getByRole("heading", { name: "課題" })).toBeVisible();
  await expect(page.getByTestId("challenge-frame").first()).toBeVisible();
}

test("AC-221 #807: 列が画面の幅より多いツリーがあっても、ページ全体は横にスクロールしない", async ({
  page,
}) => {
  await openChallenges(page);
  const wide = page
    .getByTestId("challenge-frame")
    .filter({ hasText: "請求データの夜間集計" })
    .getByTestId("task-tree");
  // ツリーは画面より広い（枠の中で切れている）
  const sizes = await wide.evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
  }));
  expect(sizes.scroll).toBeGreaterThan(sizes.client);
  expect(await pageOverflows(page)).toBe(false);
});

test("AC-222 #807: 列が画面の幅より多いツリーは、枠の中で横にスクロールできる", async ({
  page,
}) => {
  await openChallenges(page);
  const wide = page
    .getByTestId("challenge-frame")
    .filter({ hasText: "請求データの夜間集計" })
    .getByTestId("task-tree");
  expect(await wide.evaluate((el) => el.scrollLeft)).toBe(0);
  await wide.evaluate((el) => {
    el.scrollLeft = 400;
  });
  expect(await wide.evaluate((el) => el.scrollLeft)).toBeGreaterThan(0);
  expect(await pageOverflows(page)).toBe(false);
});

test("AC-430 #807: 幅 360px で、課題の画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await openChallenges(page);
  expect(await pageOverflows(page)).toBe(false);
  // 8 列のツリーは、360px の枠の中で横にスクロールする
  const wide = page
    .getByTestId("challenge-frame")
    .filter({ hasText: "請求データの夜間集計" })
    .getByTestId("task-tree");
  const sizes = await wide.evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
  }));
  expect(sizes.scroll).toBeGreaterThan(sizes.client);
  expect(sizes.client).toBeLessThanOrEqual(360);
});
