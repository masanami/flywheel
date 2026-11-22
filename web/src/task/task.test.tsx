import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type {
  Hold,
  ShowCardResponse,
  ShowRun,
  Task,
  TreeTask,
} from "../api/types.gen.ts";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";

// タスクの詳細の画面（#810）。完了条件の 1 つずつを、名前の AC-<番号> が直接確かめる。
// ブラウザの試験（AC-426・AC-432）は web/e2e/task.spec.ts にある。

beforeEach(() => {
  const m = document.createElement("meta");
  m.name = "flywheel-csrf";
  m.content = "tok";
  document.head.append(m);
});
afterEach(() => {
  vi.unstubAllGlobals();
  for (const m of document.head.querySelectorAll("meta[name=flywheel-csrf]"))
    m.remove();
  cleanup();
});

const BASE = "/api/v1/workspaces/alpha";

const T = (id: string, p: Partial<Task> = {}): Task => ({
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
  id,
  non_goals: [],
  operation: "",
  plan_version: 1,
  repo: "",
  source_key: null,
  spent_usd: 0,
  status: "in_progress",
  title: `題名 ${id}`,
  ...p,
});

const N = (id: string, p: Partial<TreeTask> = {}): TreeTask => ({
  cost_usd: 0,
  depends_on: [],
  id,
  key: `k-${id}`,
  phase: "running",
  progress: null,
  status: null,
  title: `題名 ${id}`,
  ...p,
});

const hold = (p: Partial<Hold> = {}): Hold => ({
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
  task_id: "T-2",
  ...p,
});

const run = (id: string, p: Partial<ShowRun> = {}): ShowRun => ({
  challenge_id: "C-1",
  cost_source: "reported",
  cost_usd: 1.5,
  cycle_budget_usd: null,
  cycle_id: null,
  ended_at: null,
  error: null,
  fix_pending_ref: null,
  flywheel_version: null,
  id,
  judgment: null,
  kind: "delegate",
  max_budget_usd: 10,
  rate_limited: false,
  result: null,
  session_id: null,
  started_at: "2026-11-01T10:00:00Z",
  task_id: "T-2",
  ...p,
});

interface Over {
  tasks?: Task[];
  nodes?: TreeTask[];
  holds?: Hold[];
  runs?: ShowRun[];
  source_children?: ShowCardResponse["source_children"];
}

function show(o: Over = {}): ShowCardResponse {
  return {
    approvals: [],
    card: {
      context: null,
      headline: null,
      modifiers: [],
      next_human_actions: [],
      slot_ids: [],
      state: "in_progress",
    },
    challenge: {
      context: null,
      created_at: "2026-01-01T00:00:00Z",
      dependencies: [],
      dependents: [],
      description: "",
      done_criteria: "",
      followup_of: null,
      headline: null,
      id: "C-1",
      improvement: null,
      integration_ref: null,
      priority: null,
      reporter: "me",
      status: "in_progress",
      status_label: "着手中",
      title: "請求の集計を直す",
      updated_at: "2026-01-01T00:00:00Z",
      urgency: null,
      uses_integration: null,
      version: 3,
    },
    conflict_fixes: [],
    direct_completions: [],
    followups: [],
    holds: o.holds ?? [],
    operations: [],
    pairs: [],
    plans: [],
    runs: o.runs ?? [],
    source_binding: null,
    source_children: o.source_children ?? [],
    stages: {
      actual: null,
      expected: null,
      figure: null,
      issues: null,
      issues_answer: null,
      non_goals: null,
    },
    tasks: o.tasks ?? [T("T-2")],
    waiting: { prerequisites: [] },
    tree: {
      budget_usd: null,
      children: [],
      cost_usd: 0,
      dependencies: [],
      followups_open: 0,
      id: "C-1",
      next_human_actions: [],
      parent: null,
      progress: null,
      status: "in_progress",
      status_label: "着手中",
      tasks: o.nodes ?? [N("T-2")],
      tasks_done: 0,
      tasks_total: 1,
      title: "請求の集計を直す",
      updated_at: "2026-11-01T00:00:00.000Z",
      uses_integration: null,
    },
  };
}

