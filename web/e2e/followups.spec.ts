import { expect, test } from "@playwright/test";

// フォローアップの画面を、偽のデータ（mock モード。alpha は未 3・済み 2・捨てた 1）で確かめる。#812

const pageOverflows = (page: import("@playwright/test").Page) =>
  page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );

test("AC-428 #812: 偽のデータで「済み」を押すと、済みのフォローアップが出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/followups");
  await expect(
    page.getByRole("heading", { name: "フォローアップ" }),
  ).toBeVisible();
  await expect(page.getByTestId("followup-row")).toHaveCount(3);
  await page.getByRole("button", { name: /^済み/ }).click();
  await expect(page.getByTestId("followup-row")).toHaveCount(2);
  await expect(page.getByTestId("followup-note").first()).toBeVisible();
  await expect(page).toHaveURL(/#\/a\/alpha\/followups\?status=done$/);
});

test("AC-434 #812: 幅 360px で、フォローアップの画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/#/a/alpha/followups");
  await expect(page.getByTestId("followup-guide")).toBeVisible();
  expect(await pageOverflows(page)).toBe(false);
  await page.getByRole("button", { name: /^済み/ }).click();
  await expect(page.getByTestId("followup-row").first()).toBeVisible();
  expect(await pageOverflows(page)).toBe(false);
});
