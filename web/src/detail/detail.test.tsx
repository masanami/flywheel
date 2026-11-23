import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type {
  Followup,
  Hold,
  ShowCardResponse,
  SupervisorAction,
  Task,
  TaskAddition,
  TreeTask,
} from "../api/types.gen.ts";
import { type MermaidLike, setMermaidLoader } from "../parts/loadMermaid.ts";
import { bodies, show } from "../testing/detailFixture.ts";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";
import { shortTime } from "../when.ts";

// 課題の詳細の画面（#808）。完了条件の 1 つずつを、名前の AC-<番号> が直接確かめる。
// ブラウザの試験（AC-104・AC-105・AC-431・AC-436）は web/e2e/detail.spec.ts にある。

beforeEach(() => {
  const m = document.createElement("meta");
  m.name = "flywheel-csrf";
  m.content = "tok";
  document.head.append(m);
});
afterEach(() => {
  vi.unstubAllGlobals();
  setMermaidLoader();
  for (const m of document.head.querySelectorAll("meta[name=flywheel-csrf]"))
    m.remove();
  cleanup();
  Reflect.deleteProperty(HTMLElement.prototype, "scrollHeight");
  Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
});

const BASE = "/api/v1/workspaces/alpha";
const XSS = "<script>alert(1)</script>";

const task = (p: Partial<TreeTask> & { key: string }): TreeTask => ({
  cost_usd: 0,
  depends_on: [],
  id: null,
  phase: "next",
  progress: null,
  status: null,
  title: p.key,
  ...p,
});

let holdSeq = 0;
const hold = (p: Partial<Hold> = {}): Hold => ({
  hold_id: `H-${++holdSeq}`,
  scope: "challenge",
  answer: null,
  answer_option: null,
  answered_at: null,
  answered_by: null,
  answered_channel: null,
  answered_verification: null,
  closed_at: null,
  from_status: "in_progress",
  from_status_label: "着手中",
  kind: "human",
  options: [],
  question: "どちらにしますか",
  raised_at: "2026-11-01T00:00:00Z",
  relay_quote: null,
  relay_session: null,
  replan: false,
  run_id: null,
  stop_kind: null,
  supervisor_findings: null,
  supervisor_recommendation: null,
  supervisor_state: null,
  task_id: null,
  ...p,
});

const followup = (id: string, status: string, title: string): Followup => ({
  challenge_id: "C-1",
  created_at: "2026-11-01T00:00:00Z",
  detail: "",
  id,
  note: null,
  promoted_challenge_id: null,
  promoted_task_id: null,
  reason: null,
  source: "learn",
  status,
  title,
  updated_at: "2026-11-01T00:00:00Z",
});

async function open(
  detail: ShowCardResponse,
  opts: { hash?: string; extra?: Record<string, unknown> } = {},
) {
  window.location.hash = opts.hash ?? "#/a/alpha/challenges/C-1";
  const ctx = installFakes(bodies(detail, opts.extra));
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const root = await screen.findByTestId("challenge-detail");
  return { root, ...ctx };
}

const q = (root: HTMLElement) => within(root);

it("AC-91 #808: 課題の詳細の画面では、上の帯の「課題」に選んでいる印が付く", async () => {
  await open(show());
  const nav = screen.getByRole("navigation", { name: "画面" });
  expect(
    within(nav)
      .getByRole("link", { name: "課題" })
      .getAttribute("aria-current"),
  ).toBe("page");
  expect(
    within(nav)
      .getByRole("link", { name: "いま進められること" })
      .getAttribute("aria-current"),
  ).toBeNull();
});

it("AC-107 #808: 課題の詳細の API が not_found なら、「見つかりません」と「いま進められること」へのリンクが出る", async () => {
  window.location.hash = "#/a/alpha/challenges/C-9";
  installFakes({
    ...fixtures(),
    [`${BASE}/challenges/C-9`]: () =>
      new Response(
        JSON.stringify({ error: { code: "not_found", message: "" } }),
        {
          status: 404,
        },
      ),
  });
  render(<App />);
  act(() => FakeEventSource.latest.open());
  expect(await screen.findByText("見つかりません。")).toBeInTheDocument();
  expect(
    screen.getByRole("link", { name: "「いま進められること」へ" }),
  ).toHaveAttribute("href", "#/a/alpha/now");
});

it("AC-110 #808: 課題の詳細の API の応答が返るまで、「見つかりません」は出ない", async () => {
  window.location.hash = "#/a/alpha/challenges/C-1";
  installFakes(bodies(show()));
  const real = globalThis.fetch;
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => {
    release = r;
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      if (String(input) === `${BASE}/challenges/C-1`) await gate;
      return real(input, init);
    }),
  );
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await screen.findByText("読み込んでいます");
  await new Promise((r) => setTimeout(r, 30));
  expect(screen.queryByText("見つかりません。")).toBeNull();
  expect(screen.getByText("読み込んでいます")).toBeInTheDocument();
  release();
  await screen.findByTestId("challenge-detail");
});

it("AC-113 #808: 課題の詳細を開いている間に、そのエージェントの変化の合図を受けると、課題の詳細の API がもう 1 回呼ばれる", async () => {
  const { requests } = await open(show());
  const calls = () =>
    requests.filter((r) => r.url === `${BASE}/challenges/C-1`).length;
  expect(calls()).toBe(1);
  // ほかのエージェントの合図では読み直さない
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "beta",
    }),
  );
  await new Promise((r) => setTimeout(r, 30));
  expect(calls()).toBe(1);
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(calls()).toBe(2));
});

it("AC-223 #808: 課題の詳細の画面に、その課題の題名と status_label が出る", async () => {
  const { root } = await open(
    show({ challenge: { status_label: "着手中（ラベル）" } }),
  );
  expect(
    q(root).getByRole("heading", { name: "請求の集計を直す" }),
  ).toBeInTheDocument();
  expect(q(root).getByTestId("detail-status")).toHaveTextContent(
    "着手中（ラベル）",
  );
});

