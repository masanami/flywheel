import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type { Activity, ShowCardResponse, ShowRun } from "../api/types.gen.ts";
import { bodies, show } from "../testing/detailFixture.ts";
import { FakeEventSource, installFakes } from "../testing/fakes.ts";

// 課題の詳細の「経緯」（#809）。完了条件の 1 つずつを、名前の AC-<番号> が直接確かめる。
// 幅 360px のブラウザの試験（AC-435）は web/e2e/history.spec.ts にある。

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

const BASE = "/api/v1/workspaces/alpha";
const HISTORY_HASH = "#/a/alpha/challenges/C-1/history";

const act1 = (p: Partial<Activity> = {}): Activity => ({
  action: "plan",
  actor: "owner",
  after: null,
  at: "2026-11-01T10:00:00Z",
  before: null,
  channel: "cli",
  entity: "challenge",
  entity_id: "C-1",
  verification: "none",
  ...p,
});

const run = (p: Partial<ShowRun> = {}): ShowRun => ({
  challenge_id: "C-1",
  cost_source: "claude",
  cost_usd: 1.5,
  cycle_budget_usd: 20,
  cycle_id: "Y-1",
  ended_at: "2026-11-01T10:30:00Z",
  error: null,
  fix_pending_ref: null,
  flywheel_version: null,
  id: "R-1",
  judgment: null,
  kind: "delegate",
  max_budget_usd: 10,
  rate_limited: false,
  result: "succeeded",
  session_id: null,
  started_at: "2026-11-01T10:00:00Z",
  task_id: null,
  ...p,
});

const task = (
  id: string,
  title: string,
): ShowCardResponse["tasks"][number] => ({
  added: null,
  covers: [],
  lands_spec: false,
  flow_reason: null,
  planned_flow: null,
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
  status: "running",
  title,
});

async function open(
  activities: Activity[],
  over: Parameters<typeof show>[0] = {},
  hash = HISTORY_HASH,
) {
  window.location.hash = hash;
  const ctx = installFakes(
    bodies(show(over), { [`${BASE}/log?challenge=C-1`]: { activities } }),
  );
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const root = await screen.findByTestId("challenge-detail");
  return { root, ...ctx };
}

const rows = (root: HTMLElement) =>
  within(root).queryAllByTestId("history-row");
const whatOf = (root: HTMLElement) =>
  rows(root).map((r) => within(r).getByTestId("history-what").textContent);
const pick = (root: HTMLElement, name: string) =>
  fireEvent.click(within(root).getByRole("button", { name }));
const firstRow = async (root: HTMLElement) =>
  (await within(root).findAllByTestId("history-row"))[0] as HTMLElement;

it("AC-255 #809: 「経緯」を押すと、URL のハッシュが #/a/<名前>/challenges/<C>/history になる", async () => {
  const { root } = await open([], {}, "#/a/alpha/challenges/C-1");
  fireEvent.click(within(root).getByRole("link", { name: "経緯" }));
  await waitFor(() =>
    expect(window.location.hash).toBe("#/a/alpha/challenges/C-1/history"),
  );
  expect(await within(root).findByTestId("detail-history")).toBeInTheDocument();
});

it("AC-256 #809: 「経緯」を開くと、作業ログの API が課題の ID を付けて呼ばれる。開く前は呼ばれない", async () => {
  const { requests } = await open([], {}, "#/a/alpha/challenges/C-1");
  expect(requests.some((r) => r.url.includes("/log"))).toBe(false);
  fireEvent.click(screen.getByRole("link", { name: "経緯" }));
  await screen.findByTestId("detail-history");
  const logs = requests.filter((r) => r.url.includes("/log"));
  expect(logs.map((r) => r.url)).toEqual([`${BASE}/log?challenge=C-1`]);
  expect(logs[0]?.method).toBe("GET");
});

it("AC-257 #809: 経緯に、作業ログの記録の行が出る", async () => {
  const { root } = await open([act1({ action: "plan" })]);
  await firstRow(root);
  expect(whatOf(root)).toEqual(["計画を作った"]);
});

it("AC-258 #809: 経緯に、実行の記録（課題の詳細の runs）の行が出る", async () => {
  const { root } = await open([], { runs: [run({ id: "R-9" })] });
  await firstRow(root);
  expect(rows(root)).toHaveLength(1);
  expect(whatOf(root)).toEqual(["委譲"]);
});

