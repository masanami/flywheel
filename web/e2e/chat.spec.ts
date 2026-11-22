import { expect, test } from "@playwright/test";

// 対話の欄の通し（偽の API＝テスト用の実行エンジン）。実物の実行エンジンでの確かめは、
// オーケストレーターが試し用のワークスペースで行う（受入基準 AC-185〜AC-191）。

test("AC-193: 文を送ると、自分の文と返事が欄に出て、実行したコマンドも見える", async ({
  page,
}) => {
  await page.goto("/");
  const panel = page.getByTestId("chat-panel");
  await expect(panel).toBeVisible();

  await panel
    .getByRole("textbox", { name: "オーケストレーターへの文" })
    .fill("いま進められることは？");
  await panel.getByRole("button", { name: "送る" }).click();

  await expect(panel.getByTestId("chat-message-owner")).toHaveText(
    "いま進められることは？",
  );
  const reply = panel.getByTestId("chat-message-orchestrator");
  await expect(reply).toContainText("確認しました");
  await expect(reply.getByTestId("chat-activity")).toContainText(
    "flywheel status",
  );
  // 返事が終わると、入力欄は空に戻る
  await expect(
    panel.getByRole("textbox", { name: "オーケストレーターへの文" }),
  ).toHaveValue("");
});
