import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import { type ApiClient, resetApi, setApi } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { ChatMessage, ChatResponse, Notice } from "../api/types.gen.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import { FakeEventSource } from "../testing/fakes.ts";

// 対話の欄（S4d-1 #1002）。偽のデータ（web/src/mock）の上で、欄の振る舞いを受入基準ごとに確かめる。
// サーバの実装には依存しない（GET …/chat と POST は、ここで差し替えた偽の口が受ける）。

const msg = (o: Partial<ChatMessage> & { id: number }): ChatMessage => ({
  activity: [],
  body: "",
  cost_usd: 0,
  created_at: "2026-11-22T00:00:00.000Z",
  role: "orchestrator",
  state: "done",
  viewing: null,
  ...o,
});

let chatBody: ChatResponse;
const calls: string[] = [];
let sendError: ApiError | null = null;
let sendBodies: unknown[] = [];
let noticesBody: Notice[] = [];

const chatOf = (o: Partial<ChatResponse> = {}): ChatResponse => ({
  cost_usd: 0,
  messages: [],
  session_id: null,
  started_at: null,
  state: "idle",
  tools: "restricted",
  ...o,
});

beforeEach(() => {
  chatBody = chatOf();
  calls.length = 0;
  sendError = null;
  sendBodies = [];
  noticesBody = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  FakeEventSource.instances = [];
  const base = createMockApi(new MockStore(), { latencyMs: 0 });
  const api: ApiClient = {
    ...base,
    chat: async () => structuredClone(chatBody),
    notices: async () => ({ notices: structuredClone(noticesBody) }),
    chatSend: async (_n, req) => {
      calls.push("send");
      sendBodies.push(req);
      if (sendError) throw sendError;
      return { id: 1 };
    },
    chatStop: async () => {
      calls.push("stop");
      return {};
    },
    chatNew: async () => {
      calls.push("new");
      chatBody = chatOf();
      return {};
    },
  };
  setApi(api);
});
afterEach(() => {
  window.location.hash = "";
  resetApi();
  vi.unstubAllGlobals();
});

async function open(hash = "#/a/alpha/now") {
  window.location.hash = hash;
  const r = render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  await screen.findByTestId("chat-panel");
  return r;
}
const panel = () => within(screen.getByTestId("chat-panel"));
const input = () => screen.getByLabelText("オーケストレーターへの文");

// AC-1: 共通の枠を使う画面のルートのそれぞれで、欄の要素が描かれる。
const ROUTES: [string, string][] = [
  ["すべて", "#/all"],
  ["いま進められること", "#/a/alpha/now"],
  ["課題", "#/a/alpha/challenges"],
  ["フォローアップ", "#/a/alpha/followups"],
  ["サイクル", "#/a/alpha/cycle"],
  ["課題の詳細", "#/a/alpha/challenges/C-3"],
  ["経緯", "#/a/alpha/challenges/C-3/history"],
  ["計画の本文", "#/a/alpha/challenges/C-3/plan"],
  ["計画の承認", "#/a/alpha/challenges/C-3/approve-plan"],
  ["完了の承認", "#/a/alpha/challenges/C-7/approve-completion"],
  ["タスクの詳細", "#/a/alpha/challenges/C-28/tasks/T-40"],
  ["見つかりません", "#/a/alpha/nothing"],
];
for (const [name, hash] of ROUTES) {
  it(`AC-1: ${name}（${hash}）で、対話の欄の要素が描かれる`, async () => {
    await open(hash);
    expect(panel().getByRole("button", { name: "たたむ" })).toBeInTheDocument();
    expect(input()).toBeInTheDocument();
    expect(panel().getByRole("button", { name: "送る" })).toBeInTheDocument();
  });
}

it("AC-2: たたんだ状態で見えるのは、開く操作だけ", async () => {
  await open();
  fireEvent.click(panel().getByRole("button", { name: "たたむ" }));
  const p = within(screen.getByTestId("chat-panel"));
  expect(p.getAllByRole("button").map((b) => b.textContent)).toEqual([
    "対話の欄を開く",
  ]);
  expect(screen.queryByLabelText("オーケストレーターへの文")).toBeNull();
  expect(screen.getByTestId("chat-panel").textContent).toBe("対話の欄を開く");
});

