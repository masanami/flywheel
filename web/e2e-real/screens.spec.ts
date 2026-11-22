import { expect, test } from "@playwright/test";
import { BASE_URL_ENV } from "./global-setup";

// 本物の `flywheel serve` と種のストア（Go のテスト TestE2ESeed）で、画面が出そろった後の筋書きを確かめる。#813
// 種: C-1 計画承認待ち（タスク 2 件・問い 1 件）・C-2 着手中（タスク 3 件）・C-3 人間対応待ち・C-4 完了、
// F-1 未・F-2 済み・F-3 捨てた（フォローアップ）。種の題名は internal/core/e2e_seed_test.go と同じにする。
// 試験は 1 つの serve と 1 つの種を共有する。計画の承認の試験だけが種を書き換えるので、最後に置く。
test.use({ baseURL: process.env[BASE_URL_ENV] });

const AGENT = "seed";

test("AC-440 #813: 本物の serve で、課題の画面に、種にした課題のタスクのツリーが出る", async ({
  page,
}) => {
  await page.goto(`/#/a/${AGENT}/challenges`);
  await expect(
    page.getByRole("heading", { name: "課題", exact: true }),
  ).toBeVisible();
  const frame = page
    .getByTestId("challenge-frame")
    .filter({ hasText: "着手中の課題" });
  await expect(frame).toBeVisible();
  const tree = frame.getByTestId("task-tree");
  await expect(tree).toBeVisible();
  // 着手中の課題のタスク 3 件
  for (const key of ["タスク a", "タスク b", "タスク c"]) {
    await expect(tree.getByText(key, { exact: false }).first()).toBeVisible();
  }
});

test("AC-441 #813: 本物の serve で、フォローアップの一覧に、種にしたフォローアップの題名が出る", async ({
  page,
}) => {
  await page.goto(`/#/a/${AGENT}/followups`);
  await expect(
    page.getByRole("heading", { name: "フォローアップ" }),
  ).toBeVisible();
  await expect(
    page.getByTestId("followup-row").filter({ hasText: "未のフォローアップ" }),
  ).toBeVisible();
  // 済みへ切り替えると、済みの題名が出る
  await page.getByRole("button", { name: /^済み/ }).click();
  await expect(
    page
      .getByTestId("followup-row")
      .filter({ hasText: "済みのフォローアップ" }),
  ).toBeVisible();
  // 捨てたへ切り替えると、捨てた題名が出る
  await page.getByRole("button", { name: /^捨てた/ }).click();
  await expect(
    page
      .getByTestId("followup-row")
      .filter({ hasText: "捨てたフォローアップ" }),
  ).toBeVisible();
});

test("AC-442 #813: 本物の serve で、「いま進められること」に、種にした計画の承認待ちの課題の題名が出る", async ({
  page,
}) => {
  await page.goto(`/#/a/${AGENT}/now`);
  await expect(
    page.getByRole("heading", { name: "いま進められること" }),
  ).toBeVisible();
  await expect(
    page
      .locator('[data-kind="plan_approval"]')
      .filter({ hasText: "計画承認待ちの課題" }),
  ).toBeVisible();
});

// 種を書き換える（C-1 が着手中になる）ので、最後に置く
test("#1055: 本物の serve で、対話の欄の「新しい会話」が 204（本文なし）で成功し、欄にエラーが出ない", async ({
  page,
}) => {
  await page.goto(`/#/a/${AGENT}/challenges`);
  const panel = page.getByTestId("chat-panel");
  await expect(panel).toBeVisible();
  const done = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/v1/workspaces/${AGENT}/chat/new`) &&
      r.request().method() === "POST",
  );
  await panel.getByRole("button", { name: "新しい会話" }).click();
  expect((await done).status()).toBe(204);
  await expect(panel.getByTestId("chat-error")).toHaveCount(0);
});

test("AC-352 #813: 本物の serve で、計画の承認の画面の「この計画を承認する」を押すと、課題が着手中（in_progress）になる", async ({
  page,
  request,
}) => {
  const base = process.env[BASE_URL_ENV];
  const stateOf = async () => {
    const res = await request.get(
      `${base}/api/v1/workspaces/${AGENT}/challenges/C-1`,
    );
    expect(res.status()).toBe(200);
    return ((await res.json()) as { challenge: { status: string } }).challenge
      .status;
  };
  expect(await stateOf()).toBe("awaiting_plan_approval");

  await page.goto(`/#/a/${AGENT}/challenges/C-1/approve-plan`);
  await expect(
    page.getByRole("heading", { name: /^計画の承認: / }),
  ).toBeVisible();
  await page.getByRole("button", { name: "この計画を承認する" }).click();
  await expect(page.getByText("承認しました。")).toBeVisible();
  expect(await stateOf()).toBe("in_progress");
});