const binding = (url = "https://example.com/issues/1") => ({
  comments_count: 0,
  created_at: "",
  external_key: "ext-1",
  fingerprint: "",
  policy_state: "in_policy",
  read_comments_count: 0,
  read_upstream_updated_at: null,
  source_id: "gh",
  updated_at: "",
  upstream_state: "open",
  upstream_updated_at: null,
  url,
});
const child = (key: string, title: string, state = "open") => ({
  external_key: key,
  parent_external_key: "ext-1",
  title,
  upstream_state: state,
  url: `https://example.com/issues/${key}`,
});

it("AC-224 #808: 取り込み元との対応がある課題の詳細に、上流へのリンクが出る", async () => {
  const { root } = await open(show({ source_binding: binding() }));
  const link = q(root).getByRole("link", { name: "上流を開く" });
  expect(link).toHaveAttribute("href", "https://example.com/issues/1");
});

it("AC-224 #808: 上流の行き先が http(s) でなければリンクにしない", async () => {
  const { root } = await open(
    show({ source_binding: binding("javascript:alert(1)") }),
  );
  expect(q(root).getByRole("link", { name: "上流を開く" })).toHaveAttribute(
    "href",
    "#",
  );
});

it("AC-225 #808: 取り込み元の子が 2 件ある課題の詳細の、課題の項目に、子の件数の 2 が出る", async () => {
  const { root } = await open(
    show({
      source_binding: binding(),
      source_children: [child("c1", "子 1"), child("c2", "子 2")],
    }),
  );
  expect(q(root).getByTestId("source-children-count")).toHaveTextContent("2");
});

it("AC-226 #808: uses_integration が偽の課題の詳細に、統合先の「使わない」が出る", async () => {
  const { root } = await open(show({ tree: { uses_integration: false } }));
  expect(q(root).getByTestId("detail-integration")).toHaveTextContent(
    "使わない",
  );
});

it("AC-231 #808: tree の uses_integration が null の課題の詳細に、統合先は出ない", async () => {
  const { root } = await open(show({ tree: { uses_integration: null } }));
  expect(q(root).queryByText("統合先")).toBeNull();
});

it("AC-227 #808: 課題の詳細に、tree の進み（tasks_done と tasks_total）が出る", async () => {
  const { root } = await open(
    show({ tree: { tasks_done: 2, tasks_total: 5 } }),
  );
  expect(q(root).getByTestId("tasks-done")).toHaveTextContent("2");
  expect(q(root).getByTestId("tasks-total")).toHaveTextContent("5");
});

it("AC-228 #808: 課題の詳細に、tree の cost_usd が出る", async () => {
  const { root } = await open(show({ tree: { cost_usd: 11.2 } }));
  expect(q(root).getByTestId("cost")).toHaveTextContent("11.2");
});

it("AC-229 #808: tree の budget_usd が 30 の課題の詳細に、予算の上限の 30 が出る", async () => {
  const { root } = await open(show({ tree: { budget_usd: 30 } }));
  expect(q(root).getByText("予算の上限")).toBeInTheDocument();
  expect(q(root).getByTestId("budget")).toHaveTextContent("30");
});

it("AC-230 #808: tree の budget_usd が null の課題の詳細に、予算の上限は出ない", async () => {
  const { root } = await open(show({ tree: { budget_usd: null } }));
  expect(q(root).queryByText("予算の上限")).toBeNull();
  expect(q(root).queryByTestId("budget")).toBeNull();
});

it("AC-232 #808: 課題の詳細に、タスクのツリーが出る（開いた直後は「ツリー」を選んでいる）", async () => {
  const { root } = await open(
    show({
      tree: {
        tasks: [
          task({ key: "a", title: "調べる", id: "T-1", phase: "done" }),
          task({ key: "b", title: "直す", id: "T-2", depends_on: ["a"] }),
        ],
      },
    }),
  );
  expect(q(root).getByTestId("task-tree")).toBeInTheDocument();
  expect(q(root).getAllByTestId("task-box")).toHaveLength(2);
  expect(q(root).getByRole("link", { name: "ツリー" })).toHaveAttribute(
    "aria-current",
    "page",
  );
});

it("AC-233 #808: approve_plan がある課題の詳細の、あなたを待っているものの区画に、計画の承認の画面へのリンクが出る", async () => {
  const { root } = await open(show({ actions: ["approve_plan"] }));
  const w = q(root).getByTestId("detail-waiting");
  expect(
    within(w).getByRole("link", { name: "計画の承認の画面を開く" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-1/approve-plan");
});

const summary = {
  approval: {
    challenge_id: "C-1",
    conditions_after_release: [],
    conditions_after_release_heading: "",
    direct_completion: null,
    done_criteria: "要約の完了条件",
    kind: "completion",
    mergeability_note: "",
    other_pending_operations: [],
    pairs: [],
    plan: null,
    release_held: true,
    releases: [],
    stages: {
      actual: null,
      expected: null,
      figure: null,
      issues: null,
      issues_answer: null,
      non_goals: null,
    },
    title: "請求の集計を直す",
    version: 7,
  },
};

it("AC-234 #808: approve_completion がある課題の詳細に、完了承認画面へのリンクが出て、要約は出ない（S3d）", async () => {
  const { root, requests } = await open(
    show({ actions: ["approve_completion"] }),
    {
      extra: { [`${BASE}/challenges/C-1/approval`]: summary },
    },
  );
  const w = q(root).getByTestId("detail-waiting");
  expect(
    within(w).getByRole("link", { name: "完了の承認の画面を開く" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-1/approve-completion");
  expect(q(root).queryByTestId("approval-summary")).toBeNull();
  expect(requests.some((r) => r.url.endsWith("/approval"))).toBe(false);
});

it("AC-235 #808: approve_completion がある課題の詳細に、承認のボタンは無い（S3d）", async () => {
  const { root } = await open(show({ actions: ["approve_completion"] }), {
    extra: { [`${BASE}/challenges/C-1/approval`]: summary },
  });
  await q(root).findByTestId("detail-waiting");
  expect(
    q(root).queryByRole("button", { name: /承認/, hidden: true }),
  ).toBeNull();
});

it("AC-236 #808: answer_question がある課題の詳細に、その保留の問いの文が出る", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [
        hold({ question: "どちらにしますか" }),
        hold({
          question: "答え済みの問い",
          answer: "A",
          raised_at: "2026-10-01T00:00:00Z",
        }),
      ],
    }),
  );
  const w = q(root).getByTestId("detail-waiting");
  expect(within(w).getByText("どちらにしますか")).toBeInTheDocument();
  expect(within(w).queryByText("答え済みの問い")).toBeNull();
});

it("AC-125・AC-126 S4c-3: 開いている保留が 2 つの課題の詳細の待ちに、保留ごとの ID と --hold つきの案内が出る", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [
        hold({ hold_id: "H-3", scope: "task", question: "a への問い" }),
        hold({
          hold_id: "H-4",
          scope: "task",
          question: "b への問い",
          raised_at: "2026-11-02T00:00:00Z",
        }),
      ],
    }),
  );
  const w = within(q(root).getByTestId("detail-waiting"));
  const ids = w.getAllByTestId("hold-id").map((e) => e.textContent);
  expect(ids.join(" ")).toContain("H-3");
  expect(ids.join(" ")).toContain("H-4");
  expect(w.getByText(/--hold H-3 /)).toBeInTheDocument();
  expect(w.getByText(/--hold H-4 /)).toBeInTheDocument();
});

