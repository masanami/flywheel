import { expect, test } from "@playwright/test";

// 完了承認画面を、偽のデータ（mock モード。alpha の C-7 は完了の承認待ち）で確かめる。

test("AC-34 #930: 「いま進められること」の完了の承認の行から完了承認画面を開き、「完了を承認する」を押すと、承認を記録した旨が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/now");
  const row = page.locator('[data-kind="completion_approval"]').first();
  await expect(row).toBeVisible();
  await row.getByRole("link").click();
  await expect(page).toHaveURL(
    /#\/a\/alpha\/challenges\/[^/]+\/approve-completion$/,
  );
  await expect(
    page.getByRole("heading", { level: 2, name: /^完了の承認: / }),
  ).toBeVisible();
  await expect(page.getByTestId("approve-completion")).toBeVisible();
  await page.getByRole("button", { name: "完了を承認する" }).click();
  await expect(page.getByRole("status")).toContainText(
    "完了の承認を記録しました。反映先へ入れる承認は、Claude Code との対話で行います。",
  );
});

test("AC-27・AC-29・AC-30 #930: 計画の承認待ちの課題の完了承認画面には、ボタンが出ず、完了の承認待ちでない旨と課題の詳細へのリンクが出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-3/approve-completion");
  await expect(page.getByTestId("completion-not-awaiting")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "完了を承認する" }),
  ).toHaveCount(0);
  await expect(
    page.getByText("この課題は、完了の承認待ちではありません。"),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "課題の詳細を開く" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-3");
});

test("AC-18・AC-22・AC-23・AC-31・AC-32 #930: 完了承認画面に、差し戻しのボタンとコマンドの例は無く、対話での案内が出る", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-7/approve-completion");
  await expect(page.getByTestId("approve-completion")).toBeVisible();
  await expect(page.getByRole("button")).toHaveCount(1);
  await expect(page.getByTestId("completion-release-note")).toBeVisible();
  await expect(page.getByTestId("completion-reject-note")).toBeVisible();
  await expect(page.getByTestId("approve-completion")).not.toContainText(
    /flywheel /,
  );
});

test("#930: 幅 360px で、完了承認画面は、ページ全体が横にスクロールしない", async ({
  page,
}) => {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto("/#/a/alpha/challenges/C-7/approve-completion");
  await expect(page.getByTestId("approve-completion")).toBeVisible();
  const overflows = await page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );
  expect(overflows).toBe(false);
});

test("#970: 完了承認画面は承認の要約 1 つだけを読み、課題の詳細は読まない。要約が返したタスク・通したコンフリクトの解消が出て、「課題が更新されました」の画面は出ない", async ({
  page,
}) => {
  const requests: string[] = [];
  page.on("request", (r) => requests.push(new URL(r.url()).pathname));
  await page.goto("/#/a/alpha/challenges/C-7/approve-completion");
  await expect(page.getByTestId("approve-completion")).toBeVisible();
  await expect(page.getByTestId("completion-tasks")).toBeVisible();
  await expect(page.getByTestId("completion-followups")).toBeVisible();
  await expect(page.getByTestId("conflict-fixes")).toBeVisible();
  await expect(page.getByText("課題が更新されました")).toHaveCount(0);
  await expect(page.getByTestId("completion-incoherent")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "完了を承認する" }),
  ).toBeEnabled();
  // mock モードはブラウザ内の偽の応答なので、ネットワークの要求の検査は、要求が出ていないことだけを確かめる。
  expect(requests.filter((p) => p.endsWith("/challenges/C-7"))).toHaveLength(0);
});
