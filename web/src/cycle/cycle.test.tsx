import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { CYCLE_END_REASON_CODES } from "../api/types.gen.ts";
import {
  CYCLE_RESULTS,
  CYCLE_RESULT_LABELS,
  CYCLE_STATES,
  CYCLE_STATE_SHORT_LABELS,
  CYCLE_WORK_STAGES,
  CYCLE_WORK_STAGE_LABELS,
  cycleStateText,
} from "../labels.ts";
import { NO_CYCLE_WORK_TEXT } from "../shell/CycleWorkPart.tsx";
import { type Agent, openAgents } from "../testing/agents.tsx";
import { emptyStatus } from "../testing/fakes.ts";
import { cycleEndReasonText, cycleResultText } from "./CycleScreen.tsx";

// サイクルの画面の受入基準を 1 つずつ直接確かめる。Issue #806。

const STARTED = "2026-10-17T00:50:00Z";
const ENDED = "2026-10-17T01:00:00Z";
const agent: Agent = {
  name: "alpha",
  cycle: {
    state: "idle",
    last_started_at: STARTED,
    last_ended_at: ENDED,
    last_result: "completed",
  },
  cycleWork: { total: 3, stages: { classify: 1, plan: 2 } },
};

beforeEach(() => {
  window.location.hash = "";
});
afterEach(() => vi.unstubAllGlobals());

const body = () => document.querySelector(".shell-body") as HTMLElement;

it("AC-376 #806: サイクルの画面に、見出し「サイクル」が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  expect(
    await screen.findByRole("heading", { name: "サイクル" }),
  ).toBeInTheDocument();
});

it("AC-377 #806: 最後のサイクルの始まった時刻が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  const el = await screen.findByTestId("cycle-last-started");
  expect(el).toHaveTextContent(new Date(STARTED).toLocaleString());
  expect(el.querySelector("time")?.getAttribute("datetime")).toBe(STARTED);
});

it("AC-378 #806: 最後のサイクルの終わった時刻が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  const el = await screen.findByTestId("cycle-last-ended");
  expect(el).toHaveTextContent(new Date(ENDED).toLocaleString());
});

it("AC-379 #806: 最後のサイクルの結果が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  expect(await screen.findByTestId("cycle-last-result")).toHaveTextContent(
    "最後まで終わった",
  );
});

it("AC-145: 一度も回していないときは、状態と「サイクルを回す」だけを出す（記録・費用の内訳の枠は出さない）", async () => {
  await openAgents(
    [{ name: "alpha", cycle: { state: "idle" }, cycleWork: { total: 1 } }],
    "#/a/alpha/cycle",
  );
  expect(await screen.findByTestId("cycle-screen-state")).toHaveTextContent(
    "走っていない",
  );
  expect(
    await screen.findByRole("button", { name: "サイクルを回す" }),
  ).toBeInTheDocument();
  for (const id of [
    "cycle-last-started",
    "cycle-supervisor-empty",
    "cycle-costs-empty",
    "cycle-structure",
  ]) {
    expect(screen.queryByTestId(id)).toBeNull();
  }
  expect(screen.queryByText("スーパーバイザーの記録")).toBeNull();
  expect(screen.queryByText("費用の内訳")).toBeNull();
});

it("#806: 状態（実行中・走っていない・止まっているかもしれない）が出る", async () => {
  await openAgents(
    [{ name: "alpha", cycle: { state: "stale" } }],
    "#/a/alpha/cycle",
  );
  expect(await screen.findByTestId("cycle-screen-state")).toHaveTextContent(
    "止まっているかもしれない",
  );
});

it("AC-380 #806: 走っていないときは、サイクルを止めるボタンは無い（ボタンは「サイクルを回す」だけ）", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  await screen.findByTestId("cycle-work");
  const names = within(body())
    .queryAllByRole("button")
    .map((b) => b.textContent);
  expect(names).toEqual(["サイクルを回す"]);
});

it("AC-181 #995・AC-381 #806: サイクルを回すと進むものの合計と「サイクルを回す」のボタンが出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  const part = await screen.findByTestId("cycle-work");
  await waitFor(() =>
    expect(within(part).getByTestId("cycle-work-total")).toHaveTextContent("3"),
  );
  expect(
    within(part).getByRole("button", { name: "サイクルを回す" }),
  ).toBeEnabled();
  // 段ごとの件数は、構造の図に、段を進む順で全部の段が出る（0 件の段も）
  const structure = await screen.findByTestId("cycle-structure");
  expect(structure.querySelector('[data-stage="plan"]')).toHaveTextContent(
    "計画 2",
  );
  expect(structure.querySelector('[data-stage="release"]')).toHaveTextContent(
    "反映 0",
  );
  expect(
    [...structure.querySelectorAll("[data-stage]")].map((e) =>
      e.getAttribute("data-stage"),
    ),
  ).toEqual([...CYCLE_WORK_STAGES]);
});