it("S4c-3: 開いている保留が 1 つの課題の案内は、--hold を含まない", async () => {
  const { root } = await open(
    show({ actions: ["answer_question"], holds: [hold()] }),
  );
  const w = within(q(root).getByTestId("detail-waiting"));
  expect(w.queryByText(/--hold/)).toBeNull();
});

it("#808（旧詳細の移し）: 取り下げで回答なしに閉じた保留は、あなたを待っているものに出ない", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [
        hold({ question: "開いた問い" }),
        hold({ question: "閉じた問い", closed_at: "2026-02-03T00:00:00Z" }),
      ],
    }),
  );
  const w = q(root).getByTestId("detail-waiting");
  expect(within(w).getByText("開いた問い")).toBeInTheDocument();
  expect(within(w).queryByText("閉じた問い")).toBeNull();
});

it("#808（旧詳細の移し）: 外部由来の文字列（題名・説明・計画の本文・保留の問い）は文字として出て、script・img 要素と onerror 属性ができない", async () => {
  const EVIL = `${XSS}<img src=x onerror=alert(1)>`;
  const { root } = await open(
    show({
      actions: ["answer_question"],
      challenge: { title: EVIL, description: EVIL },
      holds: [hold({ question: EVIL })],
      plans: [plan(1, EVIL)],
    }),
    { hash: PLAN_HASH },
  );
  await q(root).findByTestId("plan-body");
  expect(document.body.textContent).toContain("onerror=alert(1)");
  expect(document.body.querySelector("script")).toBeNull();
  expect(document.body.querySelector("img")).toBeNull();
  expect(document.body.querySelector("[onerror]")).toBeNull();
});

it("AC-237 #808: 保留がタスクに付いているとき、そのタスクの題名と、タスクの詳細へのリンクが出る", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [hold({ task_id: "T-9" })],
      tree: { tasks: [task({ key: "x", id: "T-9", title: "決済の移行" })] },
    }),
  );
  const w = q(root).getByTestId("detail-waiting");
  expect(within(w).getByRole("link", { name: "決済の移行" })).toHaveAttribute(
    "href",
    "#/a/alpha/challenges/C-1/tasks/T-9",
  );
});

it("AC-238 #808: 回答を待っている課題の詳細に、ターミナルのコマンドの案内（flywheel answer）が出る。答える欄は置かない", async () => {
  const { root } = await open(
    show({ actions: ["answer_question"], holds: [hold()] }),
  );
  const g = q(root).getByRole("region", { name: "承認方法の案内" });
  expect(g).toHaveTextContent(
    "flywheel answer C-1 --workspace '/w/alpha' --answer <回答>",
  );
  expect(root.querySelector("textarea, input, form")).toBeNull();
});

it("AC-239 #808: increase_budget がある課題の詳細に、ターミナルのコマンドの案内（flywheel budget）が出る", async () => {
  const { root } = await open(show({ actions: ["increase_budget"] }));
  expect(
    q(root).getByRole("region", { name: "承認方法の案内" }),
  ).toHaveTextContent(
    "flywheel budget C-1 --workspace '/w/alpha' --impl-usd <額>",
  );
});

it("AC-240 #808: next_human_actions が空の課題の詳細に、「あなたを待っているものはありません。」が出る", async () => {
  const { root } = await open(show({ actions: [] }));
  expect(
    q(root).getByText("あなたを待っているものはありません。"),
  ).toBeInTheDocument();
});

it("AC-241 #808: parent_direct の保留で止まっている課題の詳細に、オーナーが答える問いではない旨が出る", async () => {
  const { root } = await open(
    show({
      holds: [hold({ kind: "parent_direct", question: "親が直接やる" })],
    }),
  );
  expect(q(root).getByTestId("parent-direct")).toHaveTextContent(
    "オーナーが答える問いではありません",
  );
  expect(q(root).queryByText("親が直接やる")).toBeNull();
});

it("AC-411 #808: 保留の問いの **太字** が、強調の要素になる", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [hold({ question: "これは **重要** な問い" })],
    }),
  );
  const strong = q(root).getByTestId("hold-question").querySelector("strong");
  expect(strong?.textContent).toBe("重要");
});

const followupsShow = () =>
  show({
    followups: [
      followup("F-1", "open", "ログの整理"),
      followup("F-2", "open", "文言の見直し"),
      followup("F-3", "done", "済んだ項目"),
    ],
    tree: { followups_open: 2 },
  });

it("AC-242 #808: 未のフォローアップが 2 件、済みが 1 件の課題の詳細の、フォローアップの区画に、未の 2 と、済みの 1 が出る", async () => {
  const { root } = await open(followupsShow());
  const f = q(root).getByTestId("detail-followups");
  expect(within(f).getByTestId("followups-open")).toHaveTextContent("2");
  expect(within(f).getByTestId("followups-done")).toHaveTextContent("1");
});

