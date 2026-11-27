import { expect, test } from "@playwright/test";

// 課題の詳細の、区画「要求」と区切りを分ける保留の所（#1407）。
// 偽のデータ（mock モード）の beta の C-7（区切りを分ける保留）・C-8（要求を指す解決した課題）を使う。

test("AC-228〜AC-236 #1407: 区切りを分ける保留の所に、案の区切り・理由・証跡の場所と 2 つのボタンが出て、「この案で分ける」で答えられる", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-7");
  const frame = page.locator(".detail-question");
  const proposal = frame.getByTestId("slice-split-proposal");
  const slices = proposal.getByTestId("slice-split-slice");
  await expect(slices).toHaveCount(2);
  await expect(slices.locator("b")).toHaveText([
    "1. 通知の設定を保存する",
    "2. 通知をまとめて送る",
  ]);
  await expect(slices.nth(0)).toContainText(
    "要旨: 通知の設定を保存するための区切り",
  );
  await expect(slices.nth(0)).toContainText("リポジトリ: app");
  await expect(slices.nth(1)).toContainText("前に済ませる区切り: 1");
  await expect(proposal).toContainText(
    "分ける理由: 設定と送信は、それぞれ単独で出荷できる",
  );
  await expect(proposal).toContainText(
    "証跡の場所: .flywheel/requirements/split-C-7.md",
  );
  await expect(frame.getByTestId("hold-option-button")).toHaveText([
    "この案で分ける",
    "分けずに 1 つの課題で進める",
  ]);
  await expect(page.getByTestId("requirement-section")).toHaveCount(0); // AC-227
  await frame.getByRole("button", { name: "この案で分ける" }).click();
  await page.getByTestId("hold-confirm-send").click();
  await expect(page.locator(".detail-question")).toHaveCount(0);
});

test("AC-224・AC-225 #1407: 区画「要求」に元の Issue のリンクと同じ要求の課題が出て、課題の詳細へ移れる", async ({
  page,
}) => {
  await page.goto("/#/a/beta/challenges/C-8");
  const section = page.getByTestId("requirement-section");
  const url = "https://github.com/example/app/issues/41";
  await expect(section.getByRole("link", { name: url })).toHaveAttribute(
    "href",
    url,
  );
  const rows = section.getByTestId("requirement-challenge");
  await expect(rows.locator(".status-pill")).toHaveText([
    "人間対応待ち",
    "完了",
  ]);
  await rows.getByRole("link", { name: "予約の取り消しを足す" }).click();
  await expect(page).toHaveURL(/#\/a\/beta\/challenges\/C-4$/);
  await expect(page.getByTestId("detail-title")).toHaveText(
    "予約の取り消しを足す",
  );
});