it("#806: 走っているときは「サイクルを回す」を押せない", async () => {
  await openAgents(
    [{ ...agent, cycle: { ...agent.cycle, state: "running" } }],
    "#/a/alpha/cycle",
  );
  const part = await screen.findByTestId("cycle-work");
  await waitFor(() =>
    expect(
      within(part).getByRole("button", { name: "サイクルを回す" }),
    ).toBeDisabled(),
  );
});

it("#806: サイクルの状態・結果・段の全部の値に文言がある", () => {
  for (const s of CYCLE_STATES) {
    expect(CYCLE_STATE_SHORT_LABELS[s]).toBeTruthy();
    expect(cycleStateText(s)).toBe(CYCLE_STATE_SHORT_LABELS[s]);
  }
  for (const r of CYCLE_RESULTS) {
    expect(cycleResultText(r)).toBe(CYCLE_RESULT_LABELS[r]);
  }
  expect(CYCLE_WORK_STAGES.map((s) => CYCLE_WORK_STAGE_LABELS[s])).toEqual([
    "反映",
    "分類",
    "計画",
    "委譲",
    "完了の検証",
    "学び",
    "改修の提案",
    "片付け",
    "スーパーバイザー待ち",
  ]);
});

it("#806: 進められるものが 0 件のとき、始めない旨が出て、「サイクルを回す」は押せない", async () => {
  await openAgents([{ ...agent, cycleWork: { total: 0 } }], "#/a/alpha/cycle");
  const part = await screen.findByTestId("cycle-work");
  await within(part).findByText(NO_CYCLE_WORK_TEXT);
  expect(
    within(part).getByRole("button", { name: "サイクルを回す" }),
  ).toBeDisabled();
});

it("#806: 止まっているかもしれないときは、ターミナルのコマンドの案内が出て、「サイクルを回す」は押せない", async () => {
  await openAgents(
    [{ ...agent, cycle: { ...agent.cycle, state: "stale" } }],
    "#/a/alpha/cycle",
  );
  const part = await screen.findByTestId("cycle-work");
  await within(part).findByText("flywheel cycle --workspace '/w/alpha'");
  expect(
    within(part).getByRole("button", { name: "サイクルを回す" }),
  ).toBeDisabled();
});

it("#798: エージェントを切り替えても、前のエージェントの「サイクルを始めました。」は残らない", async () => {
  await openAgents([agent, { ...agent, name: "beta" }], "#/a/alpha/cycle", {
    "POST /api/v1/workspaces/alpha/cycle": { started: true },
    "POST /api/v1/workspaces/beta/cycle": { started: true },
  });
  const part = await screen.findByTestId("cycle-work");
  await waitFor(() =>
    expect(
      within(part).getByRole("button", { name: "サイクルを回す" }),
    ).toBeEnabled(),
  );
  fireEvent.click(within(part).getByRole("button", { name: "サイクルを回す" }));
  expect(await screen.findByText("サイクルを始めました。")).toBeInTheDocument();
  act(() => {
    window.location.hash = "#/a/beta/cycle";
  });
  await waitFor(() =>
    expect(screen.getByTestId("cycle-work")).toBeInTheDocument(),
  );
  expect(screen.queryByText("サイクルを始めました。")).toBeNull();
});

it("#798: 止まっているかもしれないときの案内は、ターミナルで flywheel cycle を実行すると、止まったサイクルを片付けてから始める旨を伝える", async () => {
  await openAgents(
    [{ ...agent, cycle: { ...agent.cycle, state: "stale" } }],
    "#/a/alpha/cycle",
  );
  const part = await screen.findByTestId("cycle-work");
  const code = await within(part).findByText(
    "flywheel cycle --workspace '/w/alpha'",
  );
  const note = code.closest("p");
  expect(note).toHaveTextContent("止まったサイクルを片付けてから始めます");
  expect(note).toHaveTextContent("ターミナルで");
  expect(note).toHaveTextContent("この画面からは始められません");
});

const RUNNING = {
  active_runs: 2,
  budget_usd: 20,
  max_parallel_runs: 3,
  rounds: 4,
  spent_usd: 6.5,
  started_at: "2026-10-17T02:00:00Z",
  stop_requested: false,
  trigger: "ui",
  watching: [
    {
      challenge_id: "C-1",
      run_started_at: "2026-10-17T02:05:00Z",
      spent_usd: 2.25,
      state: "working",
      task_id: "T-1",
      title: "タスクの題名",
    },
    {
      challenge_id: "C-2",
      run_started_at: "2026-10-17T02:06:00Z",
      spent_usd: 0,
      state: "waiting_owner",
      task_id: null,
      title: "課題の題名",
    },
  ],
};
const URL_CYCLE = "/api/v1/workspaces/alpha/cycle";
const runningAgent: Agent = { ...agent, cycle: { state: "running" } };
const screenBody = (extra: Record<string, unknown>) => ({
  last_ended_at: null,
  last_result: null,
  last_started_at: "2026-10-17T02:00:00Z",
  state: "running",
  running: RUNNING,
  supervisor_actions: [],
  costs: [],
  ...extra,
});