it("AC-243 #808: フォローアップの区画に、未のフォローアップの題名が出る（済みの題名は出さない）", async () => {
  const { root } = await open(followupsShow());
  const f = q(root).getByTestId("detail-followups");
  expect(within(f).getByText("ログの整理")).toBeInTheDocument();
  expect(within(f).getByText("文言の見直し")).toBeInTheDocument();
  expect(within(f).queryByText("済んだ項目")).toBeNull();
});

it("AC-244 #808: フォローアップの区画に、その課題で絞ったフォローアップの一覧へのリンクが出る", async () => {
  const { root } = await open(followupsShow());
  const f = q(root).getByTestId("detail-followups");
  expect(
    within(f).getByRole("link", { name: /フォローアップの一覧/ }),
  ).toHaveAttribute("href", "#/a/alpha/followups?challenge=C-1");
});

it("AC-245 #808: フォローアップが 1 件も無い課題の詳細に、フォローアップの区画は出ない", async () => {
  const { root } = await open(show({ followups: [] }));
  expect(q(root).queryByTestId("detail-followups")).toBeNull();
});

const withChildren = () =>
  show({
    source_binding: binding(),
    source_children: [
      child("c1", "上流の子 1", "open"),
      child("c2", "上流の子 2", "closed"),
    ],
    tasks: [
      {
        added: null,
        body: "",
        budget_impl_usd: 0,
        budget_review_usd: 0,
        artifacts: [],
        delegate: null,
        facts: {},
        landing: null,
        verification: null,
        depends_on: [],
        done_criteria: "",
        id: "T-1",
        non_goals: [],
        operation: "",
        plan_version: 1,
        repo: "",
        source_key: "c1",
        spent_usd: 0,
        status: "running",
        title: "子 1 を実装するタスク",
      },
    ],
  });

it("AC-246 #808: 取り込み元の子がある課題の詳細に、子の題名（上流へのリンク）と、上流の状態が出る", async () => {
  const { root } = await open(withChildren());
  const s = q(root).getByTestId("detail-source-children");
  expect(within(s).getByRole("link", { name: "上流の子 1" })).toHaveAttribute(
    "href",
    "https://example.com/issues/c1",
  );
  expect(within(s).getByText("開いている")).toBeInTheDocument();
  expect(within(s).getByText("閉じている")).toBeInTheDocument();
});

it("AC-247 #808: 取り込み元の子に対応するタスクがあるとき、その子の行に、タスクの題名が出る", async () => {
  const { root } = await open(withChildren());
  const s = q(root).getByTestId("detail-source-children");
  const rows = within(s).getAllByRole("listitem");
  expect(rows[0]).toHaveTextContent("子 1 を実装するタスク");
  expect(rows[1]).not.toHaveTextContent("タスク:");
});

it("AC-248 #808: 取り込み元の子が無い課題の詳細に、取り込み元の子の区画は出ない", async () => {
  const { root } = await open(show({ source_children: [] }));
  expect(q(root).queryByTestId("detail-source-children")).toBeNull();
});

const rel = (id: string, title: string, label = "着手中") => ({
  challenge_id: id,
  status: "in_progress",
  status_label: label,
  title,
});

it("AC-249 #808: 前提の課題がある課題の詳細の、関係する課題の区画に、前提の課題の題名と状態が出る", async () => {
  const { root } = await open(
    show({
      tree: {
        dependencies: [
          { ...rel("C-5", "ビルドの高速化", "着手中"), satisfied: false },
        ],
      },
    }),
  );
  const r = q(root).getByTestId("detail-related");
  const link = within(r).getByRole("link", { name: "ビルドの高速化" });
  expect(link).toHaveAttribute("href", "#/a/alpha/challenges/C-5");
  expect(within(r).getByText("着手中")).toBeInTheDocument();
  expect(within(r).getByText("未解消")).toBeInTheDocument();
  expect(within(r).queryByText("未完了")).toBeNull();
});