async function open(
  detail: ShowCardResponse,
  opts: { hash?: string; extra?: Record<string, unknown> } = {},
) {
  window.location.hash = opts.hash ?? "#/a/alpha/challenges/C-1/tasks/T-2";
  const ctx = installFakes({
    ...fixtures(),
    [`${BASE}/challenges`]: {
      challenges: [{ ...detail.challenge, card: detail.card }],
    },
    [`${BASE}/challenges/C-1`]: detail,
    ...opts.extra,
  });
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const root = await screen.findByTestId("task-detail");
  return { root, ...ctx };
}

it("AC-92 #810: タスクの詳細の画面では、上の帯の「課題」に選んでいる印が付く", async () => {
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

it("AC-108 #810: その課題に無いタスクを指す URL では、「見つかりません」と「いま進められること」へのリンクが出る", async () => {
  window.location.hash = "#/a/alpha/challenges/C-1/tasks/T-99";
  installFakes({
    ...fixtures(),
    [`${BASE}/challenges/C-1`]: show(),
  });
  render(<App />);
  act(() => FakeEventSource.latest.open());
  expect((await screen.findByRole("alert")).textContent).toContain(
    "見つかりません",
  );
  expect(
    screen
      .getByRole("link", { name: "「いま進められること」へ" })
      .getAttribute("href"),
  ).toBe("#/a/alpha/now");
  expect(screen.queryByTestId("task-detail")).toBeNull();
});

it("AC-285 #810: 課題の画面へのリンク（「課題」）と、その課題の詳細へのリンク（課題の題名）が出る", async () => {
  const { root } = await open(show());
  const crumb = within(within(root).getByTestId("task-crumb"));
  expect(crumb.getByRole("link", { name: "課題" }).getAttribute("href")).toBe(
    "#/a/alpha/challenges",
  );
  expect(
    crumb.getByRole("link", { name: "請求の集計を直す" }).getAttribute("href"),
  ).toBe("#/a/alpha/challenges/C-1");
});

it("AC-286 #810: タスクの題名と、表示の区分の語が出る", async () => {
  const { root } = await open(
    show({
      tasks: [T("T-2", { title: "集計を書き直す" })],
      nodes: [N("T-2", { title: "集計を書き直す", phase: "next" })],
    }),
  );
  expect(
    within(root).getByRole("heading", { name: "集計を書き直す" }),
  ).toBeTruthy();
  expect(within(root).getByTestId("task-phase").textContent).toBe("次に実行");
});

it("AC-287 #810: 前提のタスクの題名が、そのタスクの詳細へのリンクとして出る", async () => {
  const { root } = await open(
    show({
      tasks: [
        T("T-1", { title: "仕様を書く" }),
        T("T-2", { depends_on: ["T-1"] }),
      ],
      nodes: [N("T-1", { phase: "done" }), N("T-2", { depends_on: ["k-T-1"] })],
    }),
  );
  const pre = within(within(root).getByTestId("task-prerequisites"));
  expect(
    pre.getByRole("link", { name: "仕様を書く" }).getAttribute("href"),
  ).toBe("#/a/alpha/challenges/C-1/tasks/T-1");
});

it("AC-288 #810: このタスクを前提にしているタスクの題名が、「この後」にそのタスクの詳細へのリンクとして出る", async () => {
  const { root } = await open(
    show({
      tasks: [
        T("T-2"),
        T("T-3", { title: "結合を確かめる", depends_on: ["T-2"] }),
      ],
      nodes: [N("T-2"), N("T-3", { phase: "blocked" })],
    }),
  );
  const after = within(within(root).getByTestId("task-dependents"));
  expect(
    after.getByRole("link", { name: "結合を確かめる" }).getAttribute("href"),
  ).toBe("#/a/alpha/challenges/C-1/tasks/T-3");
  expect(within(root).getByText("この後")).toBeTruthy();
});

it("AC-289 #810: tree のそのタスクの cost_usd が出る", async () => {
  const { root } = await open(show({ nodes: [N("T-2", { cost_usd: 3.2 })] }));
  expect(within(root).getByTestId("task-cost").textContent).toBe("3.2");
});

it("AC-290 #810: body が Markdown として整って出る（- で始まる行が箇条書きの要素になる）", async () => {
  const { root } = await open(
    show({
      tasks: [T("T-2", { body: "手順:\n\n- 表を作る\n- 件数を数える" })],
    }),
  );
  const items = within(within(root).getByTestId("task-body")).getAllByRole(
    "listitem",
  );
  expect(items.map((i) => i.textContent)).toEqual(["表を作る", "件数を数える"]);
});

it("AC-291 #810: done_criteria が Markdown として整って出る", async () => {
  const { root } = await open(
    show({
      tasks: [T("T-2", { done_criteria: "- 検査が通る\n- **件数**が合う" })],
    }),
  );
  const box = within(within(root).getByTestId("task-done-criteria"));
  expect(box.getAllByRole("listitem")).toHaveLength(2);
  expect(box.getByText("件数").tagName).toBe("STRONG");
});

it("AC-292 #810: non_goals の要素が箇条書きで出る", async () => {
  const { root } = await open(
    show({
      tasks: [T("T-2", { non_goals: ["画面は変えない", "旧基盤は止めない"] })],
    }),
  );
  const items = within(within(root).getByTestId("task-non-goals")).getAllByRole(
    "listitem",
  );
  expect(items.map((i) => i.textContent)).toEqual([
    "画面は変えない",
    "旧基盤は止めない",
  ]);
});

const waitingShow = () =>
  show({
    nodes: [N("T-2", { phase: "waiting" })],
    holds: [hold({ question: "表が既にあるときは作り直しますか" })],
  });

it("AC-293 #810: phase が waiting のタスクの詳細に、保留の問いの文と、ターミナルのコマンドの案内（flywheel answer）が出る", async () => {
  const { root } = await open(waitingShow());
  const w = within(within(root).getByTestId("task-waiting"));
  expect(w.getByText("表が既にあるときは作り直しますか")).toBeTruthy();
  expect(w.getByText(/flywheel answer C-1/)).toBeTruthy();
});

it("AC-294 #810: phase が waiting のタスクの詳細に、ボタンは無い", async () => {
  const { root } = await open(waitingShow());
  expect(within(root).queryAllByRole("button")).toHaveLength(0);
});

// #814 掃除 6: 「あなたを待っているもの」は、答えていないオーナー宛ての保留があるとき出す（区分が waiting でなくても）。
it("#814: 区分が waiting でなくても、答えていないオーナー宛ての保留があれば「あなたを待っているもの」が出る", async () => {
  const { root } = await open(
    show({
      nodes: [N("T-2", { phase: "running" })],
      holds: [hold({ question: "走りながら上げた問い" })],
    }),
  );
  const w = within(within(root).getByTestId("task-waiting"));
  expect(w.getByText("走りながら上げた問い")).toBeTruthy();
});

it("#814: 区分が waiting なら、保留が見つからなくても案内は出す", async () => {
  const { root } = await open(
    show({ nodes: [N("T-2", { phase: "waiting" })], holds: [] }),
  );
  expect(within(root).getByTestId("task-waiting")).toBeTruthy();
});

it("#814: 区分が waiting でなく、保留も答え済みなら「あなたを待っているもの」は出さない", async () => {
  const { root } = await open(
    show({
      nodes: [N("T-2", { phase: "running" })],
      holds: [hold({ answer: "はい" })],
    }),
  );
  expect(within(root).queryByTestId("task-waiting")).toBeNull();
});

const holdsShow = () =>
  show({
    holds: [
      hold({ question: "新しい問い", raised_at: "2026-11-01T02:00:00Z" }),
      hold({
        question: "古い問い",
        raised_at: "2026-11-01T01:00:00Z",
        answer: "そろえる",
        relay_quote: "旧基盤とそろえてほしい",
        replan: true,
      }),
      hold({ question: "別のタスクの問い", task_id: "T-9" }),
    ],
  });

it("AC-295 #810: 「問いと回答」に、そのタスクの保留の問いと回答が、古い順に出る", async () => {
  const { root } = await open(holdsShow());
  const rows = within(within(root).getByTestId("task-holds")).getAllByTestId(
    "task-hold",
  );
  expect(rows).toHaveLength(2);
  expect(rows[0]?.textContent).toContain("古い問い");
  expect(rows[0]?.textContent).toContain("そろえる");
  expect(rows[1]?.textContent).toContain("新しい問い");
});

it("#798: 保留の回答は Markdown として整えて出る（箇条書きが li になる。「回答:」の語は残る）", async () => {
  const { root } = await open(
    show({
      holds: [
        hold({
          question: "どうしますか",
          answer: "次の 2 つにする:\n\n- 案A を採る\n- **案B** は見送る",
        }),
      ],
    }),
  );
  const a = within(root).getByTestId("task-hold-answer");
  expect(a.textContent).toContain("回答:");
  expect([...a.querySelectorAll("li")].map((li) => li.textContent)).toEqual([
    "案A を採る",
    "案B は見送る",
  ]);
  expect(a.querySelector("li strong")?.textContent).toBe("案B");
});

it("AC-296 #810: 「問いと回答」に、ほかのタスクの保留は出ない", async () => {
  const { root } = await open(holdsShow());
  expect(within(root).queryByText("別のタスクの問い")).toBeNull();
});

it("AC-297 #810: 対話で受けた回答（relay_quote がある）の行に、発言の要旨が出る", async () => {
  const { root } = await open(holdsShow());
  expect(within(root).getByTestId("task-hold-quote").textContent).toContain(
    "旧基盤とそろえてほしい",
  );
});

it("AC-298 #810: 計画の作り直しへ回した回答（replan が真）の行に、その旨が出る", async () => {
  const { root } = await open(holdsShow());
  expect(within(root).getByTestId("task-hold-replan").textContent).toContain(
    "計画の作り直し",
  );
});

it("AC-299 #810: 保留が 1 件も無いタスクの詳細に、「問いと回答」の区画は出ない", async () => {
  const { root } = await open(show({ holds: [hold({ task_id: "T-9" })] }));
  expect(within(root).queryByTestId("task-holds")).toBeNull();
  expect(within(root).queryByText("問いと回答")).toBeNull();
});

const runsShow = () =>
  show({
    tasks: [T("T-2")],
    runs: [
      run("R-1", {
        started_at: "2026-11-01T09:00:00Z",
        ended_at: "2026-11-01T09:30:00Z",
        result: "errored",
        cost_usd: 0.8,
      }),
      run("R-2", { started_at: "2026-11-01T10:00:00Z", cost_usd: 1.25 }),
      run("R-3", { task_id: "T-9", started_at: "2026-11-01T11:00:00Z" }),
    ],
  });

const reportBody = {
  report: {
    assumptions: ["既存のテストは変えない"],
    run_id: "R-2",
    unverified: ["本番の負荷での挙動"],
  },
};
const withReport = {
  [`${BASE}/runs/R-2/report`]: reportBody,
  [`${BASE}/runs/R-1/report`]: reportBody,
};

it("AC-300 #810: 実行の記録に、task_id がそのタスクの実行だけが、新しい順に出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const rows = within(root).getAllByTestId("task-run");
  expect(rows.map((r) => r.getAttribute("data-run-id"))).toEqual([
    "R-2",
    "R-1",
  ]);
});

it("AC-301 #810: 実行の記録の行に、その実行の種類が出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const kinds = within(root).getAllByTestId("run-kind");
  expect(kinds[0]?.textContent).toBe("委譲");
});