it("AC-259 #809: 作業ログ（t2）と実行 2 つ（t1・t3）は、t3・t2・t1 の順に上から出る", async () => {
  const { root } = await open(
    [act1({ action: "approve", at: "2026-11-01T11:00:00Z" })],
    {
      runs: [
        run({
          id: "R-3",
          started_at: "2026-11-01T12:00:00Z",
          judgment: "J5",
          kind: "judgment",
        }),
        run({
          id: "R-1",
          started_at: "2026-11-01T10:00:00Z",
          kind: "delegate",
        }),
      ],
    },
  );
  await firstRow(root);
  expect(whatOf(root)).toEqual([
    "完了を検証する判断点（J5）",
    "承認した",
    "委譲",
  ]);
});

it("AC-260 #809: タスクの記録の行に、そのタスクの題名が出る", async () => {
  const { root } = await open(
    [act1({ entity: "task", entity_id: "T-7", action: "task_status" })],
    { tasks: [task("T-7", "入力を受ける")] },
  );
  const r = await firstRow(root);
  expect(within(r).getByTestId("history-task")).toHaveTextContent(
    "入力を受ける",
  );
});

it("#809: タスクを作った記録は「課題を作った」ではなく「タスクを足した」と出る", async () => {
  const { root } = await open([
    act1({ entity: "task", entity_id: "T-7", action: "create" }),
  ]);
  const r = await firstRow(root);
  expect(within(r).getByTestId("history-what")).toHaveTextContent(
    "タスクを足した",
  );
});

it("AC-261 #809: 経路が ui の承認の記録の行に「あなた（画面）」が出る", async () => {
  const { root } = await open([
    act1({ action: "approve", channel: "ui", verification: "ui_session" }),
  ]);
  const r = await firstRow(root);
  expect(within(r).getByTestId("history-who")).toHaveTextContent(
    "あなた（画面）",
  );
});

it("AC-262 #809: 本人確認の方式が relayed の行に「あなた（対話）」と発言の要旨が出る", async () => {
  const { root } = await open([
    act1({
      action: "answer",
      verification: "relayed",
      after: { relay_quote: "英語にしてください" },
    }),
  ]);
  const r = await firstRow(root);
  expect(within(r).getByTestId("history-who")).toHaveTextContent(
    "あなた（対話）",
  );
  expect(within(r).getByTestId("history-quote")).toHaveTextContent(
    "英語にしてください",
  );
});

it("AC-263 #809: 経路が cli で方式が tty_confirm の行に「あなた（ターミナル）」が出る", async () => {
  const { root } = await open([
    act1({ action: "approve", channel: "cli", verification: "tty_confirm" }),
  ]);
  const r = await firstRow(root);
  expect(within(r).getByTestId("history-who")).toHaveTextContent(
    "あなた（ターミナル）",
  );
});

it("AC-264 #809: 経路が invoker の行の「誰が」に、経路の値 invoker がそのまま出る", async () => {
  const { root } = await open([act1({ channel: "invoker" })]);
  const r = await firstRow(root);
  expect(within(r).getByTestId("history-who").textContent).toBe("invoker");
});

const MIXED: Activity[] = [
  act1({ action: "hold", at: "2026-11-01T01:00:00Z" }),
  act1({ action: "answer", at: "2026-11-01T02:00:00Z" }),
  act1({ action: "answer_plan", at: "2026-11-01T03:00:00Z" }),
  act1({ action: "plan", at: "2026-11-01T04:00:00Z" }),
  act1({ action: "approve", at: "2026-11-01T05:00:00Z" }),
  act1({ action: "approve_hold_release", at: "2026-11-01T06:00:00Z" }),
  act1({ action: "reject", at: "2026-11-01T07:00:00Z" }),
  act1({
    action: "op_approve",
    after: { operation_id: "O-1" },
    at: "2026-11-01T08:00:00Z",
  }),
  act1({ action: "submit", at: "2026-11-01T09:00:00Z" }),
  act1({ action: "verify_met", at: "2026-11-01T09:10:00Z" }),
  act1({ action: "verify_not_met", at: "2026-11-01T09:20:00Z" }),
  act1({ action: "verify_uncertain", at: "2026-11-01T09:30:00Z" }),
  act1({ action: "mystery_action", at: "2026-11-01T09:40:00Z" }),
];

it("AC-265 #809: 「問いと回答」は hold・answer・answer_plan の行だけを出す", async () => {
  const { root } = await open(MIXED, { runs: [run()] });
  await firstRow(root);
  pick(root, "問いと回答");
  expect([...whatOf(root)].sort()).toEqual(
    ["保留にして、問いを上げた", "問いに答えた", "計画の問いに答えた"].sort(),
  );
});

