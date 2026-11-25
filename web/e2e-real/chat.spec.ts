import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { type Page, expect, test } from "@playwright/test";
import { BASE_URL_ENV, FAKE_LOG_ENV } from "./global-setup";

// 対話の欄を、ブラウザから本物の `flywheel serve` と本物の対話の実行（internal/invoker）まで通して確かめる（#1289）。
// 偽にするのは claude（e2e-real/fake-claude.sh）だけ。返事の文の「起動N <指定> 受信M」で、同じプロセスへ
// 送られたか、どの指定で起動し直したかを見る。会話は 1 つの serve で共有するので、各試験は「新しい会話」から始める。
test.use({ baseURL: process.env[BASE_URL_ENV] });

async function openFresh(page: Page) {
  await page.goto("/#/a/seed/challenges");
  const panel = page.getByTestId("chat-panel");
  await expect(panel).toBeVisible();
  const fresh = panel.getByRole("button", { name: "新しい会話" });
  await expect(fresh).toBeEnabled();
  await fresh.click();
  await expect(panel.getByTestId("chat-message-owner")).toHaveCount(0);
  return panel;
}

async function send(page: Page, text: string) {
  const panel = page.getByTestId("chat-panel");
  await panel
    .getByRole("textbox", { name: "オーケストレーターへの文" })
    .fill(text);
  await panel.getByRole("button", { name: "送る" }).click();
}

const replies = (page: Page) =>
  page.getByTestId("chat-panel").getByTestId("chat-message-orchestrator");

// i 番目の返事の「起動N <指定> 受信M」を読む。
async function engineLine(page: Page, i: number) {
  const reply = replies(page).nth(i);
  await expect(reply).toContainText(/起動\d+ --[a-z-]+ 受信\d+/);
  const m = (await reply.innerText()).match(/起動(\d+) (--[a-z-]+) 受信(\d+)/);
  return { start: Number(m?.[1]), mode: m?.[2], received: Number(m?.[3]) };
}

function pidOf(start: number, name: "pid" | "child"): number {
  const file = join(process.env[FAKE_LOG_ENV] ?? "", `start.${start}`, name);
  if (!existsSync(file)) return 0;
  return Number(readFileSync(file, "utf8").trim());
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

const idle = (page: Page) =>
  expect(
    page.getByTestId("chat-panel").getByRole("button", { name: "止める" }),
  ).toHaveCount(0);

test("#1289: 2 通目は同じプロセスへ送られ、「新しい会話」はそのプロセスを終わらせて新しい指定で起動する", async ({
  page,
}) => {
  const panel = await openFresh(page);
  await send(page, "1 通目");
  const first = await engineLine(page, 0);
  expect(first).toMatchObject({ mode: "--session-id", received: 1 });
  await idle(page);
  const activity = replies(page).nth(0).getByTestId("chat-activity");
  await activity.getByText("使った道具（1 件）").click();
  await expect(activity.getByRole("listitem")).toHaveText([
    "Bash flywheel status 成功",
  ]);

  await send(page, "2 通目");
  expect(await engineLine(page, 1)).toEqual({ ...first, received: 2 });
  await idle(page);
  // 費用は累計の差で数える（0.1 → 0.2 の合計は 0.20）。
  await expect(panel.getByTestId("chat-cost")).toHaveText("$0.20");

  const pid = pidOf(first.start, "pid");
  expect(alive(pid)).toBe(true);
  await panel.getByRole("button", { name: "新しい会話" }).click();
  await expect(panel.getByTestId("chat-message-owner")).toHaveCount(0);
  await expect.poll(() => alive(pid)).toBe(false);
  await send(page, "別の会話");
  const next = await engineLine(page, 0);
  expect(next).toMatchObject({ mode: "--session-id", received: 1 });
  expect(next.start).toBeGreaterThan(first.start);
  await idle(page);
});

test("#1289: 「止める」はプロセスと子を終わらせて出た文を残し、次の発言は続きで起動する", async ({
  page,
}) => {
  const panel = await openFresh(page);
  await send(page, "子を残して待つ");
  const first = await engineLine(page, 0);
  await expect.poll(() => pidOf(first.start, "child")).toBeGreaterThan(0);
  const pids = [pidOf(first.start, "pid"), pidOf(first.start, "child")];
  // 返事の進行中は「新しい会話」を押せない。
  await expect(
    panel.getByRole("button", { name: "新しい会話" }),
  ).toBeDisabled();

  await panel.getByRole("button", { name: "止める" }).click();
  await expect(replies(page).nth(0).getByTestId("chat-state")).toHaveText(
    "止めました。",
  );
  await expect(replies(page).nth(0)).toContainText(
    `起動${first.start} --session-id 受信1`,
  );
  for (const pid of pids) await expect.poll(() => alive(pid)).toBe(false);

  await send(page, "続き");
  const next = await engineLine(page, 1);
  expect(next).toMatchObject({ mode: "--resume", received: 1 });
  expect(next.start).toBeGreaterThan(first.start);
  await idle(page);
});

test("#1289: 返事の途中でプロセスが落ちると失敗になって出た文が残り、次の発言は続きで起動する", async ({
  page,
}) => {
  await openFresh(page);
  await send(page, "途中で落ちる");
  const first = await engineLine(page, 0);
  await expect(replies(page).nth(0).getByTestId("chat-state")).toHaveText(
    "返事を作れませんでした。もう一度送れます。",
  );
  await expect(replies(page).nth(0)).toContainText(`起動${first.start}`);

  await send(page, "続き");
  expect(await engineLine(page, 1)).toMatchObject({
    mode: "--resume",
    received: 1,
  });
  await idle(page);
});

test("#1289: 確認を待つ間に「止める」を押すと、返事が止まり、確認は答えられなかったになる", async ({
  page,
}) => {
  const panel = await openFresh(page);
  await send(page, "許可を求める");
  const prompt = panel.getByTestId("chat-prompt");
  await expect(prompt).toHaveAttribute("data-state", "pending");
  await expect(prompt.getByTestId("chat-prompt-target")).toHaveText(
    "rm -rf build",
  );
  await panel.getByRole("button", { name: "止める" }).click();
  await expect(prompt).toHaveAttribute("data-state", "expired");
  await expect(replies(page).nth(0).getByTestId("chat-state")).toHaveText(
    "止めました。",
  );
});
