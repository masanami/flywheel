import { expect, test } from "@playwright/test";

// 下書きづくりの問いの保留（#1357）。偽のデータ（mock モード）の beta の C-4 の保留 H-1 を使う。

test("AC-374〜AC-380・AC-384・AC-386・AC-389 #1357: 課題の詳細の保留の所に、下書きの問い・推奨と理由・下書きの場所・案内・選択肢のボタンが出て、推奨のまま進められる", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-4");
  const frame = page.getByTestId("hold-question");
  await expect(frame).toHaveCount(1);
  await expect(frame).toContainText("下書き: .flywheel/specs/c-4.md");
  const questions = frame.getByTestId("spec-questions");
  await expect(questions).toContainText(
    "取り消しは、実行中の処理にも効かせますか",
  );
  await expect(questions.getByRole("listitem").first()).toContainText(
    "（推奨）",
  );
  await expect(questions).toContainText(
    "推奨の理由: 課題の説明が、すぐ止めたいと書いているため",
  );
  await expect(questions.locator("input, select, textarea")).toHaveCount(0);
  await expect(frame.getByTestId("spec-answer-guide")).toContainText(
    "--option answer --choice <問いの番号>=<選択肢の番号>",
  );
  await expect(frame.getByTestId("hold-option-button")).toHaveText([
    "推奨のまま進める",
    "いまの下書きで計画へ進む",
    "仕様の文書なしで計画する",
  ]);
  await frame.getByRole("button", { name: "推奨のまま進める" }).click();
  await page.getByTestId("hold-confirm-send").click();
  await expect(page.getByTestId("hold-question")).toHaveCount(0);
});