it("AC-266 #809: 「計画と承認」は plan・approve・approve_hold_release・reject・op_approve の行を出す", async () => {
  const { root } = await open(
    MIXED.filter((a) => a.entity === "challenge"),
    { runs: [run()] },
  );
  await firstRow(root);
  pick(root, "計画と承認");
  expect([...whatOf(root)].sort()).toEqual(
    [
      "計画を作った",
      "承認した",
      "完了を承認した（反映は保留のまま）",
      "差し戻した",
      "不可逆操作を承認した",
    ].sort(),
  );
});

it("AC-267 #809: 「計画と承認」は、課題の側の op_approve の行も出す", async () => {
  const operationOnly = [
    act1({ action: "op_approve", after: { operation_id: "O-1" } }),
    act1({ action: "hold", at: "2026-11-01T11:00:00Z" }),
  ];
  const { root } = await open(operationOnly);
  await firstRow(root);
  pick(root, "計画と承認");
  expect(whatOf(root)).toEqual(["不可逆操作を承認した"]);
});

const opOf = (id: string, kind: string) => ({
  approved_head: null,
  challenge_id: "C-1",
  closed_at: null,
  closed_reason: null,
  consecutive_failures: 0,
  created_at: "",
  executed_at: null,
  id,
  kind,
  last_execution_detail: null,
  last_execution_result: null,
  merge_commit: null,
  ref: null,
  state: "approved",
  summary: "",
  superseded_by: null,
  supersedes: null,
  version: 1,
  waiting_since: null,
});

it("#933: 不可逆操作の側の承認・差し戻しの行は出さず、課題の側の行に操作の種類の語を添える", async () => {
  const { root } = await open(
    [
      act1({
        action: "approve",
        entity: "operation",
        entity_id: "O-1",
        at: "2026-11-01T01:00:00Z",
      }),
      act1({
        action: "op_approve",
        after: { operation_id: "O-1" },
        at: "2026-11-01T01:00:00Z",
      }),
      act1({
        action: "reject",
        entity: "operation",
        entity_id: "O-2",
        at: "2026-11-01T02:00:00Z",
      }),
      act1({
        action: "op_reject",
        after: { operation_id: "O-2" },
        at: "2026-11-01T02:00:00Z",
      }),
    ],
    { operations: [opOf("O-1", "release"), opOf("O-2", "delete")] },
  );
  await firstRow(root);
  expect(whatOf(root).sort()).toEqual([
    "不可逆操作を差し戻した（削除）",
    "不可逆操作を承認した（反映）",
  ]);
});

it("#933: 対になる課題の側の行が無い不可逆操作の側の承認・差し戻しの行は、消えずに出る（種類の語つき）", async () => {
  const { root } = await open(
    [
      act1({
        action: "approve",
        entity: "operation",
        entity_id: "O-1",
        at: "2026-11-01T01:00:00Z",
      }),
      act1({
        action: "reject",
        entity: "operation",
        entity_id: "O-2",
        at: "2026-11-01T02:00:00Z",
      }),
      // 別の操作の課題の側の行は、O-1・O-2 の対にならない。
      act1({
        action: "op_approve",
        after: { operation_id: "O-3" },
        at: "2026-11-01T03:00:00Z",
      }),
    ],
    {
      operations: [
        opOf("O-1", "release"),
        opOf("O-2", "delete"),
        opOf("O-3", "release"),
      ],
    },
  );
  await firstRow(root);
  expect(whatOf(root).sort()).toEqual(
    [
      "不可逆操作を承認した（反映）",
      "不可逆操作を差し戻した（削除）",
      "不可逆操作を承認した（反映）",
    ].sort(),
  );
});

it("#933: 対になる課題の側の行が無い不可逆操作の側の承認・差し戻しの行は、絞り「計画と承認」でも出る", async () => {
  const { root } = await open(
    [
      act1({
        action: "approve",
        entity: "operation",
        entity_id: "O-1",
        at: "2026-11-01T01:00:00Z",
      }),
      act1({
        action: "reject",
        entity: "operation",
        entity_id: "O-2",
        at: "2026-11-01T02:00:00Z",
      }),
      act1({
        action: "op_add",
        entity: "operation",
        entity_id: "O-1",
        at: "2026-11-01T00:00:00Z",
      }),
    ],
    { operations: [opOf("O-1", "release"), opOf("O-2", "delete")] },
  );
  await firstRow(root);
  pick(root, "計画と承認");
  expect(whatOf(root).sort()).toEqual(
    ["不可逆操作を承認した（反映）", "不可逆操作を差し戻した（削除）"].sort(),
  );
});