it("AC-302 #810: 実行の記録の行に、その実行の結果が出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const results = within(root).getAllByTestId("run-result");
  expect(results[0]?.textContent).toBe("実行中");
  expect(results[1]?.textContent).toBe("失敗");
});

it("AC-303 #810: 実行の記録の行に、その実行の費用が出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const costs = within(root).getAllByTestId("run-cost");
  expect(costs[0]?.textContent).toBe("1.25 USD");
  expect(costs[1]?.textContent).toBe("0.8 USD");
});

it("AC-304 #810: 実行の記録の行に、その実行の始まった時刻が出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const started = within(root).getAllByTestId("run-started");
  expect(started[0]?.getAttribute("datetime")).toBe("2026-11-01T10:00:00Z");
  expect(started[0]?.textContent).toMatch(/^\d+\/\d+ \d\d:\d\d$/);
});

it("AC-305 #810: 終わっている実行の行に、終わった時刻が出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const rows = within(root).getAllByTestId("task-run");
  const ended = within(rows[1] as HTMLElement).getByTestId("run-ended");
  expect(ended.getAttribute("datetime")).toBe("2026-11-01T09:30:00Z");
});

it("AC-306 #810: 終わっていない実行の行に、終わった時刻は出ない", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const rows = within(root).getAllByTestId("task-run");
  expect(within(rows[0] as HTMLElement).queryByTestId("run-ended")).toBeNull();
});

