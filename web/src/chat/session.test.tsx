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
import type {
  ChatMessage,
  ChatPrompt,
  ChatPromptAnswerRequest,
  ChatResponse,
  ChatSessionSummary,
  ChatTranscriptResponse,
  ShowCardResponse,
} from "../api/types.gen.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import { FakeEventSource } from "../testing/fakes.ts";

// 対話の欄の作り替え（docs/features/chat-session.md S1。Issue #1288）: 道具の一覧・確認のボタン・
// 会話の一覧・全文の画面・課題の詳細の仕様づくりの費用と会話。口はここで差し替えた偽の口が受ける。

const msg = (o: Partial<ChatMessage> & { id: number }): ChatMessage => ({
  activity: [],
  body: "",
  cost_usd: 0,
  created_at: "2026-11-25T00:00:00.000Z",
  prompts: [],
  role: "orchestrator",
  state: "done",
  viewing: null,
  ...o,
});

const LONG_COMMAND = `printf '%s' ${"abcdefghij".repeat(80)}\n  && echo   "末尾の印"`;

const permission = (o: Partial<ChatPrompt> = {}): ChatPrompt => ({
  answer: null,
  answered_at: null,
  created_at: "2026-11-25T00:00:00.000Z",
  id: 7,
  kind: "permission",
  questions: [],
  state: "pending",
  target: LONG_COMMAND,
  tool: "Bash",
  ...o,
});

const single = {
  multi_select: false,
  options: [
    { label: "案A", description: "小さく直す" },
    { label: "案B", description: "作り直す" },
  ],
  question: "どちらの案にしますか？",
};
const multi = {
  multi_select: true,
  options: [
    { label: "試験", description: "" },
    { label: "文書", description: "" },
    { label: "画面", description: "" },
  ],
  question: "どれを直しますか？",
};
const question = (
  questions: ChatPrompt["questions"],
  o: Partial<ChatPrompt> = {},
): ChatPrompt =>
  permission({
    kind: "question",
    tool: "AskUserQuestion",
    target: null,
    questions,
    ...o,
  });

const session = (o: Partial<ChatSessionSummary> = {}): ChatSessionSummary => ({
  challenges: [],
  closed_at: "2026-11-24T10:00:00.000Z",
  cost_usd: 0,
  first_message: "仕様を作りたい",
  id: "s-closed",
  open: false,
  started_at: "2026-11-24T09:00:00.000Z",
  transcript_available: true,
  ...o,
});

let chatBody: ChatResponse;
let sessions: ChatSessionSummary[];
let transcript: ChatTranscriptResponse | ApiError;
let answerError: ApiError | null;
let resumeError: ApiError | null;
let detail: Partial<ShowCardResponse["challenge"]>;
let answers: [number, ChatPromptAnswerRequest][];
let resumed: string[];
let transcriptAsked: string[];
let stops: number;

const chatOf = (o: Partial<ChatResponse> = {}): ChatResponse => ({
  cost_usd: 0,
  messages: [],
  session_id: null,
  started_at: null,
  state: "idle",
  ...o,
});
const replyingWith = (m: Partial<ChatMessage>): ChatResponse =>
  chatOf({
    state: "replying",
    messages: [msg({ id: 2, state: "replying", ...m })],
  });