it("#933: 突き合わせて見つからない operation_id の行は、種類の語なしで出る。不可逆操作の登録の行は変わらない", async () => {
  const { root } = await open(
    [
      act1({
        action: "op_approve",
        after: { operation_id: "O-9" },
        at: "2026-11-01T01:00:00Z",
      }),
      act1({
        action: "op_add",
        entity: "operation",
        entity_id: "O-9",
        at: "2026-11-01T02:00:00Z",
      }),
    ],
    { operations: [opOf("O-1", "release")] },
  );
  await firstRow(root);
  expect(whatOf(root).sort()).toEqual(
    ["不可逆操作を足した", "不可逆操作を承認した"].sort(),
  );
});

it("AC-268 #809: 「完了の検証」は submit・verify_met・verify_not_met・verify_uncertain の行だけを出す", async () => {
  const { root } = await open(MIXED, { runs: [run()] });
  await firstRow(root);
  pick(root, "完了の検証");
  expect(rows(root)).toHaveLength(4);
  expect([...whatOf(root)].sort()).toEqual(
    [
      "完了の検証へ出した",
      "完了条件を満たしていると確かめた",
      "完了条件を満たしていないと確かめた",
      "完了条件を満たしているか判断できなかった",
    ].sort(),
  );
});

it("AC-269 #809: 「実行」は実行の記録の行だけを出す（作業ログの記録は出ない）", async () => {
  const { root } = await open(MIXED, {
    runs: [
      run({ id: "R-1" }),
      run({ id: "R-2", kind: "judgment", judgment: "J2" }),
    ],
  });
  await firstRow(root);
  pick(root, "実行");
  expect(rows(root)).toHaveLength(2);
  expect([...whatOf(root)].sort()).toEqual(
    ["委譲", "計画を作る判断点（J2）"].sort(),
  );
});

it("AC-270 #809: 画面の知らない action は、その値がそのまま出る", async () => {
  const { root } = await open([act1({ action: "mystery_action" })]);
  await firstRow(root);
  expect(whatOf(root)).toEqual(["mystery_action"]);
});

it("AC-271 #809: 知らない action の行は、絞りが「すべて」のときだけ出る", async () => {
  const { root } = await open(MIXED, { runs: [run()] });
  await firstRow(root);
  expect(whatOf(root)).toContain("mystery_action");
  for (const name of ["計画と承認", "問いと回答", "完了の検証", "実行"]) {
    pick(root, name);
    expect(whatOf(root)).not.toContain("mystery_action");
  }
  pick(root, "すべて");
  expect(whatOf(root)).toContain("mystery_action");
});

it("AC-272 #809: 実行の記録の行に、何の実行か（判断点の名前か委譲）・結果・費用が出る", async () => {
  const { root } = await open([], {
    runs: [
      run({
        id: "R-1",
        kind: "judgment",
        judgment: "J2",
        result: "succeeded",
        cost_usd: 0.42,
        started_at: "2026-11-01T12:00:00Z",
      }),
      run({
        id: "R-2",
        kind: "delegate",
        result: "errored",
        cost_usd: 3.1,
        started_at: "2026-11-01T11:00:00Z",
      }),
    ],
  });
  await firstRow(root);
  const [j, d] = rows(root) as [HTMLElement, HTMLElement];
  expect(within(j).getByTestId("history-what")).toHaveTextContent(
    "計画を作る判断点（J2）",
  );
  expect(within(j).getByTestId("history-detail")).toHaveTextContent(
    "成功・0.42 USD",
  );
  expect(within(d).getByTestId("history-what")).toHaveTextContent("委譲");
  expect(within(d).getByTestId("history-detail")).toHaveTextContent(
    "失敗・3.1 USD",
  );
});

it("#809: 経緯を開いても fetch は GET だけで、form 要素も入力欄も無い", async () => {
  const { root, requests } = await open([act1()], { runs: [run()] });
  await firstRow(root);
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  expect(root.querySelector("form, input, textarea")).toBeNull();
});

it("#809: 作業ログの読み込みに失敗したら、エラーを出す", async () => {
  window.location.hash = HISTORY_HASH;
  installFakes(bodies(show()));
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const root = await screen.findByTestId("challenge-detail");
  await waitFor(() =>
    expect(within(root).getByRole("alert")).toBeInTheDocument(),
  );
});