it("AC-307 #810: 委譲の実行が 2 件あるとき、報告の API は、新しい方の実行の ID で 1 回だけ呼ばれる", async () => {
  const { root, requests } = await open(runsShow(), { extra: withReport });
  await within(root).findByTestId("run-assumptions");
  const calls = requests.filter((r) => r.url.includes("/report"));
  expect(calls.map((c) => c.url)).toEqual([`${BASE}/runs/R-2/report`]);
});

it("AC-308 #810: 報告に仮定があるとき、実行の記録の区画に、その仮定が出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const box = await within(root).findByTestId("run-assumptions");
  expect(box.textContent).toContain("既存のテストは変えない");
  expect(
    within(within(root).getByTestId("task-runs")).getByTestId(
      "run-assumptions",
    ),
  ).toBe(box);
});

it("AC-309 #810: 報告に確かめていないことがあるとき、実行の記録の区画に、それが出る", async () => {
  const { root } = await open(runsShow(), { extra: withReport });
  const box = await within(root).findByTestId("run-unverified");
  expect(box.textContent).toContain("本番の負荷での挙動");
});

it("AC-310 #810: source_key があるタスクの詳細に、取り込み元の子の題名（上流へのリンク）と、上流の状態が出る", async () => {
  const { root } = await open(
    show({
      tasks: [T("T-2", { source_key: "example/b#29" })],
      source_children: [
        {
          external_key: "example/b#29",
          parent_external_key: "example/b#28",
          title: "集計クエリを書き直す",
          upstream_state: "closed",
          url: "https://example.com/b/issues/29",
        },
      ],
    }),
  );
  const src = within(within(root).getByTestId("task-source"));
  const a = src.getByRole("link", { name: "集計クエリを書き直す" });
  expect(a.getAttribute("href")).toBe("https://example.com/b/issues/29");
  expect(within(root).getByTestId("task-upstream").textContent).toBe(
    "閉じている",
  );
});