it("AC-250 #808: この課題を前提にしている課題があるとき、関係する課題の区画に、その課題が出る", async () => {
  const { root } = await open(show({ challenge: { dependents: ["C-2"] } }));
  const r = q(root).getByTestId("detail-related");
  expect(
    within(r).getByRole("link", { name: "後から続く課題" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-2");
});

it("AC-251 #808: 親の課題がある課題の詳細の、関係する課題の区画に、親の課題の題名が出る", async () => {
  const { root } = await open(
    show({ tree: { parent: rel("C-8", "親の課題") } }),
  );
  expect(
    within(q(root).getByTestId("detail-related")).getByRole("link", {
      name: "親の課題",
    }),
  ).toBeInTheDocument();
});

it("AC-252 #808: 子の課題がある課題の詳細の、関係する課題の区画に、子の課題の題名が出る", async () => {
  const { root } = await open(
    show({ tree: { children: [rel("C-9", "子の課題")] } }),
  );
  expect(
    within(q(root).getByTestId("detail-related")).getByRole("link", {
      name: "子の課題",
    }),
  ).toBeInTheDocument();
});

it("AC-253 #808: 前提・親・子のどれも無く、この課題を前提にしている課題も無いとき、関係する課題の区画は出ない", async () => {
  const { root } = await open(show());
  expect(q(root).queryByTestId("detail-related")).toBeNull();
});

it("AC-254 #808: 不可逆操作がある課題の詳細に、その種類と状態が出る", async () => {
  const op = {
    approved_head: null,
    challenge_id: "C-1",
    closed_at: null,
    closed_reason: null,
    consecutive_failures: 0,
    created_at: "",
    executed_at: null,
    id: "O-1",
    kind: "release",
    last_execution_detail: null,
    last_execution_result: null,
    merge_commit: null,
    ref: null,
    state: "pending",
    summary: "本番へ反映する",
    superseded_by: null,
    supersedes: null,
    version: 1,
    waiting_since: null,
  };
  const { root } = await open(show({ operations: [op] }));
  const o = q(root).getByTestId("detail-operations");
  expect(o).toHaveTextContent("反映 / 承認待ち");
  expect(o).not.toHaveTextContent("release");
  expect(o).not.toHaveTextContent("pending");
  expect(o).toHaveTextContent("本番へ反映する");
});

it("#933: 不可逆操作の状態 closed は「終了」と出て、closed は出ない", async () => {
  const { root } = await open(
    show({
      operations: [
        {
          approved_head: null,
          challenge_id: "C-1",
          closed_at: null,
          closed_reason: null,
          consecutive_failures: 0,
          created_at: "",
          executed_at: null,
          id: "O-2",
          kind: "delete",
          last_execution_detail: null,
          last_execution_result: null,
          merge_commit: null,
          ref: null,
          state: "closed",
          summary: "古い資料を消す",
          superseded_by: null,
          supersedes: null,
          version: 1,
          waiting_since: null,
        },
      ],
    }),
  );
  const o = q(root).getByTestId("detail-operations");
  expect(o).toHaveTextContent("削除 / 終了");
  expect(o).not.toHaveTextContent("closed");
  expect(o).toHaveTextContent("古い資料を消す");
});

it("#798: 不可逆操作の行に、参照（ref）があるときは文字で添える（リンクにしない）。無いときは出さない", async () => {
  const op = {
    approved_head: null,
    challenge_id: "C-1",
    closed_at: null,
    closed_reason: null,
    consecutive_failures: 0,
    created_at: "",
    executed_at: null,
    id: "O-1",
    kind: "release",
    last_execution_detail: null,
    last_execution_result: null,
    merge_commit: null,
    ref: "https://example.invalid/pull/3",
    state: "pending",
    summary: "本番へ反映する",
    superseded_by: null,
    supersedes: null,
    version: 1,
    waiting_since: null,
  };
  const { root } = await open(
    show({ operations: [op, { ...op, id: "O-2", ref: null }] }),
  );
  const o = q(root).getByTestId("detail-operations");
  const rows = o.querySelectorAll("li");
  expect(rows).toHaveLength(2);
  expect(rows[0]).toHaveTextContent("参照: https://example.invalid/pull/3");
  expect(rows[0]?.querySelector("a")).toBeNull();
  expect(rows[1]).not.toHaveTextContent("参照");
});

it("AC-273 #808: 「計画の本文」を押すと、URL のハッシュが #/a/<名前>/challenges/<C>/plan になる", async () => {
  const { root } = await open(show());
  fireEvent.click(q(root).getByRole("link", { name: "計画の本文" }));
  await waitFor(() =>
    expect(window.location.hash).toBe("#/a/alpha/challenges/C-1/plan"),
  );
  expect(q(root).getByTestId("detail-plan-view")).toBeInTheDocument();
});

const plan = (version: number, body: string) => ({
  body,
  created_at: "2026-11-01T00:00:00Z",
  spec: null,
  version,
});
const PLAN_HASH = "#/a/alpha/challenges/C-1/plan";

it("AC-274 #808: 「計画の本文」に、最新の版の計画の本文が、Markdown として整って出る（## 方針 が見出しの要素になる）", async () => {
  const { root } = await open(
    show({ plans: [plan(1, "古い本文"), plan(2, "## 方針\n\n新しい本文")] }),
    { hash: PLAN_HASH },
  );
  const p = q(root).getByTestId("plan-body");
  expect(within(p).getByRole("heading", { name: "方針" })).toBeInTheDocument();
  expect(within(p).getByText("新しい本文")).toBeInTheDocument();
  expect(within(p).queryByText("古い本文")).toBeNull();
});

it("AC-275 #808: 「計画の本文」に、課題の説明が出る", async () => {
  const { root } = await open(show(), { hash: PLAN_HASH });
  expect(q(root).getByTestId("detail-description")).toHaveTextContent(
    "説明の本文",
  );
});

it("AC-276 #808: 「計画の本文」に、課題の完了条件が出る", async () => {
  const { root } = await open(show(), { hash: PLAN_HASH });
  expect(q(root).getByTestId("detail-done-criteria")).toHaveTextContent(
    "完了条件の本文",
  );
});

it("#814 掃除 9: 計画の本文の版の日時は shortTime の書き方で出て、正確な値は time 要素の dateTime に持つ", async () => {
  const { root } = await open(
    show({
      plans: [{ ...plan(1, "本文"), created_at: "2026-11-16T11:09:22.711Z" }],
    }),
    { hash: PLAN_HASH },
  );
  const t = within(q(root).getByTestId("plan-body")).getByText(
    shortTime("2026-11-16T11:09:22.711Z"),
  );
  expect(t.tagName).toBe("TIME");
  expect(t).toHaveAttribute("datetime", "2026-11-16T11:09:22.711Z");
  expect(q(root).getByTestId("plan-body")).not.toHaveTextContent(
    "2026-11-16T11:09:22.711Z",
  );
});

it("AC-277 #808: 計画が 2 つの版を持つとき、前の版を選ぶと、前の版の本文が出る", async () => {
  const { root } = await open(
    show({ plans: [plan(1, "古い本文"), plan(2, "新しい本文")] }),
    { hash: PLAN_HASH },
  );
  const p = q(root).getByTestId("plan-body");
  expect(within(p).getByText("新しい本文")).toBeInTheDocument();
  fireEvent.click(within(p).getByRole("button", { name: "v1" }));
  expect(within(p).getByText("古い本文")).toBeInTheDocument();
  expect(within(p).queryByText("新しい本文")).toBeNull();
});

it("AC-278 #808: 計画が無い課題の「計画の本文」に、説明と完了条件が出る", async () => {
  const { root } = await open(show({ plans: [] }), { hash: PLAN_HASH });
  expect(q(root).queryByTestId("plan-body")).toBeNull();
  expect(q(root).getByTestId("detail-description")).toHaveTextContent(
    "説明の本文",
  );
  expect(q(root).getByTestId("detail-done-criteria")).toHaveTextContent(
    "完了条件の本文",
  );
});

const stagePair = {
  expected: {
    kind: "ui",
    scene: "決済の画面",
    before: "前の見え方",
    after: "予想の見え方",
  },
  actual: { scene: "決済の画面（実物）", after: "実物の見え方" },
};
const stagesOf = () => ({
  actual: [stagePair.actual],
  expected: [stagePair.expected],
  figure: null,
  issues: null,
  issues_answer: null,
  non_goals: null,
});

it("AC-279 #808: 予想（stages.expected）がある課題の「計画の本文」に、予想の場面が出る", async () => {
  const { root } = await open(
    show({ stages: stagesOf(), pairs: [stagePair] }),
    {
      hash: PLAN_HASH,
    },
  );
  // 「決済の画面」は実物の場面（「決済の画面（実物）」）にも含まれるので、予想の側にだけある文字で確かめる。
  const expected = q(root)
    .getByTestId("stages-detail")
    .querySelector('[data-field="expected"]');
  expect(expected).toHaveTextContent("決済の画面");
  expect(expected).toHaveTextContent("予想の見え方");
  // #964: 種類は英語のコードのまま出さず、語で出す
  expect(expected).toHaveTextContent("予想（画面・決済の画面）");
  expect(expected).not.toHaveTextContent("予想（ui");
});

it("AC-280 #808: 実物（stages.actual）がある課題の「計画の本文」に、実物の場面が出る", async () => {
  const { root } = await open(
    show({ stages: stagesOf(), pairs: [stagePair] }),
    {
      hash: PLAN_HASH,
    },
  );
  expect(q(root).getByTestId("stages-detail")).toHaveTextContent(
    "決済の画面（実物）",
  );
});

it("AC-281 #808: 課題の完了条件の **太字** が、強調の要素になる", async () => {
  const { root } = await open(
    show({ challenge: { done_criteria: "これは **大事** な条件" } }),
    {
      hash: PLAN_HASH,
    },
  );
  expect(
    q(root).getByTestId("detail-done-criteria").querySelector("strong")
      ?.textContent,
  ).toBe("大事");
});

it("AC-410 #808: 課題の説明の **太字** が、強調の要素になる。生の HTML は文字のまま出る", async () => {
  const { root } = await open(
    show({ challenge: { description: `説明は **太い** ${XSS}` } }),
    { hash: PLAN_HASH },
  );
  const d = q(root).getByTestId("detail-description");
  expect(d.querySelector("strong")?.textContent).toBe("太い");
  expect(d.querySelector("script")).toBeNull();
  expect(d.textContent).toContain(XSS);
});

// AC-282 #808 は「相談の口は無い」だった。S4d-1（#1002）の AC-7 で口を置くことになったため、反転した。
it("AC-282 #808 → S4d-1 AC-7: 課題の詳細に、「この課題について相談する」の口がある（欄の開閉は chat の試験）", async () => {
  const { root } = await open(show());
  expect(
    q(root).getByRole("button", { name: "この課題について相談する" }),
  ).toBeInTheDocument();
});

it("AC-283 #808: 課題の詳細に、スーパーバイザーの記録の区画は無い", async () => {
  const { root } = await open(show());
  expect(q(root).queryByText(/スーパーバイザー/)).toBeNull();
});

it("AC-284 #808: 図（stages.figure）がある課題の「計画の本文」に、図の部品が出る", async () => {
  const mermaid: MermaidLike = {
    initialize: vi.fn(),
    render: vi.fn(async () => ({ svg: "<svg></svg>" })),
  };
  setMermaidLoader(vi.fn(async () => mermaid));
  const { root } = await open(
    show({ stages: { ...stagesOf(), figure: "graph TD; A-->B" } }),
    { hash: PLAN_HASH },
  );
  await q(root).findByTestId("figure-svg");
  expect(mermaid.render).toHaveBeenCalled();
});

it("AC-413 #808: 長い計画の本文は、開いた直後は途中までが出て、「全文を開く」を押すと全部が出る", async () => {
  // jsdom は高さを持たないので、畳んだ要素が実際に切れている状態を作る
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get: () => 400,
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get: () => 100,
  });
  const long = Array.from({ length: 60 }, (_, i) => `- 項目 ${i}`).join("\n");
  const { root } = await open(show({ plans: [plan(1, long)] }), {
    hash: PLAN_HASH,
  });
  const p = q(root).getByTestId("plan-body");
  expect(p.querySelector('[data-clamped="true"]')).not.toBeNull();
  fireEvent.click(await within(p).findByRole("button", { name: "全文を開く" }));
  expect(p.querySelector('[data-clamped="true"]')).toBeNull();
  expect(
    within(p).getByRole("button", { name: "折りたたむ" }),
  ).toBeInTheDocument();
});

it("#808: 「経緯」の切り替えは URL（…/history）と置き場を持つ", async () => {
  const { root } = await open(show(), {
    hash: "#/a/alpha/challenges/C-1/history",
    extra: { [`${BASE}/log?challenge=C-1`]: { activities: [] } },
  });
  expect(await q(root).findByTestId("detail-history")).toBeInTheDocument();
  expect(q(root).getByRole("link", { name: "経緯" })).toHaveAttribute(
    "aria-current",
    "page",
  );
});

it("#808: 課題の詳細を開いても fetch は GET だけで、form 要素も入力欄も無い", async () => {
  const { root, requests } = await open(
    show({ actions: ["answer_question"], holds: [hold()] }),
  );
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  expect(root.querySelector("form, input, textarea")).toBeNull();
});

it("AC-107 #932: 課題の詳細のツリーの箱に、タスクの progress の語が出る", async () => {
  const { root } = await open(
    show({
      tree: {
        tasks: [
          task({
            key: "a",
            id: "T-1",
            phase: "running",
            progress: "verifying",
          }),
          task({ key: "b", id: "T-2" }),
        ],
      },
    }),
  );
  const boxes = q(root).getAllByTestId("task-box");
  expect(
    q(boxes[0] as HTMLElement).getByTestId("task-progress"),
  ).toHaveTextContent("検証待ち");
  expect(q(boxes[1] as HTMLElement).queryByTestId("task-progress")).toBeNull();
});

it.each([
  ["reflecting", "反映中"],
  ["checking", "全体の確認中"],
])(
  "課題の詳細のタスクのツリーの見出しに、課題の progress（%s）の語「%s」が出る",
  async (progress, word) => {
    const { root } = await open(show({ tree: { progress } }));
    expect(q(root).getByTestId("challenge-progress")).toHaveTextContent(word);
  },
);

it("課題の progress が null の課題の詳細に、課題の progress の札は出ない", async () => {
  const { root } = await open(show({ tree: { progress: null } }));
  expect(q(root).queryByTestId("challenge-progress")).toBeNull();
});

const passedFix = (over: Record<string, unknown> = {}) => ({
  at: "2026-11-20T14:12:00Z",
  files: ["runtime/internal/cli/ticket.go"],
  from_ref: "a",
  operation_id: null,
  run_id: "R-1",
  target: "integration",
  task_id: null,
  to_ref: "b",
  ...over,
});

it("AC-127: 課題の詳細に、通したコンフリクトの解消の件数・宛先・直したファイルの名前が出る", async () => {
  const { root } = await open(
    show({
      conflict_fixes: [
        passedFix({ task_id: "T-9" }),
        passedFix({ run_id: "R-2", target: "release", operation_id: "O-1" }),
      ],
    }),
  );
  const box = q(root).getByTestId("conflict-fixes");
  expect(q(box).getByTestId("conflict-fixes-count").textContent).toBe("2");
  const rows = q(box).getAllByTestId("conflict-fix");
  expect(rows[0]?.textContent).toContain("統合先（T-9）");
  expect(rows[0]?.textContent).toContain("runtime/internal/cli/ticket.go");
  expect(rows[1]?.textContent).toContain("反映先");
});

it("AC-128: 通したコンフリクトの解消が 0 件なら、課題の詳細に区画を出さない", async () => {
  const { root } = await open(show());
  expect(q(root).queryByTestId("conflict-fixes")).toBeNull();
});

// 画面のボタンで選ぶだけの回答（区切り S4d-2 の T5）。課題の詳細の待ちの所の、保留ごとの枠。
const buttonOpts = [
  { id: "a", label: "案 A", description: "A の説明", button: true },
  { id: "b", label: "案 B", description: "", button: true },
];

it("AC-97 #1182: 開いている保留が 2 つあると、保留ごとの枠が 2 つ出て、それぞれに保留の ID・問い・範囲・選択肢が出る", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [
        hold({
          hold_id: "H-61",
          question: "問い 61",
          options: buttonOpts,
          check: "c61",
        }),
        hold({
          hold_id: "H-62",
          question: "問い 62",
          options: buttonOpts,
          check: "c62",
        }),
      ],
    }),
  );
  const frames = q(root).getAllByTestId("hold-question");
  expect(frames).toHaveLength(2);
  expect(
    within(frames[0] as HTMLElement).getByTestId("hold-id").textContent,
  ).toContain("H-61");
  expect(frames[0]?.textContent).toContain("問い 61");
  expect(frames[0]?.textContent).toContain("課題全体");
  expect(
    within(frames[1] as HTMLElement).getByTestId("hold-id").textContent,
  ).toContain("H-62");
  expect(
    within(frames[0] as HTMLElement).getAllByTestId("hold-option-button"),
  ).toHaveLength(2);
});