// #814 掃除 8: ストアに実際にある記録の語。足した語ごとに、その語が出ることを確かめる。
const ADDED_WORDS: [string, string, string][] = [
  ["challenge", "hold", "保留にして、問いを上げた"],
  ["operation", "op_add", "不可逆操作を足した"],
  ["challenge", "op_approve", "不可逆操作を承認した"],
  ["challenge", "op_reject", "不可逆操作を差し戻した"],
  [
    "challenge",
    "complete_direct",
    "オーケストレーターが直接行った作業を、完了として記録した",
  ],
  ["challenge", "reopen_for_release", "反映をやり直すため、完了から戻した"],
  ["challenge", "ingest_create", "取り込み元から課題を作った"],
  ["challenge", "ingest_update", "取り込み元の変更を取り込んだ"],
  ["challenge", "bind", "取り込み元と対応付けた"],
  ["challenge", "unbind", "取り込み元との対応を外した"],
  ["challenge", "edit", "課題を書き換えた"],
  ["challenge", "improvement", "改修の提案から課題を作った"],
  ["challenge", "upstream_read", "取り込み元の更新を読んだと記録した"],
  [
    "challenge",
    "upstream_observation_change",
    "取り込み元のコメント数・更新日時の観測が変わった",
  ],
  ["challenge", "upstream_state_change", "取り込み元の状態が変わった"],
  [
    "challenge",
    "policy_state_change",
    "取り込みの方針の範囲内か外かが変わった",
  ],
];

it.each(ADDED_WORDS)(
  "#814: 記録 %s:%s は「%s」と出る",
  async (entity, action, word) => {
    const { root } = await open([act1({ entity, entity_id: "C-1", action })]);
    const r = await firstRow(root);
    expect(within(r).getByTestId("history-what")).toHaveTextContent(word);
    expect(within(r).getByTestId("history-what").textContent).toBe(word);
  },
);

it("#814: 足した語は、絞りの種類の対応を変えない（不可逆操作の承認・差し戻しは「計画と承認」、そのほかの足した語は「すべて」だけ）", async () => {
  const { root } = await open(
    ADDED_WORDS.map(([entity, action], i) =>
      act1({
        entity,
        entity_id: "C-1",
        action,
        at: `2026-11-01T0${(i % 9) + 1}:00:00Z`,
      }),
    ),
  );
  await firstRow(root);
  pick(root, "計画と承認");
  expect([...whatOf(root)].sort()).toEqual(
    ["不可逆操作を承認した", "不可逆操作を差し戻した"].sort(),
  );
  pick(root, "問いと回答");
  expect(whatOf(root)).toEqual(["保留にして、問いを上げた"]);
  pick(root, "完了の検証");
  expect(whatOf(root)).toEqual([]);
});

it("#814: 表に無い記録の値と、constructor のような継承されたキーは、値のまま出る", async () => {
  const { root } = await open([
    act1({ action: "constructor" }),
    act1({ action: "toString", at: "2026-11-01T02:00:00Z" }),
  ]);
  await firstRow(root);
  expect([...whatOf(root)].sort()).toEqual(["constructor", "toString"]);
});

// 区切り S4d-2 の T8: 経緯の語。生の値のまま出さない。
it("AC-168 #1185: スーパーバイザーの手の出来事が、言葉で出る", async () => {
  const { root } = await open([act1({ action: "supervise" })]);
  await firstRow(root);
  expect(whatOf(root)).toEqual(["スーパーバイザーが手を打った"]);
});

it("AC-169 #1185: 問いの書き換えの出来事が、言葉で出る", async () => {
  const { root } = await open([act1({ action: "hold_edit" })]);
  await firstRow(root);
  expect(whatOf(root)).toEqual(["問いを書き換えた"]);
});

it("AC-170 #1185: 上限の変更の出来事が、言葉で出る", async () => {
  const { root } = await open([act1({ action: "budget_limit" })]);
  await firstRow(root);
  expect(whatOf(root)).toEqual(["課題の費用の上限を変えた"]);
});

it("AC-171 #1185: 足す依頼の出来事（作る・タスクを足す・状態の変更）が、言葉で出る", async () => {
  const { root } = await open([
    act1({
      entity: "task_addition",
      entity_id: "TA-1",
      action: "create",
      at: "2026-11-01T10:00:00Z",
    }),
    act1({
      entity: "task_addition",
      entity_id: "TA-1",
      action: "add_tasks",
      at: "2026-11-01T11:00:00Z",
    }),
    act1({
      entity: "task_addition",
      entity_id: "TA-1",
      action: "state",
      at: "2026-11-01T12:00:00Z",
    }),
  ]);
  await firstRow(root);
  expect(whatOf(root)).toEqual([
    "タスクを足す依頼の状態が変わった",
    "計画の後にタスクを足した",
    "タスクを足す依頼を作った",
  ]);
});