it("#810: 報告を読めなくても、実行の記録は出る（報告の区画だけ出ない）", async () => {
  const { root } = await open(runsShow());
  await waitFor(() =>
    expect(within(root).getAllByTestId("task-run")).toHaveLength(2),
  );
  expect(within(root).queryByTestId("run-assumptions")).toBeNull();
});

it("#810: 前提・この後のリンクの横に、そのタスクの表示の区分が出る", async () => {
  const { root } = await open(
    show({
      tasks: [T("T-1"), T("T-2", { depends_on: ["T-1"] })],
      nodes: [N("T-1", { phase: "done" }), N("T-2")],
    }),
  );
  expect(
    within(within(root).getByTestId("task-prerequisites")).getByText("済み"),
  ).toBeTruthy();
});

// 誰が答えたかは、経路と本人確認の方式の組で決まる（対話で受けた回答は、経路が cli・方式が relayed で記録される）。
it.each([
  ["cli", "relayed", "あなた（対話）"],
  ["ui", "none", "あなた（画面）"],
  ["cli", "tty_confirm", "あなた（ターミナル）"],
  ["invoker", "none", "invoker"],
])(
  "#810: 回答の行に、誰が答えたかが出る（経路 %s・方式 %s）",
  async (channel, verification, want) => {
    const { root } = await open(
      show({
        holds: [
          hold({
            answer: "はい",
            answered_channel: channel,
            answered_verification: verification,
          }),
        ],
      }),
    );
    expect(within(root).getByTestId("task-hold-by").textContent).toContain(
      want,
    );
  },
);