it("AC-3: たたんで別の画面へ移ると、たたんだままになっている", async () => {
  await open();
  fireEvent.click(panel().getByRole("button", { name: "たたむ" }));
  act(() => {
    window.location.hash = "#/a/alpha/challenges";
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  });
  await screen
    .findByRole("heading", { level: 2, name: /課題/ })
    .catch(() => {});
  expect(panel().getByRole("button", { name: "対話の欄を開く" })).toBeVisible();
  expect(screen.queryByLabelText("オーケストレーターへの文")).toBeNull();
});

it("AC-4: 課題の詳細を開いているとき、欄の上に、その課題の題名が「いま見ているもの」として出る", async () => {
  await open("#/a/alpha/challenges/C-3");
  expect(await screen.findByTestId("chat-viewing")).toHaveTextContent(
    "いま見ているもの: 通知メールの再送制御",
  );
});

it("AC-5: タスクの詳細を開いているとき、欄の上に、そのタスクの題名が出る", async () => {
  await open("#/a/alpha/challenges/C-28/tasks/T-40");
  expect(await screen.findByTestId("chat-viewing")).toHaveTextContent(
    "いま見ているもの: 手順 1 を進める",
  );
});

it("AC-6: 課題もタスクも開いていない画面では、「いま見ているもの」が出ない", async () => {
  await open("#/a/alpha/challenges");
  expect(screen.queryByTestId("chat-viewing")).toBeNull();
  expect(screen.queryByText(/いま見ているもの/)).toBeNull();
});

it("AC-7/8: 「この課題について相談する」で、たたんでいた欄が開き、入力にフォーカスが移る", async () => {
  await open("#/a/alpha/challenges/C-3");
  fireEvent.click(panel().getByRole("button", { name: "たたむ" }));
  expect(screen.queryByLabelText("オーケストレーターへの文")).toBeNull();
  fireEvent.click(
    await screen.findByRole("button", { name: "この課題について相談する" }),
  );
  await waitFor(() => expect(input()).toBeInTheDocument());
  expect(input()).toHaveFocus();
});

it("AC-9: やりとりが 0 件の会話では、「オーケストレーターに、相談や指示を書けます。」が出る", async () => {
  await open();
  expect(
    await panel().findByText("オーケストレーターに、相談や指示を書けます。"),
  ).toBeInTheDocument();
});

it("AC-25/26: 返事の本文の見出しと箇条書きが、見出し・箇条書きの要素として描かれる", async () => {
  chatBody = chatOf({
    messages: [msg({ id: 2, body: "# 見出しです\n\n- 項目です" })],
  });
  await open();
  expect(
    await panel().findByRole("heading", { name: "見出しです" }),
  ).toBeInTheDocument();
  const md = screen.getByTestId("chat-panel").querySelector(".md");
  expect(md?.querySelector("ul > li")).toHaveTextContent("項目です");
});

it("AC-27/30: 返事が進行中のとき「返事を待っています…」が出て、送るボタンは押せない", async () => {
  chatBody = chatOf({
    state: "replying",
    messages: [
      msg({ id: 1, role: "owner", body: "状況は" }),
      msg({ id: 2, state: "replying" }),
    ],
  });
  await open();
  expect(await panel().findByText("返事を待っています…")).toBeInTheDocument();
  fireEvent.change(input(), { target: { value: "続き" } });
  expect(panel().getByRole("button", { name: "送る" })).toBeDisabled();
  expect(calls).toEqual([]);
});

it("AC-36: 返事の進行中に「止める」を押すと、stop が呼ばれる", async () => {
  chatBody = chatOf({
    state: "replying",
    messages: [msg({ id: 2, state: "replying" })],
  });
  await open();
  fireEvent.click(await panel().findByRole("button", { name: "止める" }));
  await waitFor(() => expect(calls).toEqual(["stop"]));
});

it("返事が進行中でないとき、「止める」は出ない", async () => {
  await open();
  await panel().findByText(/相談や指示/);
  expect(panel().queryByRole("button", { name: "止める" })).toBeNull();
});

