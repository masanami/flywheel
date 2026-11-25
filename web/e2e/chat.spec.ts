import { type Page, expect, test } from "@playwright/test";

// 対話の欄の通し（偽の API＝テスト用の実行エンジン）。実物の実行エンジンでの確かめは、
// オーケストレーターが試し用のワークスペースで行う（受入基準 AC-185〜AC-191）。

test("AC-193: 文を送ると、自分の文と返事が欄に出て、使った道具も見える", async ({
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
  const activity = reply.getByTestId("chat-activity");
  await activity.getByText("使った道具（1 件）").click();
  await expect(activity.getByRole("listitem")).toBeVisible();
  await expect(activity).toContainText("flywheel status");
  await expect(activity).not.toContainText("実行中");
  // 返事が終わると、入力欄は空に戻る
  await expect(
    panel.getByRole("textbox", { name: "オーケストレーターへの文" }),
  ).toHaveValue("");
});

const send = async (page: Page, text: string) => {
  const panel = page.getByTestId("chat-panel");
  await panel
    .getByRole("textbox", { name: "オーケストレーターへの文" })
    .fill(text);
  await panel.getByRole("button", { name: "送る" }).click();
  return panel;
};

// 対話の欄の口だけを本物の HTTP の呼び出しにして（?mock=chat-http）、画面が送る要求をブラウザの側で受ける。
test("許可の確認で「許可する」を押すと、確認への答えの口へ decision: allow が送られる", async ({
  page,
}) => {
  const command = `printf '%s' ${"abcdefghij".repeat(60)} && echo 末尾の印`;
  let state = "pending";
  const posted: { path: string; body: unknown; csrf: string | undefined }[] =
    [];
  await page.route("**/api/v1/workspaces/alpha/chat**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (req.method() === "POST") {
      posted.push({
        path,
        body: req.postDataJSON(),
        csrf: req.headers()["x-flywheel-csrf"],
      });
      state = "allowed";
      await route.fulfill({ status: 204 });
      return;
    }
    const at = "2026-11-25T00:00:00.000Z";
    await route.fulfill({
      json: {
        cost_usd: 0,
        session_id: "s-1",
        started_at: at,
        state: state === "pending" ? "replying" : "idle",
        tools: "restricted",
        messages: [
          {
            activity: [
              { id: "t1", tool: "Bash", target: "printf", state: "running" },
            ],
            body: "実行してよいですか。",
            cost_usd: 0,
            created_at: at,
            id: 2,
            role: "orchestrator",
            state: state === "pending" ? "replying" : "done",
            viewing: null,
            prompts: [
              {
                id: 41,
                kind: "permission",
                state,
                tool: "Bash",
                target: command,
                questions: [],
                answer:
                  state === "pending"
                    ? null
                    : { decision: "allow", message: null, answers: null },
                created_at: at,
                answered_at: state === "pending" ? null : at,
              },
            ],
          },
        ],
      },
    });
  });
  await page.goto("/?mock=chat-http");
  const panel = page.getByTestId("chat-panel");
  const prompt = panel.getByTestId("chat-prompt");
  await expect(prompt.getByTestId("chat-prompt-target")).toHaveText(command);
  // 対象は切らずに出る（枠の中で折り返し、はみ出して隠れない）
  const clipped = await prompt
    .getByTestId("chat-prompt-target")
    .evaluate(
      (el) =>
        el.scrollWidth > el.clientWidth || el.scrollHeight > el.clientHeight,
    );
  expect(clipped).toBe(false);
  await expect(panel.getByRole("button", { name: "止める" })).toBeEnabled();
  await prompt.getByRole("button", { name: "許可する" }).click();

  await expect(prompt).toContainText("許可しました");
  await expect(prompt.getByRole("button")).toHaveCount(0);
  expect(posted).toEqual([
    {
      path: "/api/v1/workspaces/alpha/chat/prompts/41/answer",
      body: { decision: "allow" },
      csrf: expect.any(String),
    },
  ]);
});

test("問いに選択肢のボタンで答えると、答えが欄に出て返事が終わる", async ({
  page,
}) => {
  await page.goto("/");
  const panel = await send(page, "選択肢の問い を出して");
  const prompt = panel.getByTestId("chat-prompt");
  await expect(panel.getByRole("button", { name: "止める" })).toBeEnabled();
  await prompt.getByRole("button", { name: "再送だけ" }).click();
  await expect(prompt).toContainText("回答: 再送だけ");
  await expect(prompt.getByRole("button")).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "止める" })).toHaveCount(0);
});

test("会話の一覧で、閉じた会話の全文を開き、続けられる。全文が無い会話は続けられない", async ({
  page,
}) => {
  await page.goto("/");
  const panel = page.getByTestId("chat-panel");
  await panel.getByRole("button", { name: "会話の一覧" }).click();
  const rows = panel.getByTestId("chat-session");
  await expect(rows).toHaveCount(2);
  const gone = rows.filter({ hasText: "古い相談の続き" });
  await expect(gone).toContainText("全文はもう無い");
  await expect(gone.getByRole("link", { name: "全文" })).toHaveCount(0);
  await expect(gone.getByRole("button", { name: "続ける" })).toHaveCount(0);

  const kept = rows.filter({ hasText: "通知メールの再送の仕様を作りたい" });
  await kept.getByRole("link", { name: "全文" }).click();
  const transcript = page.getByTestId("chat-transcript");
  await expect(
    transcript.getByTestId("transcript-entry").first(),
  ).toContainText("通知メールの再送の仕様を作りたい");

  await kept.getByRole("button", { name: "続ける" }).click();
  await expect(panel.getByTestId("chat-message-owner")).toHaveText(
    "通知メールの再送の仕様を作りたい",
  );
  // 開いた会話の行には「続ける」が出ない
  await panel.getByRole("button", { name: "会話の一覧" }).click();
  await expect(kept).toContainText("いまの会話");
  await expect(kept.getByRole("button", { name: "続ける" })).toHaveCount(0);
});
