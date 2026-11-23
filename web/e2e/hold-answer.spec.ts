import { expect, test } from "@playwright/test";

// 区切り S4d-2 の T5: 保留の選択肢を画面のボタンで選んで答える。偽のデータ（mock モード）の C-8 の保留 H-1 を使う。

test("AC-95・AC-96・AC-98 S4d-2: 課題の詳細の待ちに、選べる選択肢のボタンと、選べない選択肢の案内が出る。確かめてから答えると枠が消える", async ({
  page,
}) => {
  await page.goto("/#/a/alpha/challenges/C-8");
  const frame = page.getByTestId("hold-question");
  await expect(frame).toHaveCount(1);
  await expect(frame.getByTestId("hold-id")).toContainText("H-1");
  await expect(frame.getByTestId("hold-option-button")).toHaveCount(2);
  await expect(frame.getByTestId("hold-option-conversation")).toContainText(
    "対話で伝えてください",
  );
  await frame.getByRole("button", { name: "含めない" }).click();
  await expect(page.getByTestId("hold-confirm")).toContainText("含めない");
  // 確かめる前は、枠が残っている
  await expect(frame).toHaveCount(1);
  await page.getByTestId("hold-confirm-send").click();
  await expect(page.getByTestId("hold-question")).toHaveCount(0);
});

test("AC-98 S4d-2: 確かめをやめると、答えない", async ({ page }) => {
  await page.goto("/#/a/alpha/challenges/C-8");
  await page.getByRole("button", { name: "含める", exact: true }).click();
  await page.getByTestId("hold-confirm-cancel").click();
  await expect(page.getByTestId("hold-confirm")).toHaveCount(0);
  await expect(page.getByTestId("hold-question")).toHaveCount(1);
});