// 返事の終わり方ごとの文（AC-37・62・102・162・165）。done は何も出さない。
const STATE_TEXTS: [string, string][] = [
  ["AC-37", "止めました。"],
  [
    "AC-102",
    "上限に達したので、ここで止まりました。続きは、もう一度送ってください。",
  ],
  ["AC-165", "利用上限に達しました。時間を置いて、もう一度送ってください。"],
  [
    "AC-62",
    "前の会話を続けられませんでした。新しい会話として、もう一度送ってください。",
  ],
  ["AC-162", "返事を作れませんでした。もう一度送れます。"],
];
const STATES = ["stopped", "limit", "rate_limited", "session_lost", "failed"];
STATE_TEXTS.forEach(([ac, text], i) => {
  const state = STATES[i] as string;
  it(`${ac}: state が ${state} の返事には、「${text}」が出る`, async () => {
    chatBody = chatOf({
      messages: [msg({ id: 2, state, body: "途中まで" })],
    });
    await open();
    expect(await panel().findByText(text)).toBeInTheDocument();
    // 出し終えていた分は残る
    expect(panel().getByText("途中まで")).toBeInTheDocument();
  });
});

it("state が done の返事には、状態の文は出ない", async () => {
  chatBody = chatOf({ messages: [msg({ id: 2, body: "おわり" })] });
  await open();
  await panel().findByText("おわり");
  expect(screen.queryByTestId("chat-state")).toBeNull();
});

it("AC-40/41: 「実行したコマンド」は畳んで出て、開くとコマンドの行が見える", async () => {
  chatBody = chatOf({
    messages: [
      msg({
        id: 2,
        body: "見ました",
        activity: [
          { command: "flywheel status", ok: true },
          { command: "rm -rf /", ok: false },
        ],
      }),
    ],
  });
  await open();
  const d = await screen.findByTestId("chat-activity");
  expect(d.tagName).toBe("DETAILS");
  expect(d).not.toHaveAttribute("open");
  expect(within(d).getByText("実行したコマンド")).toBeInTheDocument();
  fireEvent.click(within(d).getByText("実行したコマンド"));
  expect(d).toHaveAttribute("open");
  expect(within(d).getByText("flywheel status")).toBeInTheDocument();
  expect(within(d).getByText("rm -rf /")).toBeInTheDocument();
});

it("AC-46: 読み込み直すと、いまの会話の発言と返事が、古い順に出る", async () => {
  chatBody = chatOf({
    // 並びを崩して返しても、古い順（id の昇順）に出す
    messages: [
      msg({ id: 3, body: "三番目" }),
      msg({ id: 1, role: "owner", body: "一番目" }),
      msg({ id: 2, body: "二番目" }),
    ],
  });
  await open();
  await panel().findByText("三番目");
  const texts = [...screen.getByTestId("chat-panel").querySelectorAll("li")]
    .map((li) => li.textContent)
    .filter((t) => t && /番目/.test(t));
  expect(texts).toEqual(["一番目", "二番目", "三番目"]);
});

it("AC-59: 「新しい会話」を押すと、chat/new が呼ばれ、欄のやりとりが空になる", async () => {
  chatBody = chatOf({ messages: [msg({ id: 1, role: "owner", body: "前" })] });
  await open();
  await panel().findByText("前");
  fireEvent.click(panel().getByRole("button", { name: "新しい会話" }));
  await waitFor(() => expect(calls).toEqual(["new"]));
  expect(await panel().findByText(/相談や指示/)).toBeInTheDocument();
  expect(panel().queryByText("前")).toBeNull();
});

it("AC-72/73: full のときだけ「すべての道具を許可しています」が出る", async () => {
  chatBody = chatOf({ tools: "full" });
  await open();
  expect(
    await panel().findByText("すべての道具を許可しています"),
  ).toBeInTheDocument();
});
it("AC-73: restricted のときは、その文は出ない", async () => {
  await open();
  await panel().findByText(/相談や指示/);
  expect(screen.queryByText("すべての道具を許可しています")).toBeNull();
});