it("AC-97 #1182: 片方の保留にボタンで答えると、送るのは答えた保留の ID で、読み直した後、もう片方の枠が残る", async () => {
  const h1 = hold({ hold_id: "H-63", options: buttonOpts, check: "c63" });
  const h2 = hold({ hold_id: "H-64", options: buttonOpts, check: "c64" });
  let current = show({ actions: ["answer_question"], holds: [h1, h2] });
  const { root, requests } = await open(current, {
    extra: {
      [`${BASE}/challenges/C-1`]: () =>
        new Response(JSON.stringify(current), { status: 200 }),
      [`POST ${BASE}/challenges/C-1/holds/H-63/answer`]: () => {
        current = {
          ...current,
          holds: [
            { ...h1, answer: "案 A", closed_at: "2026-11-02T00:00:00Z" },
            h2,
          ],
        };
        return new Response(
          JSON.stringify({ cycle: { started: true, reason: "" }, hold: h1 }),
          { status: 200 },
        );
      },
    },
  });
  const first = q(root).getAllByTestId("hold-question")[0] as HTMLElement;
  fireEvent.click(
    within(first).getAllByTestId("hold-option-button")[0] as HTMLElement,
  );
  fireEvent.click(q(root).getByTestId("hold-confirm-send"));
  await waitFor(() =>
    expect(q(root).getAllByTestId("hold-question")).toHaveLength(1),
  );
  expect(q(root).getByTestId("hold-id").textContent).toContain("H-64");
  const post = requests.find((r) => r.method === "POST");
  expect(JSON.parse(post?.body ?? "{}")).toEqual({ option: "a", check: "c63" });
});

