import { type Page, expect, test } from "@playwright/test";

// タスクの詳細の画面を、偽のデータ（mock モード。alpha の C-28 は 8 列のツリーで、T-43 は実行・問いと回答を持つ）で確かめる。#810

const pageOverflows = (page: Page) =>
  page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );

const C28 = "請求データの夜間集計を新しい基盤へ載せ替える";

test("AC-426 #810: 課題の画面から課題の詳細、タスクの詳細の順に移れる", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges");
  await expect(page.getByTestId("challenge-frame").first()).toBeVisible();
  await page.getByRole("link", { name: C28 }).click();
  await expect(page.getByTestId("challenge-detail")).toBeVisible();
  await page
    .getByTestId("task-box")
    .filter({ hasText: "手順 4 を進める" })
    .click();
  await expect(page.getByTestId("task-detail")).toBeVisible();
  expect(page.url()).toContain("#/a/alpha/challenges/C-28/tasks/T-43");
  await expect(
    page.getByRole("heading", { name: "手順 4 を進める" }),
  ).toBeVisible();
  await expect(page.getByTestId("task-holds")).toBeVisible();
  await expect(page.getByTestId("task-run")).toHaveCount(2);
});

test("AC-432 #810: 幅 360px で、タスクの詳細の画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  for (const t of ["T-43", "T-44"]) {
    await page.goto(`/#/a/alpha/challenges/C-28/tasks/${t}`);
    await expect(page.getByTestId("task-detail")).toBeVisible();
    await expect(page.getByTestId("task-runs")).toBeVisible();
    expect(await pageOverflows(page)).toBe(false);
  }
  // 答え待ちのタスクには、問いと端末のコマンドの案内が出て、ボタンは無い
  await expect(page.getByTestId("task-waiting")).toBeVisible();
  await expect(page.getByTestId("task-detail").getByRole("button")).toHaveCount(
    0,
  );
});

test("AC-108 #810: その課題に無いタスクを指す URL では、「見つかりません」と「いま進められること」へのリンクが出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-28/tasks/T-999");
  await expect(page.getByRole("alert")).toContainText("見つかりません");
  await expect(
    page.getByRole("link", { name: "「いま進められること」へ" }),
  ).toBeVisible();
});

test("AC-295〜309 #810: 偽のデータで、問いと回答・実行の記録（新しい順・報告の仮定と確かめていないこと）が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-28/tasks/T-43");
  const holds = page.getByTestId("task-hold");
  await expect(holds).toHaveCount(1);
  await expect(page.getByTestId("task-hold-quote")).toContainText(
    "旧基盤とそろえてほしい",
  );
  await expect(page.getByTestId("task-hold-replan")).toBeVisible();
  const runs = page.getByTestId("task-run");
  await expect(runs).toHaveCount(2);
  await expect(runs.first()).toHaveAttribute("data-run-id", "R-16");
  await expect(runs.first().getByTestId("run-ended")).toHaveCount(0);
  await expect(runs.nth(1).getByTestId("run-ended")).toBeVisible();
  await expect(page.getByTestId("run-assumptions")).toBeVisible();
  await expect(page.getByTestId("run-unverified")).toBeVisible();
});

test("AC-310 #810: 偽のデータで、取り込み元の子の題名リンクと上流の状態が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-28/tasks/T-40");
  const src = page.getByTestId("task-source");
  await expect(src.getByRole("link")).toBeVisible();
  await expect(page.getByTestId("task-upstream")).toBeVisible();
});