it("AC-125〜129: 走っているとき、始まり・きっかけ・回り・実行の件数と上限・費用と上限を出す", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({}),
  });
  expect(await screen.findByTestId("cycle-running-started")).toHaveTextContent(
    new Date(RUNNING.started_at).toLocaleString(),
  );
  expect(screen.getByTestId("cycle-running-trigger")).toHaveTextContent(
    "画面のボタン",
  );
  expect(screen.getByTestId("cycle-running-rounds")).toHaveTextContent("4");
  expect(screen.getByTestId("cycle-running-runs")).toHaveTextContent(
    "2 件（同時に 3 件まで）",
  );
  expect(screen.getByTestId("cycle-running-cost")).toHaveTextContent(
    "6.5 USD（上限 20 USD）",
  );
});

it("AC-130〜133: 見ているタスクを、題名・様子（言葉）・実行の始まり・費用つきで並べ、タスクの詳細（無ければ課題の詳細）へリンクする", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({}),
  });
  const rows = await screen.findAllByTestId("cycle-watching-row");
  expect(rows).toHaveLength(2);
  expect(rows[0]).toHaveTextContent("タスクの題名");
  expect(rows[0]).toHaveTextContent("作業中");
  expect(rows[0]).toHaveTextContent("2.25 USD");
  expect(rows[1]).toHaveTextContent("あなたの回答か承認待ち");
  const hrefs = rows.map((r) => r.querySelector("a")?.getAttribute("href"));
  expect(hrefs[0]).toContain("T-1");
  expect(hrefs[1]).toContain("C-2");
  expect(hrefs[1]).not.toContain("T-");
});

it("AC-134: 構造の図に承認待ち・回答待ちの件数が出る（承認は計画・完了・不可逆操作の合計）", async () => {
  const ch = (id: string, status: string) => ({ id, status });
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({}),
    "/api/v1/workspaces/alpha/status": {
      ...emptyStatus,
      needs_human: {
        ...emptyStatus.needs_human,
        challenges: [
          ch("C-1", "awaiting_plan_approval"),
          ch("C-2", "awaiting_completion_approval"),
          ch("C-3", "awaiting_human"),
          ch("C-4", "awaiting_human"),
        ],
        operations: [{ id: "O-1", challenge_id: "C-2", summary: "x" }],
      },
    },
  });
  expect(await screen.findByTestId("cycle-waiting")).toHaveTextContent(
    "承認待ち 3 件・回答待ち 2 件",
  );
});

it("AC-135〜137: 走っていて止める指示が無いときだけ「サイクルを止める」が出て、押すと止める口へ送り、読み直す", async () => {
  let stopped = false;
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: () =>
      new Response(
        JSON.stringify(
          screenBody({
            running: { ...RUNNING, stop_requested: stopped },
          }),
        ),
      ),
    [`POST ${URL_CYCLE}/stop`]: () => {
      stopped = true;
      return new Response(
        JSON.stringify({ cycle_id: "Y-1", requested: true, running: true }),
      );
    },
  });
  fireEvent.click(
    await screen.findByRole("button", { name: "サイクルを止める" }),
  );
  expect(await screen.findByTestId("cycle-stop-requested")).toHaveTextContent(
    "止める指示を出しました。いまの段が終わると止まります",
  );
  expect(screen.queryByRole("button", { name: "サイクルを止める" })).toBeNull();
});

it("AC-136: 止める指示が出ているときは、ボタンを出さず、案内だけを出す", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({ running: { ...RUNNING, stop_requested: true } }),
  });
  expect(await screen.findByTestId("cycle-stop-requested")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "サイクルを止める" })).toBeNull();
});

it("AC-137: 止める口が誤りを返すと、理由を出す", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({}),
    [`POST ${URL_CYCLE}/stop`]: () =>
      new Response(
        JSON.stringify({
          error: { code: "internal_error", message: "失敗した" },
        }),
        { status: 500 },
      ),
  });
  fireEvent.click(
    await screen.findByRole("button", { name: "サイクルを止める" }),
  );
  expect(await screen.findByRole("alert")).toHaveTextContent("失敗した");
});