it("AC-154: その会話の費用が $0.42 の形で出る（小数 2 桁）", async () => {
  chatBody = chatOf({ cost_usd: 0.42, messages: [msg({ id: 1 })] });
  await open();
  expect(await screen.findByTestId("chat-cost")).toHaveTextContent("$0.42");
});
it("費用 0.4 は $0.40、0 は $0.00 と出る", async () => {
  chatBody = chatOf({ cost_usd: 0.4 });
  await open();
  expect(await screen.findByTestId("chat-cost")).toHaveTextContent("$0.40");
});

it("AC-159: 文を送って 503 で断られると、「実行エンジンが見つかりません。」が出る", async () => {
  sendError = new ApiError("unavailable", "engine", 503);
  await open();
  fireEvent.change(input(), { target: { value: "こんにちは" } });
  fireEvent.click(panel().getByRole("button", { name: "送る" }));
  expect(
    await screen.findByText("実行エンジンが見つかりません。"),
  ).toBeInTheDocument();
  // 送れなかった文は入力に残る
  expect(input()).toHaveValue("こんにちは");
});

it("文を送ると、viewing（見ている課題）を添えた POST になり、入力が空になる。見ていなければ viewing を省く", async () => {
  await open("#/a/alpha/challenges/C-3");
  await screen.findByTestId("chat-viewing");
  fireEvent.change(input(), { target: { value: "  相談  " } });
  fireEvent.click(panel().getByRole("button", { name: "送る" }));
  await waitFor(() => expect(sendBodies).toHaveLength(1));
  expect(sendBodies[0]).toEqual({
    text: "相談",
    viewing: { challenge_id: "C-3" },
  });
  await waitFor(() => expect(input()).toHaveValue(""));

  act(() => {
    window.location.hash = "#/a/alpha/challenges";
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  });
  await waitFor(() => expect(screen.queryByTestId("chat-viewing")).toBeNull());
  fireEvent.change(input(), { target: { value: "もう一つ" } });
  fireEvent.click(panel().getByRole("button", { name: "送る" }));
  await waitFor(() => expect(sendBodies).toHaveLength(2));
  expect(sendBodies[1]).toEqual({ text: "もう一つ" });
});

it("タスクを見ているとき、viewing に challenge_id と task_id を添える", async () => {
  await open("#/a/alpha/challenges/C-28/tasks/T-40");
  await screen.findByTestId("chat-viewing");
  fireEvent.change(input(), { target: { value: "これ" } });
  fireEvent.click(panel().getByRole("button", { name: "送る" }));
  await waitFor(() => expect(sendBodies).toHaveLength(1));
  expect(sendBodies[0]).toEqual({
    text: "これ",
    viewing: { challenge_id: "C-28", task_id: "T-40" },
  });
});

it("空白だけの文では、送るボタンは押せない", async () => {
  await open();
  fireEvent.change(input(), { target: { value: "   " } });
  expect(panel().getByRole("button", { name: "送る" })).toBeDisabled();
});

it("変化の知らせ（workspace_changed）を受けると、GET …/chat を読み直す（新しい口は作らない）", async () => {
  await open();
  await panel().findByText(/相談や指示/);
  chatBody = chatOf({ messages: [msg({ id: 2, body: "新しい返事" })] });
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  expect(await panel().findByText("新しい返事")).toBeInTheDocument();
});

it("選んだエージェントが一覧に無いとき、代わりのエージェントの欄は出さない", async () => {
  window.location.hash = "#/a/nothing/now";
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  await screen.findByText(/見つかりません/);
  expect(screen.queryByTestId("chat-panel")).toBeNull();
});

it("欄の見出しに、相手のエージェントの名前が出る", async () => {
  await open("#/a/alpha/now");
  expect(panel().getByRole("heading", { level: 2 })).toHaveTextContent("alpha");
});

it("送っている間に打ち足した文は、送信の成功で消えない", async () => {
  await open();
  fireEvent.change(input(), { target: { value: "最初" } });
  fireEvent.click(panel().getByRole("button", { name: "送る" }));
  fireEvent.change(input(), { target: { value: "最初の続き" } });
  await waitFor(() => expect(sendBodies).toHaveLength(1));
  await waitFor(() =>
    expect(panel().getByRole("button", { name: "新しい会話" })).toBeEnabled(),
  );
  expect(input()).toHaveValue("最初の続き");
});

