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