it("AC-96 #1182: ボタンで選べない選択肢と、選択肢の無い問いには、ボタンが出ない", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [
        hold({
          hold_id: "H-65",
          options: [
            { id: "add", label: "足す", description: "", button: false },
          ],
          check: "c",
        }),
        hold({ hold_id: "H-66" }),
      ],
    }),
  );
  expect(q(root).queryByTestId("hold-option-button")).toBeNull();
  expect(q(root).getByTestId("hold-option-conversation").textContent).toContain(
    "対話で伝えてください",
  );
  const guide = q(root).getByLabelText("承認方法の案内");
  expect(guide.textContent).toContain("--hold H-66");
  expect(guide.textContent).not.toContain("H-65");
});

it("AC-96 #1182: ボタンの値が無い選択肢は、ボタンにしない（画面では選べると判定しない）", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [
        hold({
          hold_id: "H-69",
          options: [{ id: "x", label: "案 X", description: "" }],
          check: "c",
        }),
      ],
    }),
  );
  expect(q(root).queryByTestId("hold-option-button")).toBeNull();
});

it("AC-101 #1182: スーパーバイザーが関わった保留の枠に、調べたことと推奨が出る。関わっていない枠には出ない", async () => {
  const { root } = await open(
    show({
      actions: ["answer_question"],
      holds: [
        hold({
          hold_id: "H-67",
          supervisor_findings: "調べた内容",
          supervisor_recommendation: "推奨の内容",
        }),
        hold({ hold_id: "H-68" }),
      ],
    }),
  );
  const frames = q(root).getAllByTestId("hold-question");
  expect(
    within(frames[0] as HTMLElement).getByTestId("hold-supervisor").textContent,
  ).toContain("調べた内容");
  expect(
    within(frames[0] as HTMLElement).getByTestId("hold-supervisor").textContent,
  ).toContain("推奨の内容");
  expect(
    within(frames[1] as HTMLElement).queryByTestId("hold-supervisor"),
  ).toBeNull();
});