beforeEach(() => {
  chatBody = chatOf();
  sessions = [];
  transcript = { entries: [] };
  answerError = null;
  resumeError = null;
  detail = {};
  answers = [];
  resumed = [];
  transcriptAsked = [];
  stops = 0;
  vi.stubGlobal("EventSource", FakeEventSource);
  FakeEventSource.instances = [];
  const base = createMockApi(new MockStore(), { latencyMs: 0 });
  const api: ApiClient = {
    ...base,
    chat: async () => structuredClone(chatBody),
    notices: async () => ({ notices: [] }),
    chatStop: async () => {
      stops++;
      return {};
    },
    chatAnswer: async (_n, id, req) => {
      answers.push([id, req]);
      if (answerError) throw answerError;
      return {};
    },
    chatSessions: async () => ({ sessions: structuredClone(sessions) }),
    chatResume: async (_n, id) => {
      resumed.push(id);
      if (resumeError) throw resumeError;
      chatBody = chatOf({
        session_id: id,
        messages: [msg({ id: 1, role: "owner", body: "続きの会話の発言" })],
      });
      return {};
    },
    chatTranscript: async (_n, id) => {
      transcriptAsked.push(id);
      if (transcript instanceof ApiError) throw transcript;
      return structuredClone(transcript);
    },
    challenge: async (n, id) => {
      const s = await base.challenge(n, id);
      return { ...s, challenge: { ...s.challenge, ...detail } };
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
const button = (name: string | RegExp) => panel().getByRole("button", { name });
const noButton = (name: string | RegExp) =>
  expect(panel().queryByRole("button", { name })).toBeNull();
const findPrompt = () => panel().findByTestId("chat-prompt");

// ---- 道具の一覧 ----

it("道具の一覧は畳んで出て、1 行に道具の名前・対象・状態を出す。「実行中」は running の行にだけ出る", async () => {
  chatBody = replyingWith({
    activity: [
      { id: "a", tool: "Read", target: "docs/a.md", state: "ok" },
      { id: "b", tool: "Bash", target: "make gate", state: "error" },
      { id: "c", tool: "Grep", target: "検索の語", state: "running" },
    ],
  });
  await open();
  const d = await panel().findByTestId("chat-activity");
  expect(d.tagName).toBe("DETAILS");
  expect(d).not.toHaveAttribute("open");
  expect(d.querySelector("summary")).toHaveTextContent("使った道具（3 件）");
  const rows = within(d).getAllByRole("listitem");
  expect(rows.map((r) => r.textContent)).toEqual([
    "Read docs/a.md 成功",
    "Bash make gate 失敗",
    "Grep 検索の語 実行中",
  ]);
  expect(within(d).getAllByText("実行中")).toHaveLength(1);
});

// ---- 許可の確認 ----

it("許可の確認は、道具の名前と対象の全文（切らず、改行と空白もそのまま）を出し、「許可する」「断る」と断る理由の欄を持つ", async () => {
  chatBody = replyingWith({ prompts: [permission()] });
  await open();
  const p = await findPrompt();
  expect(p).toHaveTextContent("Bash");
  expect(within(p).getByTestId("chat-prompt-target").textContent).toBe(
    LONG_COMMAND,
  );
  expect(button("許可する")).toBeEnabled();
  expect(button("断る")).toBeEnabled();
  expect(
    within(p).getByRole("textbox", { name: "断る理由（任意）" }),
  ).toBeInTheDocument();
  // 設定へ書き足す答え（以後も許す）は出さない
  expect(
    within(p)
      .getAllByRole("button")
      .map((b) => b.textContent),
  ).toEqual(["許可する", "断る"]);
});

it("「許可する」は decision: allow だけを送る（欄に書いた理由は添えない）", async () => {
  chatBody = replyingWith({ prompts: [permission()] });
  await open();
  await findPrompt();
  fireEvent.change(screen.getByLabelText("断る理由（任意）"), {
    target: { value: "書きかけ" },
  });
  fireEvent.click(button("許可する"));
  await waitFor(() => expect(answers).toEqual([[7, { decision: "allow" }]]));
});

it("理由の欄で Enter を押しても、何も送らない", async () => {
  chatBody = replyingWith({ prompts: [permission()] });
  await open();
  await findPrompt();
  const reason = screen.getByLabelText("断る理由（任意）");
  expect(reason.closest("form")).toBeNull();
  fireEvent.keyDown(reason, { key: "Enter" });
  expect(answers).toEqual([]);
});

it("「断る」は decision: deny を送り、書いた理由は message に添える", async () => {
  chatBody = replyingWith({ prompts: [permission()] });
  await open();
  await findPrompt();
  fireEvent.change(screen.getByLabelText("断る理由（任意）"), {
    target: { value: "  別の手順で  " },
  });
  fireEvent.click(button("断る"));
  await waitFor(() =>
    expect(answers).toEqual([[7, { decision: "deny", message: "別の手順で" }]]),
  );
});

it("理由が空白だけの「断る」は、message を省く", async () => {
  chatBody = replyingWith({ prompts: [permission()] });
  await open();
  await findPrompt();
  fireEvent.change(screen.getByLabelText("断る理由（任意）"), {
    target: { value: "   " },
  });
  fireEvent.click(button("断る"));
  await waitFor(() => expect(answers).toEqual([[7, { decision: "deny" }]]));
});

it("答えを送っている間は答えのボタンを押せず、「止める」は押せる", async () => {
  chatBody = replyingWith({ prompts: [permission()] });
  let release: () => void = () => {};
  const base = createMockApi(new MockStore(), { latencyMs: 0 });
  let calls = 0;
  setApi({
    ...base,
    chat: async () => structuredClone(chatBody),
    notices: async () => ({ notices: [] }),
    chatAnswer: () => {
      calls++;
      return new Promise<void>((r) => {
        release = r;
      });
    },
    chatStop: async () => {
      stops++;
      return {};
    },
  });
  await open();
  await findPrompt();
  expect(button("止める")).toBeEnabled();
  fireEvent.click(button("許可する"));
  await waitFor(() => expect(button("許可する")).toBeDisabled());
  expect(button("断る")).toBeDisabled();
  fireEvent.click(button("許可する"));
  expect(calls).toBe(1);
  expect(button("止める")).toBeEnabled();
  fireEvent.click(button("止める"));
  await waitFor(() => expect(stops).toBe(1));
  await act(async () => release());
});

const ANSWERED: [string, ChatPrompt, string[]][] = [
  [
    "許可した確認",
    permission({
      state: "allowed",
      answer: { decision: "allow", message: null, answers: null },
      answered_at: "2026-11-25T00:01:00.000Z",
    }),
    ["Bash", "許可しました"],
  ],
  [
    "理由を添えて断った確認",
    permission({
      state: "denied",
      answer: { decision: "deny", message: "別の手順で", answers: null },
      answered_at: "2026-11-25T00:01:00.000Z",
    }),
    ["断りました", "別の手順で"],
  ],
  [
    "答えた問い",
    question([single], {
      state: "answered",
      answer: {
        decision: null,
        message: null,
        answers: { [single.question]: "案B" },
      },
      answered_at: "2026-11-25T00:01:00.000Z",
    }),
    [single.question, "案B"],
  ],
  [
    "答えられなかった確認",
    permission({ state: "expired" }),
    ["答えられませんでした"],
  ],
];
for (const [name, prompt, texts] of ANSWERED) {
  it(`${name}は、ボタンも入力の欄も出さず、結果を出す`, async () => {
    chatBody = chatOf({ messages: [msg({ id: 2, prompts: [prompt] })] });
    await open();
    const p = await findPrompt();
    for (const t of texts) expect(p).toHaveTextContent(t);
    expect(within(p).queryAllByRole("button")).toEqual([]);
    expect(p.querySelectorAll("input, textarea")).toHaveLength(0);
    expect(within(p).queryByTestId("chat-prompt-target")).toBeNull();
  });
}

it("答えが 409 で断られると、その旨が確認の中に出て、読み直した結果（答えられなかった）になる", async () => {
  chatBody = replyingWith({ prompts: [permission()] });
  await open();
  await findPrompt();
  answerError = new ApiError("conflict", "not pending", 409);
  chatBody = chatOf({
    messages: [msg({ id: 2, prompts: [permission({ state: "expired" })] })],
  });
  fireEvent.click(button("許可する"));
  const p = await findPrompt();
  await waitFor(() => expect(p).toHaveTextContent("答えられませんでした"));
  expect(within(p).getByRole("alert")).toHaveTextContent(
    "この確認は、もう答えを待っていません。",
  );
});

// ---- 問い ----

it("1 つだけの 1 つ選ぶ問いは、選択肢ごとのボタンを押すと、その見出しを答えとして送る", async () => {
  chatBody = replyingWith({ prompts: [question([single])] });
  await open();
  const p = await findPrompt();
  expect(p).toHaveTextContent(single.question);
  expect(p).toHaveTextContent("小さく直す");
  expect(button(/案A/)).toBeEnabled();
  expect(
    within(p).getByRole("textbox", {
      name: `選択肢に無い答え: ${single.question}`,
    }),
  ).toBeInTheDocument();
  fireEvent.click(button(/案B/));
  await waitFor(() =>
    expect(answers).toEqual([[7, { answers: { [single.question]: "案B" } }]]),
  );
});

it("選択肢に無い答えは、文を書いて「送る」で送る（空では押せない）", async () => {
  chatBody = replyingWith({ prompts: [question([single])] });
  await open();
  const p = await findPrompt();
  const send = within(p).getByRole("button", { name: "送る" });
  expect(send).toBeDisabled();
  fireEvent.change(
    within(p).getByLabelText(`選択肢に無い答え: ${single.question}`),
    { target: { value: " 案C にする " } },
  );
  fireEvent.click(send);
  await waitFor(() =>
    expect(answers).toEqual([
      [7, { answers: { [single.question]: "案C にする" } }],
    ]),
  );
});

it("複数を選べる問いは、選択肢ごとのチェックと「送る」を出し、選んだ見出しを選択肢の順に「, 」でつないで送る", async () => {
  chatBody = replyingWith({ prompts: [question([multi])] });
  await open();
  const p = await findPrompt();
  expect(within(p).getAllByRole("checkbox")).toHaveLength(3);
  const send = within(p).getByRole("button", { name: "送る" });
  expect(send).toBeDisabled();
  fireEvent.click(within(p).getByRole("checkbox", { name: "画面" }));
  fireEvent.click(within(p).getByRole("checkbox", { name: "試験" }));
  fireEvent.click(send);
  await waitFor(() =>
    expect(answers).toEqual([
      [7, { answers: { [multi.question]: "試験, 画面" } }],
    ]),
  );
});

it("複数を選べる問いは、チェックと選択肢に無い答えを合わせて送れる", async () => {
  chatBody = replyingWith({ prompts: [question([multi])] });
  await open();
  const p = await findPrompt();
  fireEvent.click(within(p).getByRole("checkbox", { name: "文書" }));
  fireEvent.change(
    within(p).getByLabelText(`選択肢に無い答え: ${multi.question}`),
    { target: { value: "設定" } },
  );
  fireEvent.click(within(p).getByRole("button", { name: "送る" }));
  await waitFor(() =>
    expect(answers).toEqual([
      [7, { answers: { [multi.question]: "文書, 設定" } }],
    ]),
  );
});

it("問いが複数ある確認は、全部に答えるまで「送る」を押せず、まとめて 1 回で送る", async () => {
  chatBody = replyingWith({ prompts: [question([single, multi])] });
  await open();
  const p = await findPrompt();
  const send = within(p).getByRole("button", { name: "送る" });
  const other = within(p).getByLabelText(
    `選択肢に無い答え: ${single.question}`,
  );
  // 文を書いた後に選択肢を押すと、押した選択肢が答えになる（逆も同じ）
  fireEvent.change(other, { target: { value: "下書き" } });
  fireEvent.click(button(/案A/));
  expect(other).toHaveValue("");
  expect(button(/案A/)).toHaveAttribute("aria-pressed", "true");
  expect(answers).toEqual([]);
  expect(send).toBeDisabled();
  fireEvent.change(other, { target: { value: "案C" } });
  expect(button(/案A/)).toHaveAttribute("aria-pressed", "false");
  fireEvent.click(button(/案A/));
  fireEvent.click(within(p).getByRole("checkbox", { name: "文書" }));
  fireEvent.click(send);
  await waitFor(() =>
    expect(answers).toEqual([
      [7, { answers: { [single.question]: "案A", [multi.question]: "文書" } }],
    ]),
  );
});

// ---- 会話の一覧と続き ----

async function openList() {
  await open();
  await panel().findByText(/相談や指示/);
  fireEvent.click(button("会話の一覧"));
  await panel().findByTestId("chat-sessions");
}
const rows = () => panel().getAllByTestId("chat-session");

it("会話の一覧の各行に、始めた時刻・最初の発言の先頭・費用の合計・紐づいた課題の題名・「全文」が出る", async () => {
  sessions = [
    session({
      cost_usd: 1.5,
      challenges: [{ challenge_id: "C-3", title: "通知メールの再送制御" }],
    }),
  ];
  await openList();
  const r = within(rows()[0] as HTMLElement);
  expect(rows()[0]?.querySelector("time")).toHaveAttribute(
    "dateTime",
    "2026-11-24T09:00:00.000Z",
  );
  expect(r.getByText("仕様を作りたい")).toBeInTheDocument();
  expect(rows()[0]).toHaveTextContent("$1.50");
  expect(r.getByRole("link", { name: "通知メールの再送制御" })).toHaveAttribute(
    "href",
    "#/a/alpha/challenges/C-3",
  );
  expect(r.getByRole("link", { name: "全文" })).toHaveAttribute(
    "href",
    "#/a/alpha/chat/s-closed",
  );
});

it("「続ける」は閉じた会話の行に出て、開いている会話の行には出ない", async () => {
  sessions = [
    session({ id: "s-open", open: true, closed_at: null }),
    session(),
  ];
  await openList();
  const [openRow, closedRow] = rows() as [HTMLElement, HTMLElement];
  expect(within(openRow).queryByRole("button", { name: "続ける" })).toBeNull();
  expect(
    within(closedRow).getByRole("button", { name: "続ける" }),
  ).toBeEnabled();
});

it("全文のファイルが無い会話の行には「続ける」も「全文」も出ず、「全文はもう無い」が出る", async () => {
  sessions = [session({ transcript_available: false })];
  await openList();
  const r = within(rows()[0] as HTMLElement);
  expect(r.queryByRole("button", { name: "続ける" })).toBeNull();
  expect(r.queryByRole("link", { name: "全文" })).toBeNull();
  expect(r.getByText("全文はもう無い")).toBeInTheDocument();
});

it("「続ける」を押すと、その会話を続ける口を呼び、一覧を閉じて、その会話の記録を出す", async () => {
  sessions = [session()];
  await openList();
  fireEvent.click(button("続ける"));
  expect(await panel().findByText("続きの会話の発言")).toBeInTheDocument();
  expect(resumed).toEqual(["s-closed"]);
  expect(screen.queryByTestId("chat-sessions")).toBeNull();
});

it("返事が進行中の間は「続ける」を押せない", async () => {
  chatBody = replyingWith({});
  sessions = [session()];
  await open();
  await panel().findByRole("button", { name: "止める" });
  fireEvent.click(button("会話の一覧"));
  expect(await panel().findByRole("button", { name: "続ける" })).toBeDisabled();
  expect(button("止める")).toBeEnabled();
});

it("「続ける」が transcript_unavailable で断られると、一覧のまま理由を出す", async () => {
  sessions = [session()];
  await openList();
  resumeError = new ApiError("transcript_unavailable", "gone", 409);
  fireEvent.click(button("続ける"));
  expect(await panel().findByRole("alert")).toHaveTextContent(
    "この会話は全文のファイルが無いので、続けられません。",
  );
  expect(panel().getByTestId("chat-sessions")).toBeInTheDocument();
});

it("一覧は、もう一度ボタンを押すと閉じて、欄の記録に戻る。会話が無ければその旨を出す", async () => {
  await openList();
  expect(await panel().findByText("会話はまだありません。")).toBeVisible();
  fireEvent.click(button("会話の一覧を閉じる"));
  expect(screen.queryByTestId("chat-sessions")).toBeNull();
  expect(panel().getByText(/相談や指示/)).toBeInTheDocument();
});

it("一覧を開いたまま文を送ると、欄の記録に戻る", async () => {
  await openList();
  fireEvent.change(screen.getByLabelText("オーケストレーターへの文"), {
    target: { value: "こんにちは" },
  });
  fireEvent.click(button("送る"));
  await waitFor(() => expect(screen.queryByTestId("chat-sessions")).toBeNull());
});

// ---- 全文の画面 ----

it("一覧の「全文」を押すと全文の画面が開き、発言・返事・道具の使用と結果・切った旨を読み取りだけで出す", async () => {
  sessions = [session()];
  transcript = {
    entries: [
      {
        kind: "owner",
        text: "仕様を作りたい",
        tool: null,
        truncated: false,
        at: "2026-11-24T09:00:00.000Z",
      },
      {
        kind: "assistant",
        text: "# 方針です",
        tool: null,
        truncated: false,
        at: null,
      },
      {
        kind: "tool_use",
        text: '{"command":"ls"}',
        tool: "Bash",
        truncated: false,
        at: null,
      },
      {
        kind: "tool_result",
        text: "a.md",
        tool: "Bash",
        truncated: true,
        at: null,
      },
    ],
  };
  await openList();
  const link = panel().getByRole("link", { name: "全文" });
  await act(async () => {
    window.location.hash = link.getAttribute("href") as string;
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  });
  const t = await screen.findByTestId("chat-transcript");
  const entries = await within(t).findAllByTestId("transcript-entry");
  expect(transcriptAsked[0]).toBe("s-closed");
  expect(entries).toHaveLength(4);
  expect(entries[0]).toHaveTextContent("オーナー");
  expect(entries[0]).toHaveTextContent("仕様を作りたい");
  expect(
    within(entries[1] as HTMLElement).getByRole("heading", {
      name: "方針です",
    }),
  ).toBeInTheDocument();
  expect(entries[2]).toHaveTextContent("道具の使用: Bash");
  expect(entries[2]).toHaveTextContent('{"command":"ls"}');
  expect(entries[3]).toHaveTextContent("道具の結果: Bash");
  expect(entries[3]).toHaveTextContent("長いので、ここで切りました。");
  expect(entries[2]).not.toHaveTextContent("切りました");
  expect(
    t.querySelectorAll("button, input, textarea, select, form"),
  ).toHaveLength(0);
});

it("全文が読めないとき、全文の画面は、その旨と、発言と返事は欄の記録で読めることを出す", async () => {
  transcript = new ApiError("transcript_unavailable", "gone", 404);
  await open("#/a/alpha/chat/s-gone");
  const t = await screen.findByTestId("chat-transcript-unavailable");
  expect(t).toHaveTextContent("全文を読めません。");
  expect(t).toHaveTextContent("Claude Code が古い会話を消した");
  expect(t).toHaveTextContent("発言と返事は、欄の記録で読めます。");
});

it("知らない会話の全文の画面は「見つかりません」になる", async () => {
  transcript = new ApiError("not_found", "no such session", 404);
  await open("#/a/alpha/chat/nothing");
  expect(await screen.findByText("見つかりません。")).toBeInTheDocument();
});

// ---- 課題の詳細 ----

it("課題の詳細の費用の欄に、仕様づくりの額と、実装の予算に数えない旨が出る", async () => {
  detail = { spec_cost_usd: 0.4 };
  await open("#/a/alpha/challenges/C-3");
  const spec = await screen.findByTestId("spec-cost");
  expect(spec).toHaveTextContent("仕様づくり $0.40");
  expect(spec).toHaveTextContent("実装の予算には数えません");
  expect(spec.closest("dd")).toContainElement(screen.getByTestId("cost"));
});

it("課題の詳細に、紐づいた会話の一覧（始めた時刻・最初の発言の先頭・この課題に数えた費用・「全文」）が出る", async () => {
  detail = {
    chat_sessions: [
      {
        id: "s-1",
        started_at: "2026-11-24T09:00:00.000Z",
        first_message: "仕様を作りたい",
        cost_usd: 0.42,
      },
    ],
  };
  await open("#/a/alpha/challenges/C-3");
  const box = await screen.findByTestId("detail-chat-sessions");
  expect(box.querySelector("time")).toHaveAttribute(
    "dateTime",
    "2026-11-24T09:00:00.000Z",
  );
  expect(box).toHaveTextContent("仕様を作りたい");
  expect(box).toHaveTextContent("この課題に数えた費用 $0.42");
  expect(within(box).getByRole("link", { name: "全文" })).toHaveAttribute(
    "href",
    "#/a/alpha/chat/s-1",
  );
});

it("紐づいた会話が無い課題の詳細には、会話の区画を出さない", async () => {
  detail = { chat_sessions: [] };
  await open("#/a/alpha/challenges/C-3");
  await screen.findByTestId("spec-cost");
  expect(screen.queryByTestId("detail-chat-sessions")).toBeNull();
});

// ---- 新しい確認を見える位置へ送る（flywheel#1303） ----

const changed = () =>
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );

function spyScroll() {
  const scrolled: (string | undefined)[] = [];
  const proto = Element.prototype as { scrollIntoView?: unknown };
  const before = Object.getOwnPropertyDescriptor(proto, "scrollIntoView");
  proto.scrollIntoView = function (this: HTMLElement) {
    scrolled.push(this.dataset.promptId);
  };
  return {
    scrolled,
    restore: () => {
      if (before) Object.defineProperty(proto, "scrollIntoView", before);
      else Reflect.deleteProperty(proto, "scrollIntoView");
    },
  };
}

it("答えを待つ確認が新しく出ると、その確認を欄の見える位置へ送る。読み直しただけでは送り直さない", async () => {
  const spy = spyScroll();
  try {
    chatBody = replyingWith({ body: "調べています" });
    await open();
    await panel().findByText("調べています");
    expect(spy.scrolled).toEqual([]);

    chatBody = replyingWith({ prompts: [permission({ id: 7 })] });
    changed();
    await findPrompt();
    await waitFor(() => expect(spy.scrolled).toEqual(["7"]));

    // 同じ確認のまま読み直しても、送り直さない
    chatBody = replyingWith({
      body: "追記",
      prompts: [permission({ id: 7 })],
    });
    changed();
    await panel().findByText("追記");
    expect(spy.scrolled).toEqual(["7"]);

    // 答え終えた確認へは送らず、次に出た確認へ送る
    chatBody = replyingWith({
      prompts: [permission({ id: 7, state: "denied" }), permission({ id: 9 })],
    });
    changed();
    await waitFor(() => expect(spy.scrolled).toEqual(["7", "9"]));
  } finally {
    spy.restore();
  }
});

it("欄を開いたときに答えを待つ確認があれば、その確認を見える位置へ送る", async () => {
  const spy = spyScroll();
  try {
    chatBody = replyingWith({ prompts: [permission({ id: 7 })] });
    await open();
    await findPrompt();
    await waitFor(() => expect(spy.scrolled).toEqual(["7"]));
  } finally {
    spy.restore();
  }
});

it("会話の一覧を開いている間に出た確認は、一覧を閉じたときに見える位置へ送る", async () => {
  const spy = spyScroll();
  try {
    chatBody = replyingWith({ body: "調べています" });
    await open();
    await panel().findByText("調べています");
    fireEvent.click(button("会話の一覧"));
    chatBody = replyingWith({
      body: "確認します",
      prompts: [permission({ id: 7 })],
    });
    changed();
    await waitFor(() =>
      expect(
        screen.getByTestId("chat-panel").querySelector('[data-prompt-id="7"]'),
      ).not.toBeNull(),
    );
    expect(spy.scrolled).toEqual([]);
    fireEvent.click(button("会話の一覧を閉じる"));
    await waitFor(() => expect(spy.scrolled).toEqual(["7"]));
  } finally {
    spy.restore();
  }
});
