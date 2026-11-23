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

test("AC-157 #805: 偽のデータで「読み直す」を押すと「読んでいます」を経て、取り込めるものが再び出る", async ({
  page,
}) => {
  // 偽の API はブラウザの中にあり通信が出ないので、要求の回数は Vitest（AC-156・AC-157）で確かめ、
  // ここでは押した後も画面が読み直しを終えて元の一覧に戻ることを確かめる。
  await page.goto("/#/a/alpha/now");
  await expect(page.getByTestId("ingestable-list")).toBeVisible();
  await page.getByRole("button", { name: "読み直す" }).click();
  await expect(page.getByTestId("ingestable-list")).toBeVisible();
  await expect(page.getByTestId("ingest-button")).toBeDisabled();
});

test("AC-125 #805: 偽のデータで、計画の承認の行のリンクを押すと計画の承認の画面へ移る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/now");
  const row = page.locator('[data-kind="plan_approval"]').first();
  await expect(row).toBeVisible();
  await row.getByRole("link").click();
  await expect(page).toHaveURL(/#\/a\/alpha\/challenges\/[^/]+\/approve-plan$/);
  await expect(page.getByRole("heading", { name: "計画の承認" })).toBeVisible();
});

test("AC-144 #805: 偽のデータで、承認の種類の印は data-kind-group=approval を持ち、回答の印は持たない", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/now");
  const plan = page.locator('[data-kind="plan_approval"] .now-kind').first();
  await expect(plan).toHaveAttribute("data-kind-group", "approval");
  const answer = page.locator('[data-kind="answer"] .now-kind').first();
  await expect(answer).toBeVisible();
  await expect(answer).not.toHaveAttribute("data-kind-group", /.*/);
});