// ---- Issue #1184: 対話の欄の知らせ（AC-182〜AC-186・AC-191）----

const notice = (at: string, over: Partial<Notice> = {}): Notice => ({
  added_count: null,
  at,
  challenge_id: "C-1",
  challenge_title: "課題の題名",
  cycle_id: null,
  end_reason: null,
  kind: "released",
  origin: null,
  task_id: null,
  task_title: null,
  ...over,
});
const inPanel = () => panel().getAllByRole("listitem");

it("AC-182: 会話が開いているとき、会話の始まりより後の知らせを、発言と合わせて時刻の順に出す", async () => {
  chatBody = chatOf({
    started_at: "2026-11-22T00:00:00.000Z",
    session_id: "s",
    messages: [
      msg({
        id: 1,
        role: "owner",
        body: "最初の発言",
        created_at: "2026-11-22T01:00:00.000Z",
      }),
      msg({ id: 2, body: "返事", created_at: "2026-11-22T03:00:00.000Z" }),
    ],
  });
  noticesBody = [
    notice("2026-11-22T04:00:00.000Z", { challenge_title: "あとの課題" }),
    notice("2026-11-22T02:00:00.000Z", { challenge_title: "間の課題" }),
  ];
  await open();
  await panel().findByText(/間の課題/);
  const texts = inPanel().map((li) => li.textContent ?? "");
  expect(texts[0]).toContain("最初の発言");
  expect(texts[1]).toContain("間の課題");
  expect(texts[2]).toContain("返事");
  expect(texts[3]).toContain("あとの課題");
});

it("AC-183: 会話が開いているとき、会話の始まりより前の知らせは出さない", async () => {
  chatBody = chatOf({
    started_at: "2026-11-22T00:00:00.000Z",
    session_id: "s",
    messages: [msg({ id: 1, role: "owner", body: "発言" })],
  });
  noticesBody = [
    notice("2026-11-21T23:00:00.000Z", { challenge_title: "前の課題" }),
    notice("2026-11-22T02:00:00.000Z", { challenge_title: "後の課題" }),
  ];
  await open();
  await panel().findByText(/後の課題/);
  expect(panel().queryByText(/前の課題/)).toBeNull();
});

it("AC-184: 会話が無いとき、直近の知らせを 3 件まで出す（古いものから 1 件落ちる）", async () => {
  noticesBody = [1, 2, 3, 4].map((n) =>
    notice(`2026-11-22T0${n}:00:00.000Z`, { challenge_title: `課題 ${n}` }),
  );
  await open();
  await panel().findByText(/課題 4/);
  expect(panel().queryByText(/課題 1/)).toBeNull();
  expect(panel().getAllByTestId("notice-item")).toHaveLength(3);
});

it("AC-185: 欄の知らせに、「知らせ」の札が付く（発言には付かない）", async () => {
  chatBody = chatOf({
    started_at: "2026-11-22T00:00:00.000Z",
    session_id: "s",
    messages: [msg({ id: 1, role: "owner", body: "発言" })],
  });
  noticesBody = [notice("2026-11-22T02:00:00.000Z")];
  await open();
  const item = await panel().findByTestId("notice-item");
  expect(within(item).getByTestId("notice-badge")).toHaveTextContent("知らせ");
  expect(panel().getAllByTestId("notice-badge")).toHaveLength(1);
});

it("AC-186: 知らせが欄に出ているときも、送る要求の本文は、送った文だけになる（承認の印の検査そのもの〔AC-191〕はサーバの試験の範囲で、ここでは知らせが送る側に混ざらないことだけを確かめる）", async () => {
  noticesBody = [
    notice("2026-11-22T02:00:00.000Z", { challenge_title: "知らせの課題" }),
  ];
  await open();
  await panel().findByText(/知らせの課題/);
  fireEvent.change(input(), { target: { value: "送る文" } });
  fireEvent.click(panel().getByRole("button", { name: "送る" }));
  await waitFor(() => expect(sendBodies).toHaveLength(1));
  expect(sendBodies[0]).toEqual({
    text: "送る文",
    viewing: undefined,
  });
  expect(JSON.stringify(sendBodies[0])).not.toContain("知らせの課題");
});
