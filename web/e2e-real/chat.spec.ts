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

// i 番目に起動した実行エンジンが受けた、確認への答えの行。
function answersOf(start: number): unknown[] {
  const file = join(
    process.env[FAKE_LOG_ENV] ?? "",
    `start.${start}`,
    "answers",
  );
  if (!existsSync(file)) return [];
  return readFileSync(file, "utf8")
    .trim()
    .split("\n")
    .map((l) => JSON.parse(l));
}

for (const { button, reason, state, response } of [
  {
    button: "許可する",
    reason: "",
    state: "allowed",
    response: {
      behavior: "allow",
      updatedInput: { command: "rm -rf build", description: "d" },
    },
  },
  {
    button: "断る",
    reason: "消さないで",
    state: "denied",
    response: { behavior: "deny", message: "消さないで" },
  },
]) {
  test(`#1290: 許可の確認で「${button}」を押すと、実行エンジンへその答えだけが届き、返事が続く`, async ({
    page,
  }) => {
    const panel = await openFresh(page);
    await send(page, "許可を求める");
    const first = await engineLine(page, 0);
    const prompt = panel.getByTestId("chat-prompt");
    await expect(prompt).toHaveAttribute("data-state", "pending");
    if (reason !== "")
      await prompt.getByRole("textbox", { name: "断る理由" }).fill(reason);
    await prompt.getByRole("button", { name: button }).click();
    await expect(prompt).toHaveAttribute("data-state", state);
    await expect(prompt.getByRole("button")).toHaveCount(0);
    await expect(replies(page).nth(0)).toContainText("答えを受けた");
    await idle(page);
    // 許可の書き足し（updatedPermissions）を含まない、その 1 回だけの答え。
    expect(answersOf(first.start)).toEqual([
      {
        type: "control_response",
        response: { subtype: "success", request_id: "r1", response },
      },
    ]);
  });
}

test("#1290: 問いの選択肢を押すと、答えが updatedInput.answers に入って届き、返事が続く", async ({
  page,
}) => {
  const panel = await openFresh(page);
  await send(page, "問いを出す");
  const first = await engineLine(page, 0);
  const prompt = panel.getByTestId("chat-prompt");
  await expect(prompt).toHaveAttribute("data-state", "pending");
  await prompt.getByRole("button", { name: "案B" }).click();
  await expect(prompt).toHaveAttribute("data-state", "answered");
  await expect(replies(page).nth(0)).toContainText("答えを受けた");
  await idle(page);
  expect(answersOf(first.start)).toMatchObject([
    {
      response: {
        request_id: "q1",
        response: {
          behavior: "allow",
          updatedInput: { answers: { "どちらにする？": "案B" } },
        },
      },
    },
  ]);
});

const sessionRows = (page: Page) =>
  page.getByTestId("chat-panel").getByTestId("chat-session");

async function openSessionList(page: Page) {
  await page
    .getByTestId("chat-panel")
    .getByRole("button", { name: "会話の一覧", exact: true })
    .click();
}