// 足されたタスクの印・スーパーバイザーの記録・足す依頼（区切り S4d-2 の T8）。
const fullTask = (id: string, p: Partial<Task> = {}): Task => ({
  added: null,
  artifacts: [],
  body: "",
  budget_impl_usd: 0,
  budget_review_usd: 0,
  delegate: null,
  depends_on: [],
  done_criteria: "",
  facts: {},
  id,
  landing: null,
  non_goals: [],
  operation: "",
  plan_version: 1,
  repo: "",
  source_key: null,
  spent_usd: 0,
  status: "in_progress",
  title: `題名 ${id}`,
  verification: null,
  ...p,
});

const svAction = (p: Partial<SupervisorAction> = {}): SupervisorAction => ({
  basis: [],
  challenge_id: "C-1",
  conditions: [],
  created_at: "2026-11-01T03:00:00Z",
  cycle_id: "Y-1",
  findings: "ログを調べた",
  hand: "retry",
  hold_id: null,
  id: 1,
  note: "",
  questions: [],
  reject_reason: null,
  result: "executed",
  run_id: null,
  stop_kind: "",
  task_id: "T-2",
  ...p,
});

const addition = (p: Partial<TaskAddition> = {}): TaskAddition => ({
  created_at: "2026-11-01T05:00:00Z",
  decided_at: null,
  followup_id: null,
  id: "TA-1",
  limit_usd: 20,
  origin: "expand",
  origin_task_id: "T-1",
  state: "awaiting_owner",
  task_ids: [],
  task_titles: ["集計の検算を足す"],
  total_after_usd: 25.5,
  ...p,
});

it("AC-163 #1185: ツリーの、足されたタスクに「後から足した」の印が出る。足されていないタスクには出ない", async () => {
  const added = {
    added_at: "2026-11-01T05:00:00Z",
    followup_id: null,
    origin: "expand",
    origin_task_id: "T-1",
  };
  const { root } = await open({
    ...show({
      tree: {
        tasks: [
          task({ key: "k1", id: "T-1", title: "もとのタスク" }),
          task({ key: "k2", id: "T-2", title: "足したタスク" }),
        ],
      },
    }),
    tasks: [fullTask("T-1"), fullTask("T-2", { added })],
  });
  const boxes = q(root).getAllByTestId("task-box");
  const byTitle = (t: string) =>
    boxes.find((b) => b.textContent?.includes(t)) as HTMLElement;
  expect(byTitle("足したタスク").getAttribute("data-added")).toBe("true");
  expect(
    within(byTitle("足したタスク")).getByTestId("task-box-added").textContent,
  ).toBe("後から足した");
  expect(byTitle("もとのタスク").getAttribute("data-added")).toBeNull();
  expect(
    within(byTitle("もとのタスク")).queryByTestId("task-box-added"),
  ).toBeNull();
});

it("AC-164 #1185: 「スーパーバイザーの記録」に、手の数と、手ごとの行が出る。行からタスクの詳細へ移れる", async () => {
  const { root } = await open({
    ...show(),
    tasks: [fullTask("T-2")],
    supervisor_actions: [
      svAction({
        id: 2,
        created_at: "2026-11-01T04:00:00Z",
        hand: "defer",
        result: "rejected",
        reject_reason: "state_changed",
      }),
      svAction({ id: 1 }),
    ],
  });
  const sec = q(root).getByTestId("sv-records");
  expect(within(sec).getByTestId("sv-records-count").textContent).toContain(
    "2",
  );
  const rows = within(sec).getAllByTestId("sv-record");
  expect(rows).toHaveLength(2);
  expect(rows[0]?.textContent).toContain("もう一度実行する");
  expect(rows[0]?.textContent).toContain("ログを調べた");
  expect(rows[1]?.textContent).toContain("見送る");
  expect(rows[1]?.textContent).toContain("断られた");
  expect(
    within(rows[0] as HTMLElement)
      .getByTestId("sv-record-task")
      .getAttribute("href"),
  ).toBe("#/a/alpha/challenges/C-1/tasks/T-2");
});

it("AC-165 #1185: スーパーバイザーの手が 0 件の課題では、「スーパーバイザーの記録」の節は出ない", async () => {
  const { root } = await open(show());
  expect(q(root).queryByTestId("sv-records")).toBeNull();
});

it("AC-166 #1185: 足す依頼が、状態・足すタスクの題名・足した後の見込みと上限つきで出る", async () => {
  const { root } = await open({ ...show(), additions: [addition()] });
  const row = q(root).getByTestId("addition");
  expect(within(row).getByTestId("addition-state").textContent).toBe(
    "上限を超えるので問い中",
  );
  expect(row.textContent).toContain("集計の検算を足す");
  const est = within(row).getByTestId("addition-estimate").textContent ?? "";
  expect(est).toContain("25.5");
  expect(est).toContain("20");
});

it("AC-167 #1185: 足す依頼が 0 件の課題では、足す依頼の枠は出ない", async () => {
  const { root } = await open(show());
  expect(q(root).queryByTestId("additions")).toBeNull();
});