// ---- 委譲先の実行・成果物・値・検証・費用の枠（#931。AC-63〜80・82〜84）----
const D = (p: Partial<NonNullable<Task["delegate"]>> = {}) => ({
  requested_kind: null,
  run_id: "R-1",
  state: "succeeded",
  summary: "要約",
  ...p,
});

it("AC-63 AC-64 AC-65 AC-79 #931: 委譲先の実行の状態の語・要約・求めていることが出る", async () => {
  const { root } = await open(
    show({
      tasks: [
        T("T-2", {
          delegate: D({
            state: "waiting",
            summary: "待っています",
            requested_kind: "answer",
          }),
        }),
      ],
    }),
  );
  const sec = within(within(root).getByTestId("task-delegate"));
  expect(sec.getByTestId("delegate-state").textContent).toBe("待機中");
  expect(sec.getByTestId("delegate-summary").textContent).toBe("待っています");
  expect(sec.getByTestId("delegate-requested").textContent).toBe("回答");
});

it("AC-66 #931: delegate が null なら「委譲先の実行」の区画は出ない", async () => {
  const { root } = await open(show());
  expect(within(root).queryByTestId("task-delegate")).toBeNull();
});

it("AC-84 #931: 要約の Markdown の記法は整えず文字のまま出る", async () => {
  const text = "# 見出し\n**太字** [x](http://e.com)";
  const { root } = await open(
    show({ tasks: [T("T-2", { delegate: D({ summary: text }) })] }),
  );
  const el = within(root).getByTestId("delegate-summary");
  expect(el.textContent).toBe(text);
  expect(el.querySelector("a, h1, strong")).toBeNull();
});

it("AC-67 AC-68 AC-69 AC-70 AC-80 #931: 成果物の種類（語に直す）・状態（記録のまま）・http(s) だけのリンク・統合先の語", async () => {
  const { root } = await open(
    show({
      tasks: [
        T("T-2", {
          artifacts: [
            {
              kind: "pull_request",
              ref: "https://example.com/pr/1",
              role: "integration",
              state: "Merged_x",
            },
            {
              kind: "pr",
              ref: "javascript:alert(1)",
              role: null,
              state: "open",
            },
          ],
        }),
      ],
    }),
  );
  const items = within(root).getAllByTestId("task-artifact");
  expect(
    within(items[0] as HTMLElement).getByTestId("artifact-kind").textContent,
  ).toBe("成果物（pull_request）");
  expect(
    within(items[1] as HTMLElement).getByTestId("artifact-state").textContent,
  ).toBe("open");
  expect(
    within(items[0] as HTMLElement).getByTestId("artifact-state").textContent,
  ).toBe("Merged_x");
  expect(
    within(items[0] as HTMLElement)
      .getByRole("link")
      .getAttribute("href"),
  ).toBe("https://example.com/pr/1");
  expect(
    within(items[0] as HTMLElement).getByTestId("artifact-role").textContent,
  ).toBe("統合先");
  expect(
    within(items[1] as HTMLElement).getByTestId("artifact-kind").textContent,
  ).toBe("プルリクエスト");
  expect(within(items[1] as HTMLElement).queryByRole("link")).toBeNull();
  expect(
    within(items[1] as HTMLElement).getByTestId("artifact-ref").textContent,
  ).toBe("javascript:alert(1)");
  expect(
    within(items[1] as HTMLElement).queryByTestId("artifact-role"),
  ).toBeNull();
});

it("AC-71 AC-72 #931: facts の名前と値が出る。空なら区画は出ない", async () => {
  const a = await open(
    show({ tasks: [T("T-2", { facts: { self_review: "complete" } })] }),
  );
  const f = within(a.root).getByTestId("task-fact");
  expect(f.textContent).toContain("self_review");
  expect(f.textContent).toContain("complete");
  cleanup();
  const b = await open(show());
  expect(within(b.root).queryByTestId("task-facts")).toBeNull();
});

