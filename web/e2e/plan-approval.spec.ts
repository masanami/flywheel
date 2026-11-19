import { expect, test } from "@playwright/test";

// 計画の承認の画面を、偽のデータ（mock モード。alpha の C-3 は計画の承認待ち）で確かめる。#811

const pageOverflows = (page: import("@playwright/test").Page) =>
  page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );

async function openPlan(page: import("@playwright/test").Page) {
  await page.goto("/#/a/alpha/challenges/C-3/approve-plan");
  await expect(
    page.getByRole("heading", { name: /^計画の承認: / }),
  ).toBeVisible();
  await expect(page.getByTestId("plan-approval")).toBeVisible();
}

test("AC-427 #811: 偽のデータで「この計画を承認する」を押すと、「承認しました。」が出る", async ({
  page,
}) => {
  await openPlan(page);
  await page.getByRole("button", { name: "この計画を承認する" }).click();
  await expect(page.getByText("承認しました。")).toBeVisible();
  await expect(
    page.getByText("続きは、次のサイクルで進みます。"),
  ).toBeVisible();
});

test("AC-433 #811: 幅 360px で、計画の承認の画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await openPlan(page);
  await expect(page.getByTestId("task-box").first()).toBeVisible();
  expect(await pageOverflows(page)).toBe(false);
});
