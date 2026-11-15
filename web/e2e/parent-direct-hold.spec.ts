import { expect, test } from "@playwright/test";

// flywheel#502: 種類が「親が直接行う」の保留は、カードの詳細で「未回答」ではなく親が直接行う旨を出す。
// 偽のデータ（`?mock=parent-direct`）: 既定の種の C-8（人間対応待ち）の開いている保留がその種類。
test.describe("親が直接行う保留（mock）", () => {
  test.use({ viewport: { width: 1280, height: 900 } });

  test("カードの詳細に、オーナーが答える問いではない旨が出て「未回答」とは出ない", async ({
    page,
  }) => {
    await page.goto("/?mock=parent-direct");
    await page.getByRole("button", { name: "ボード", exact: true }).click();
    await page.getByTestId("card-C-8").click();
    await expect(
      page.getByText("オーナーが答える問いではありません"),
    ).toBeVisible();
    await expect(page.getByText("回答: 未回答")).toHaveCount(0);
  });
});