test("#1290: 会話の一覧から閉じた会話を続けると、前のプロセスが終わり、次の発言は --resume で起動する。全文も開ける", async ({
  page,
}) => {
  const panel = await openFresh(page);
  await send(page, "続きを話す相談");
  await engineLine(page, 0);
  await idle(page);
  await panel.getByRole("button", { name: "新しい会話" }).click();
  await expect(panel.getByTestId("chat-message-owner")).toHaveCount(0);
  await send(page, "別の相談");
  const other = await engineLine(page, 0);
  await idle(page);
  const otherPid = pidOf(other.start, "pid");
  expect(alive(otherPid)).toBe(true);

  await openSessionList(page);
  // 新しい順。開いている会話には「続ける」が無い。
  await expect(sessionRows(page).nth(0)).toContainText("別の相談");
  await expect(
    sessionRows(page).nth(0).getByRole("button", { name: "続ける" }),
  ).toHaveCount(0);
  const target = sessionRows(page).nth(1);
  await expect(target).toContainText("続きを話す相談");
  await target.getByRole("button", { name: "続ける" }).click();
  await expect(panel.getByTestId("chat-message-owner")).toHaveText([
    /続きを話す相談/,
  ]);
  await expect.poll(() => alive(otherPid)).toBe(false);

  await send(page, "続き");
  const next = await engineLine(page, 1);
  expect(next).toMatchObject({ mode: "--resume", received: 1 });
  expect(next.start).toBeGreaterThan(other.start);
  await idle(page);

  await openSessionList(page);
  await sessionRows(page)
    .filter({ hasText: "続きを話す相談" })
    .getByRole("link", { name: "全文" })
    .click();
  const entries = page
    .getByTestId("chat-transcript")
    .getByTestId("transcript-entry");
  await expect(entries).toHaveText([
    /続きを話す相談/,
    /受信1/,
    /続き/,
    /--resume 受信1/,
  ]);
});

test("#1290: 全文のファイルが無い会話は「全文はもう無い」になり、続ける口と全文の口は transcript_unavailable で断る", async ({
  page,
}) => {
  const panel = await openFresh(page);
  await send(page, "全文を残さない相談");
  await engineLine(page, 0);
  await idle(page);
  const chat = await (
    await page.request.get("/api/v1/workspaces/seed/chat")
  ).json();
  await panel.getByRole("button", { name: "新しい会話" }).click();
  await expect(panel.getByTestId("chat-message-owner")).toHaveCount(0);

  await openSessionList(page);
  const row = sessionRows(page).filter({ hasText: "全文を残さない相談" });
  await expect(row).toContainText("全文はもう無い");
  await expect(row.getByRole("button", { name: "続ける" })).toHaveCount(0);
  await expect(row.getByRole("link", { name: "全文" })).toHaveCount(0);

  const base = `/api/v1/workspaces/seed/chat/sessions/${chat.session_id}`;
  const transcript = await page.request.get(`${base}/transcript`);
  expect(transcript.status()).toBe(404);
  expect((await transcript.json()).error.code).toBe("transcript_unavailable");
  const csrf = await page
    .locator('meta[name="flywheel-csrf"]')
    .getAttribute("content");
  const resume = await page.request.post(`${base}/resume`, {
    headers: { "X-Flywheel-CSRF": csrf ?? "" },
  });
  expect(resume.status()).toBe(409);
  expect((await resume.json()).error.code).toBe("transcript_unavailable");
});

test("#1290: 確認への答えと「続ける」は CSRF の値が無いと断られ、よそのサイトからの読み取りも断られる", async ({
  page,
}) => {
  const panel = await openFresh(page);
  await send(page, "許可を求める");
  const first = await engineLine(page, 0);
  const prompt = panel.getByTestId("chat-prompt");
  await expect(prompt).toHaveAttribute("data-state", "pending");
  const chat = await (
    await page.request.get("/api/v1/workspaces/seed/chat")
  ).json();
  const id = chat.messages.at(-1).prompts[0].id;
  const base = "/api/v1/workspaces/seed/chat";

  for (const path of [
    `/prompts/${id}/answer`,
    `/sessions/${chat.session_id}/resume`,
  ]) {
    const res = await page.request.post(base + path, {
      data: { decision: "allow" },
    });
    expect(res.status()).toBe(403);
  }
  for (const path of ["/sessions", `/sessions/${chat.session_id}/transcript`]) {
    const res = await page.request.get(base + path, {
      headers: { Origin: "http://evil.example" },
    });
    expect(res.status()).toBe(403);
  }
  await expect(prompt).toHaveAttribute("data-state", "pending");
  expect(answersOf(first.start)).toEqual([]);
  await panel.getByRole("button", { name: "止める" }).click();
  await expect(prompt).toHaveAttribute("data-state", "expired");
});
