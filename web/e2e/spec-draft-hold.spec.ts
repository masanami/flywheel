import { expect, test } from "@playwright/test";

// 下書きづくりの保留（#1357）。保留の所は .detail-question（hold-question は、その中の保留の問いの文の要素）。
//偽のデータ（mock モード）の beta の C-4（問い）・C-5（失敗）・C-6（回数の上限）を使う。

test("AC-374〜AC-380・AC-384・AC-386・AC-389 #1357: 課題の詳細の保留の所に、下書きの問い・推奨と理由・下書きの場所・案内・選択肢のボタンが出て、推奨のまま進められる", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-4");
  const frame = page.locator(".detail-question");
  await expect(frame).toHaveCount(1);
  await expect(frame).toContainText("下書き: .flywheel/specs/c-4.md");
  const questions = frame.getByTestId("spec-questions");
  await expect(questions).toContainText(
    "取り消しは、実行中の処理にも効かせますか",
  );
  await expect(questions.getByRole("listitem")).toHaveText([
    "実行中にも効かせる（推奨）",
    "待ちの処理だけ",
  ]);
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
  await expect(page.locator(".detail-question")).toHaveCount(0);
});

test("AC-379・AC-381・AC-387 #1357: 失敗の保留では、下書きの場所と retry・proceed・skip のボタンが出て、問いの一覧と案内は出ない", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-5");
  const frame = page.locator(".detail-question");
  await expect(frame).toContainText("下書き: .flywheel/specs/c-5.md");
  await expect(frame.getByTestId("hold-option-button")).toHaveText([
    "やり直す",
    "いまの下書きで計画へ進む",
    "仕様の文書なしで計画する",
  ]);
  await expect(page.getByTestId("spec-questions")).toHaveCount(0);
  await expect(page.getByTestId("spec-answer-guide")).toHaveCount(0);
});

test("AC-379・AC-388・AC-389 #1357: 回数の上限の保留では proceed・skip のボタンが出て、下書きの問いと場所は、その保留の所にだけ出る", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-6");
  const frames = page.locator(".detail-question");
  await expect(frames).toHaveCount(2);
  const other = frames.filter({ hasText: "通知の送り先はどこにしますか。" });
  const spec = frames.filter({ hasText: "回数の上限に達しました" });
  await expect(spec).toContainText("下書き: .flywheel/specs/c-6.md");
  await expect(spec.getByTestId("spec-questions")).toHaveCount(1);
  await expect(spec.getByTestId("hold-option-button")).toHaveText([
    "いまの下書きで計画へ進む",
    "仕様の文書なしで計画する",
  ]);
  await expect(other.getByTestId("spec-questions")).toHaveCount(0);
  await expect(other).not.toContainText(".flywheel/specs/");
  await expect(page.getByTestId("spec-answer-guide")).toHaveCount(0);
});

test("AC-298〜AC-300・AC-308・AC-314・AC-317〜AC-321・AC-335・AC-338 #1383: 保留の所に、スーパーバイザーが答えた分が出て、「仕様の文書を開く」で下書きを開いて畳める", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-4");
  const frame = page.locator(".detail-question");
  const answered = frame.getByTestId("spec-sv-answers");
  await expect(answered).toContainText("取り消した記録は、どの表に残しますか");
  await expect(answered).toContainText("いまの作業ログに残す");
  await expect(answered).toContainText("docs/architecture.md（作業ログの節）");
  await expect(frame.getByTestId("hold-question")).not.toContainText(
    "取り消しは、実行中の処理にも効かせますか",
  );
  await expect(frame.getByTestId("spec-questions")).toContainText(
    "取り消しは、実行中の処理にも効かせますか",
  );
  await expect(page.getByTestId("spec-open")).toHaveCount(1);
  await expect(page.getByTestId("spec-body")).toHaveCount(0);
  await frame.getByTestId("spec-open").click();
  await expect(
    frame
      .getByTestId("spec-body")
      .getByRole("heading", { name: "予約の取り消しの仕様" }),
  ).toBeVisible();
  await frame.getByTestId("spec-open").click();
  await expect(page.getByTestId("spec-body")).toHaveCount(0);
});

test("AC-322・AC-327 #1383: 下書きのファイルがまだ無いときは、訳の文が出て、本文は出ない", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-5");
  await page.getByTestId("spec-open").click();
  await expect(page.getByTestId("spec-unreadable")).toHaveText(
    "ファイルがまだありません",
  );
  await expect(page.getByTestId("spec-body")).toHaveCount(0);
});