it("AC-138: stale のときは止めるボタンを出さず、走っていない扱い（最後のサイクルの始まりと終わり）で出す", async () => {
  await openAgents(
    [{ ...agent, cycle: { ...agent.cycle, state: "stale" } }],
    "#/a/alpha/cycle",
    {
      [URL_CYCLE]: screenBody({
        state: "stale",
        running: null,
        last_ended_at: ENDED,
        last_result: "aborted",
      }),
    },
  );
  expect(await screen.findByTestId("cycle-last-started")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "サイクルを止める" })).toBeNull();
});

it("AC-139〜141: 走っていないとき、終わった理由を 7 つの値それぞれの言葉で出す", async () => {
  const expected: Record<string, string> = {
    settled: "進められるものが無くなった",
    budget: "費用の上限に達した",
    stopped: "止めた",
    rate_limited: "利用上限に達した",
    stalled: "進まなくなった",
    error: "エラーで終わった",
    interrupted: "中断された",
  };
  expect(CYCLE_END_REASON_CODES.map((c) => cycleEndReasonText(c))).toEqual(
    CYCLE_END_REASON_CODES.map((c) => expected[c]),
  );
  await openAgents([agent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({
      state: "idle",
      running: null,
      last_ended_at: ENDED,
      last_result: "aborted",
      last_end_reason: "stopped",
    }),
  });
  expect(await screen.findByTestId("cycle-last-end-reason")).toHaveTextContent(
    "止めた",
  );
  expect(screen.queryByTestId("cycle-budget-guide")).toBeNull();
});

it("AC-142: 終わった理由が費用の上限のとき、続け方の案内が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({
      state: "idle",
      running: null,
      last_ended_at: ENDED,
      last_result: "aborted",
      last_end_reason: "budget",
    }),
  });
  expect(await screen.findByTestId("cycle-budget-guide")).toHaveTextContent(
    "続きから進みます",
  );
});

const ACTIONS = [
  {
    at: "2026-10-17T02:10:00Z",
    challenge_id: "C-1",
    challenge_title: "課題の題名",
    findings: "条件を変えて確かめる必要がある",
    hand: "reverify",
    id: 2,
    reject_reason: null,
    result: "executed",
    task_id: "T-1",
    task_title: "タスクの題名",
  },
  {
    at: "2026-10-17T02:05:00Z",
    challenge_id: "C-2",
    challenge_title: "別の課題",
    findings: "",
    hand: null,
    id: 1,
    reject_reason: "owner_answered",
    result: "rejected",
    task_id: null,
    task_title: null,
  },
];

it("AC-143・AC-144: スーパーバイザーの記録を、種類・題名・調べたこと・結果つきで出し、行からタスク（無ければ課題）へ移れる", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({ supervisor_actions: ACTIONS }),
  });
  const rows = await screen.findAllByTestId("cycle-supervisor-row");
  expect(rows).toHaveLength(2);
  expect(rows[0]).toHaveTextContent("検証し直す");
  expect(rows[0]).toHaveTextContent("課題の題名 / タスクの題名");
  expect(rows[0]).toHaveTextContent("条件を変えて確かめる必要がある");
  expect(rows[0]).toHaveTextContent("成った");
  expect(
    (rows[0] as HTMLElement).querySelector("a")?.getAttribute("href"),
  ).toContain("T-1");
  expect(rows[1]).toHaveTextContent("別の課題");
  expect(rows[1]).toHaveTextContent("断られた");
  expect(rows[1]).toHaveTextContent("あなたが先に回答していた");
  expect(
    (rows[1] as HTMLElement).querySelector("a")?.getAttribute("href"),
  ).not.toContain("T-");
});

it("AC-143: 手が 0 件のとき「このサイクルでは、スーパーバイザーの手はありません」を出す", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({}),
  });
  expect(await screen.findByTestId("cycle-supervisor-empty")).toHaveTextContent(
    "このサイクルでは、スーパーバイザーの手はありません",
  );
});

it("AC-146: 費用の内訳は、種類ごとの額を写して出す", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({
      costs: [
        { cost_usd: 1.5, kind: "judgment", role: "J2" },
        { cost_usd: 5, kind: "delegate", role: null },
        { cost_usd: 0.4, kind: "judgment", role: "SV" },
      ],
    }),
  });
  const rows = await screen.findAllByTestId("cycle-cost-row");
  expect(rows[0]).toHaveTextContent("計画を作る判断点（J2）: 1.5 USD");
  expect(rows[1]).toHaveTextContent("委譲: 5 USD");
  expect(rows[2]).toHaveTextContent("スーパーバイザーの判断点（SV）: 0.4 USD");
});

it("時間の内訳・段ごとの結果・振り返り・「いまその段にいる件数」は出さない", async () => {
  await openAgents([runningAgent], "#/a/alpha/cycle", {
    [URL_CYCLE]: screenBody({}),
  });
  await screen.findByTestId("cycle-structure");
  expect(
    within(body()).queryByText(/時間の内訳|振り返り|いまその段/),
  ).toBeNull();
});