it("AC-73 AC-74 AC-75 #931: 検証の判定の語・理由・時刻が出る", async () => {
  const { root } = await open(
    show({
      tasks: [
        T("T-2", {
          verification: {
            at: "2026-11-01T10:00:00Z",
            reason: "条件を確かめた",
            verdict: "met",
          },
        }),
      ],
    }),
  );
  const sec = within(within(root).getByTestId("task-verification"));
  expect(sec.getByTestId("verification-verdict").textContent).toBe("満たした");
  expect(sec.getByTestId("verification-reason").textContent).toBe(
    "条件を確かめた",
  );
  expect(sec.getByTestId("verification-at").textContent).toMatch(
    /^\d{2}\/\d{2} \d{2}:\d{2}$/,
  );
  expect(sec.getByTestId("verification-at").getAttribute("datetime")).toBe(
    "2026-11-01T10:00:00Z",
  );
});

it.each([
  ["AC-76", "verified", "検証済み"],
  ["AC-77", "sent", "統合中"],
  ["AC-78", "landed", "統合済み"],
])("%s #931: landing が %s のとき「%s」が出る", async (_ac, landing, word) => {
  const { root } = await open(show({ tasks: [T("T-2", { landing })] }));
  expect(within(root).getByTestId("task-landing").textContent).toBe(word);
});

it("AC-82 AC-83 #931: 「費用」に枠（実装とレビュー対応）と使った額が出る", async () => {
  const { root } = await open(
    show({
      tasks: [
        T("T-2", {
          budget_impl_usd: 4,
          budget_review_usd: 2.5,
          spent_usd: 0.9,
        }),
      ],
      nodes: [N("T-2", { cost_usd: 1.25 })],
    }),
  );
  expect(within(root).getByTestId("task-budget-impl").textContent).toBe("4");
  expect(within(root).getByTestId("task-budget-review").textContent).toBe(
    "2.5",
  );
  expect(within(root).getByTestId("task-cost").textContent).toBe("1.25");
  // 枠に数える使った額（spent_usd）は、実行の全費用（cost_usd）とは別に、枠の欄へ並べて出す。
  expect(within(root).getByTestId("task-spent").textContent).toBe("0.9");
  expect(within(root).getByTestId("task-budget").textContent).toBe(
    "枠使った額 0.9 USD ／ 実装 4 USD ・ レビュー対応 2.5 USD",
  );
});

it("成果物の状態が空文字のとき、区切りと状態を出さない", async () => {
  const { root } = await open(
    show({
      tasks: [
        T("T-2", {
          artifacts: [
            { kind: "branch", ref: "feat/x", role: null, state: "" },
            { kind: "branch", ref: "feat/y", role: null, state: "open" },
          ],
        }),
      ],
    }),
  );
  const items = within(root).getAllByTestId("task-artifact");
  expect(
    within(items[0] as HTMLElement).queryByTestId("artifact-state"),
  ).toBeNull();
  expect(items[0]?.textContent).toBe("ブランチ ・ feat/x");
  expect(items[1]?.textContent).toBe("ブランチ ・ feat/y ・ open");
});

it("AC-129: 実行に確かめ待ちの中身（fix_pending_ref）があるタスクの詳細に、コンフリクトの解消の確かめを待っている旨が出る", async () => {
  const { root } = await open(
    show({ runs: [run("R-1", { fix_pending_ref: "abc123" })] }),
  );
  expect(within(root).getByTestId("task-fix-pending")).toHaveTextContent(
    "コンフリクトの解消の確かめを待っています",
  );
  // 区分の語は変えない
  expect(within(root).getByTestId("task-phase")).toHaveTextContent("実行中");
});

it("AC-129: 確かめ待ちの中身が無ければ、その旨は出さない", async () => {
  const { root } = await open(show({ runs: [run("R-1")] }));
  expect(within(root).queryByTestId("task-fix-pending")).toBeNull();
});
