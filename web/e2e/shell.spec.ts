import { expect, test } from "@playwright/test";

// 画面の枠（左の列・上の帯・ハッシュでの行き来）を、偽のデータ（mock モード。エージェント 3 つ）で確かめる。#803

test("AC-424 #803: 偽のデータで開くと、左の列にエージェントが 3 つ出る", async ({
  page,
}) => {
  await page.goto("/");
  const rail = page.getByRole("navigation", { name: "エージェント" });
  await expect(rail.getByRole("link", { name: /alpha/ })).toBeVisible();
  // 「すべて」を除くと 3 つ
  await expect(rail.locator(".agent-name")).toHaveText([
    "すべて",
    "alpha",
    "beta",
    "gamma",
  ]);
});

test("AC-425 #803: 偽のデータで開くと、最初に「いま進められること」が出る", async ({
  page,
}) => {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "いま進められること" }),
  ).toBeVisible();
});

test("#803: 上の帯の「課題」を押すとハッシュが変わり、戻るで前の画面に戻る", async ({
  page,
}) => {
  await page.goto("/");
  await page
    .getByRole("navigation", { name: "画面" })
    .getByRole("link", { name: "課題" })
    .click();
  await expect(page).toHaveURL(/#\/a\/alpha\/challenges$/);
  await expect(page.getByRole("heading", { name: "課題" })).toBeVisible();
  await page.goBack();
  await expect(
    page.getByRole("heading", { name: "いま進められること" }),
  ).toBeVisible();
});

test("#803: 無いエージェントを指す URL は「見つかりません」と「いま進められること」へのリンクが出る", async ({
  page,
}) => {
  await page.goto("/#/a/nobody/now");
  await expect(page.getByText("見つかりません。")).toBeVisible();
  await page.getByRole("link", { name: "「いま進められること」へ" }).click();
  await expect(
    page.getByRole("heading", { name: "いま進められること" }),
  ).toBeVisible();
});

test("#803: 読めないエージェントを選ぶと、error の文が本文に出る", async ({
  page,
}) => {
  await page.goto("/");
  await page
    .getByRole("navigation", { name: "エージェント" })
    .getByRole("link", { name: /gamma/ })
    .click();
  await expect(page.getByTestId("agent-error")).toHaveText(
    "ストアが見つかりません",
  );
});
