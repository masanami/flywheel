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
  await expect(page.getByText("サイクルを始めました。")).toBeVisible();
});

test("#798: 広い画面（幅 1280px）では、「問いと推奨」が、方針の右に並ぶ", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await openPlan(page);
  const main = await page.getByTestId("plan-main").boundingBox();
  const side = await page.getByTestId("plan-topics").boundingBox();
  if (!main || !side) throw new Error("区画の位置を読めない");
  expect(side.x).toBeGreaterThanOrEqual(main.x + main.width);
  // 上の端がそろう（右の区画が、左の区画の下に回っていない）
  expect(Math.abs(side.y - main.y)).toBeLessThan(2);
});

test("#798: 狭い画面（幅 360px）では、「問いと推奨」が、計画の中身の下に並ぶ", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await openPlan(page);
  const main = await page.getByTestId("plan-main").boundingBox();
  const side = await page.getByTestId("plan-topics").boundingBox();
  if (!main || !side) throw new Error("区画の位置を読めない");
  expect(side.y).toBeGreaterThanOrEqual(main.y + main.height);
});

test("AC-433 #811: 幅 360px で、計画の承認の画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await openPlan(page);
  await expect(page.getByTestId("task-box").first()).toBeVisible();
  expect(await pageOverflows(page)).toBe(false);
});

test("AC-189・AC-190・AC-192 #1046: 計画の承認に見込みと上限の額が出て、上限が見込みと同じなら上限の理由の行は出ない", async ({
  page,
}) => {
  await openPlan(page);
  await expect(page.getByText("見込み", { exact: true })).toBeVisible();
  await expect(page.getByTestId("plan-budget")).toContainText("12");
  await expect(page.getByText("上限", { exact: true })).toBeVisible();
  await expect(page.getByTestId("plan-budget-limit")).toContainText("12");
  await expect(page.getByText("上限の理由")).toHaveCount(0);
});

test("AC-339・AC-315・AC-328・AC-332 #1383: 下書きを持つ計画の承認待ちの課題で「仕様の文書を開く」を押すと、下書きの見出しが出る。幅 360px でも横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/#/a/alpha/challenges/C-4/approve-plan");
  await expect(page.getByTestId("plan-approval")).toBeVisible();
  const answers = page.getByTestId("spec-answers");
  await expect(answers).toContainText("答えた者: スーパーバイザー");
  await expect(answers).toContainText("添え書き");
  await expect(page.getByTestId("spec-body")).toHaveCount(0);
  await page.getByRole("button", { name: "仕様の文書を開く" }).click();
  await expect(
    page.getByRole("heading", { name: "権限チェックの仕様" }),
  ).toBeVisible();
  expect(await pageOverflows(page)).toBe(false);
});
