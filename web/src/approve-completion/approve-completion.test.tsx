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
import {
  type ApprovalSummary,
  CONDITIONS_AFTER_RELEASE_HEADINGS,
} from "../api/types.gen.ts";
import {
  APPROVAL_REJECT_NOTE,
  COMPLETION_APPROVED_NOTE,
  COMPLETION_BUTTON_NOTE,
  COMPLETION_RELEASE_NOTE,
} from "../guidance.ts";
import { RELEASE_HEAD_MISSING_TEXT } from "../labels.ts";
import {
  FakeEventSource,
  type FakeResponder,
  type Recorded,
  fixtures,
  installFakes,
} from "../testing/fakes.ts";
import {
  COMPLETION_UPDATED_TEXT,
  NOT_AWAITING_COMPLETION_TEXT,
  NO_OPEN_FOLLOWUPS_TEXT,
} from "./ApproveCompletionScreen.tsx";

beforeEach(() => {
  const m = document.createElement("meta");
  m.name = "flywheel-csrf";
  m.content = "tok-123";
  document.head.append(m);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.head.innerHTML = "";
  window.location.hash = "";
  cleanup();
});

const BASE = "/api/v1/workspaces/alpha";
const APPROVAL = `${BASE}/challenges/C-1/approval`;
const APPROVE = `POST ${BASE}/challenges/C-1/approve`;

const STAGES = {
  actual: null,
  expected: null,
  figure: null,
  issues: null,
  issues_answer: null,
  non_goals: null,
};

function summary(over: Partial<ApprovalSummary> = {}): {
  approval: ApprovalSummary;
} {
  return {
    approval: {
      challenge_id: "C-1",
      conditions_after_release: [],
      conditions_after_release_heading: "",
      conflict_fixes: [],
      direct_completion: null,
      done_criteria: "完了条件の本文",
      followups: [],
      integration_ref: null,
      kind: "completion",
      mergeability_note: "",
      other_pending_operations: [],
      pairs: [],
      plan: null,
      release_held: true,
      releases: [],
      stages: STAGES,
      tasks: [],
      title: "題名",
      uses_integration: null,
      version: 3,
      ...over,
    },
  };
}

function task(id: string, over: Record<string, unknown> = {}) {
  return {
    artifacts: [],
    body: "",
    budget_impl_usd: 10,
    budget_review_usd: 2,
    delegate: null,
    depends_on: [],
    done_criteria: "",
    facts: {},
    id,
    landing: null,
    non_goals: [],
    operation: "",
    plan_version: 1,
    repo: "r",
    source_key: null,
    spent_usd: 3.5,
    status: "done",
    title: `タスク ${id}`,
    verification: null,
    ...over,
  };
}

function followup(id: string, status: string) {
  return {
    challenge_id: "C-1",
    created_at: "",
    detail: "",
    id,
    note: null,
    promoted_challenge_id: null,
    reason: null,
    source: "agent",
    status,
    title: `フォローアップ ${id}`,
    updated_at: "",
  };
}

interface Over {
  status?: string;
  actions?: string[];
  tasks?: ReturnType<typeof task>[];
  // 木（最新の版）に載せるタスクの ID。省略すると tasks の全部。
  treeTaskIds?: string[];
  followups?: ReturnType<typeof followup>[];
  challenge?: Record<string, unknown>;
  conflictFixes?: Record<string, unknown>[];
}

function showBody(o: Over = {}) {
  const status = o.status ?? "awaiting_completion_approval";
  const card = {
    context: null,
    headline: null,
    modifiers: [],
    next_human_actions: o.actions ?? ["approve_completion"],
    state: status,
  };
  const challenge = {
    created_at: "",
    dependencies: [],
    dependents: [],
    description: "説明",
    done_criteria: "完了条件",
    followup_of: null,
    headline: null,
    id: "C-1",
    improvement: null,
    integration_ref: null,
    priority: null,
    reporter: "me",
    status,
    status_label: "",
    title: "題名",
    updated_at: "",
    urgency: null,
    uses_integration: null,
    version: 3,
    ...o.challenge,
  };
  const tasks = o.tasks ?? [];
  return {
    card,
    challenge,
    body: {
      approvals: [],
      card,
      challenge,
      conflict_fixes: o.conflictFixes ?? [],
      direct_completions: [],
      followups: o.followups ?? [],
      holds: [],
      operations: [],
      pairs: [],
      plans: [],
      runs: [],
      source_binding: null,
      source_children: [],
      stages: STAGES,
      tasks,
      tree: {
        budget_usd: null,
        children: [],
        cost_usd: 0,
        dependencies: [],
        followups_open: 0,
        id: "C-1",
        next_human_actions: [],
        parent: null,
        status: "in_progress",
        status_label: "着手中",
        tasks: tasks
          .filter((t) => (o.treeTaskIds ?? [t.id]).includes(t.id))
          .map((t) => ({
            cost_usd: 0,
            depends_on: [],
            id: t.id,
            key: t.id,
            phase: "done",
            progress: null,
            status: "done",
            title: t.title,
          })),
        tasks_done: 0,
        tasks_total: tasks.filter((t) =>
          (o.treeTaskIds ?? [t.id]).includes(t.id),
        ).length,
        title: "題名",
        updated_at: "2026-11-01T00:00:00.000Z",
        uses_integration: null,
      },
      waiting: { prerequisites: [] },
    },
  };
}

// Over の値を、要約の応答に入る形（完了承認画面の値は要約 1 つで返る）へ写す。
// 木（最新の版）にあるタスクは木の状態、木に無い前の版の済みのタスクだけを並べる（core.CompletionTask と同じ規則）。
function approvalFromOver(o: Over): Partial<ApprovalSummary> {
  const inTree = (id: string) =>
    (o.treeTaskIds ?? (o.tasks ?? []).map((t) => t.id)).includes(id);
  return {
    conflict_fixes: (o.conflictFixes ??
      []) as unknown as ApprovalSummary["conflict_fixes"],
    followups: (o.followups ?? []).filter((f) => f.status === "open"),
    integration_ref:
      (o.challenge?.integration_ref as string | null | undefined) ?? null,
    tasks: (o.tasks ?? []).flatMap((t) =>
      inTree(t.id) || t.status === "done"
        ? [
            {
              budget_usd: t.budget_impl_usd + t.budget_review_usd,
              id: t.id,
              in_tree: inTree(t.id),
              phase: "done",
              spent_usd: t.spent_usd,
              title: t.title,
            },
          ]
        : [],
    ),
    uses_integration:
      (o.challenge?.uses_integration as boolean | null | undefined) ?? null,
  };
}

function setup(o: Over = {}, extra: Record<string, unknown> = {}) {
  const f = fixtures();
  const s = showBody(o);
  f[`${BASE}/challenges`] = {
    challenges: [{ ...s.challenge, card: s.card }],
  };
  f[`${BASE}/challenges/C-1`] = s.body;
  f[`${BASE}/log?challenge=C-1`] = { activities: [] };
  return installFakes({
    ...f,
    [APPROVAL]: summary(approvalFromOver(o)),
    ...extra,
  });
}

async function open(): Promise<HTMLElement> {
  window.location.hash = "#/a/alpha/challenges/C-1/approve-completion";
  render(<App />);
  act(() => FakeEventSource.latest.open());
  return await screen.findByTestId("approve-completion");
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status });
const button = (m: HTMLElement) =>
  within(m).findByRole("button", { name: "完了を承認する" });

it("AC-1: 行き先が approve-completion のとき、完了承認画面が出る", async () => {
  setup();
  const m = await open();
  expect(
    within(m).getByRole("heading", { level: 2, name: "完了の承認: 題名" }),
  ).toBeTruthy();
});

it("AC-5: 完了したら見えるものと実物の対が pairs の件数だけ出る", async () => {
  setup(
    {},
    {
      [APPROVAL]: summary({
        pairs: [
          {
            expected: { kind: "ui", scene: "s1", before: "b", after: "予想1" },
            actual: { scene: "s1", after: "実物1" },
          },
          {
            expected: { kind: "ui", scene: "s2", before: "b", after: "予想2" },
            actual: null,
          },
        ],
      }),
    },
  );
  const m = await open();
  const checks = within(m).getByTestId("completion-checks");
  expect(checks.textContent).toContain("完了したら見えるもの");
  expect(checks.querySelectorAll("[data-pair]")).toHaveLength(2);
  expect(checks.textContent).toContain("実物1");
});

it("AC-6: 反映の後に確かめる条件が出る。無ければ出さない", async () => {
  setup(
    {},
    {
      [APPROVAL]: summary({
        conditions_after_release: ["本番で画面に出る"],
        conditions_after_release_heading: "反映で満たされる条件:",
      }),
    },
  );
  const m = await open();
  const box = within(m).getByTestId("approval-conditions-after-release");
  expect(within(box).getByText("本番で画面に出る")).toBeTruthy();
  cleanup();
  setup();
  const m2 = await open();
  expect(
    within(m2).queryByTestId("approval-conditions-after-release"),
  ).toBeNull();
});

it("AC-7: 反映の操作ごとに要約と成果物へのリンクが出る（http・https だけがリンク）", async () => {
  setup(
    {},
    {
      [APPROVAL]: summary({
        releases: [
          {
            head_sha: "abc123",
            id: "O-1",
            ref: "https://example.com/r/9",
            summary: "v1.0 を出す",
          },
          {
            head_sha: null,
            id: "O-3",
            ref: "javascript:alert(1)",
            summary: "別の反映",
          },
        ],
      }),
    },
  );
  const m = await open();
  const r1 = m.querySelector('[data-release="O-1"]') as HTMLElement;
  expect(r1.textContent).toContain("v1.0 を出す");
  expect(r1.textContent).toContain("abc123");
  expect(within(r1).getByRole("link")).toHaveAttribute(
    "href",
    "https://example.com/r/9",
  );
  const r3 = m.querySelector('[data-release="O-3"]') as HTMLElement;
  expect(r3.textContent).toContain("別の反映");
  expect(within(r3).queryByRole("link")).toBeNull();
});

it("反映の操作の head（head_sha）が null のときは「取得できませんでした」が出て、あるときは出ない", async () => {
  setup(
    {},
    {
      [APPROVAL]: summary({
        releases: [
          { head_sha: "abc123", id: "O-1", ref: null, summary: "あり" },
          { head_sha: null, id: "O-3", ref: null, summary: "なし" },
        ],
      }),
    },
  );
  const m = await open();
  const r1 = m.querySelector('[data-release="O-1"]') as HTMLElement;
  const r3 = m.querySelector('[data-release="O-3"]') as HTMLElement;
  expect(r1.textContent).toContain("abc123");
  expect(r1.textContent).not.toContain(RELEASE_HEAD_MISSING_TEXT);
  expect(r3.textContent).toContain(RELEASE_HEAD_MISSING_TEXT);
  expect(r3.textContent).toContain("取得できませんでした");
});

it("AC-8: 統合先を使う課題では統合先の識別が出る", async () => {
  setup({
    challenge: { uses_integration: true, integration_ref: "feat/int-1" },
  });
  const m = await open();
  expect(within(m).getByTestId("completion-integration").textContent).toContain(
    "feat/int-1",
  );
});

it("AC-9: 統合先を使わない課題では統合先の行が出ない", async () => {
  setup({ challenge: { uses_integration: false, integration_ref: null } });
  const m = await open();
  expect(within(m).queryByTestId("completion-integration")).toBeNull();
  expect(m.textContent).not.toContain("統合先");
});

it("AC-10・AC-11: 同時には承認されない不可逆操作が出て、種類は語で、内部の値は出ない", async () => {
  setup(
    {},
    {
      [APPROVAL]: summary({
        other_pending_operations: [
          { id: "O-2", kind: "delete", summary: "古いデータ" },
        ],
      }),
    },
  );
  const m = await open();
  const row = within(m).getByTestId("completion-other-operations");
  expect(row.textContent).toContain("古いデータ");
  expect(row.textContent).toContain("削除");
  expect(row.textContent).not.toContain("delete");
});

it("AC-12〜AC-14: タスクごとに題名・状態の語・使った額と枠が出て、題名はタスクの詳細へのリンク", async () => {
  setup({ tasks: [task("T-1"), task("T-2", { spent_usd: 0 })] });
  const m = await open();
  const t1 = m.querySelector('[data-task="T-1"]') as HTMLElement;
  expect(within(t1).getByRole("link", { name: "タスク T-1" })).toHaveAttribute(
    "href",
    "#/a/alpha/challenges/C-1/tasks/T-1",
  );
  expect(within(t1).getByTestId("completion-task-state").textContent).toBe(
    "済み",
  );
  expect(within(t1).getByTestId("completion-task-cost").textContent).toContain(
    "3.5",
  );
  expect(within(t1).getByTestId("completion-task-cost").textContent).toContain(
    "12",
  );
  expect(m.querySelectorAll("[data-task]")).toHaveLength(2);
});

it("計画を作り直した課題では、最新の版の木に無い前の版の未完・取りやめのタスクは、タスクの一覧に出ない", async () => {
  setup({
    tasks: [
      task("T-1", { plan_version: 1, status: "cancelled" }),
      task("T-3", { plan_version: 1, status: "in_progress" }),
      task("T-2", { plan_version: 2, title: "新しいタスク" }),
    ],
    treeTaskIds: ["T-2"],
  });
  const m = await open();
  const box = within(m).getByTestId("completion-tasks");
  expect(box.querySelectorAll("[data-task]")).toHaveLength(1);
  expect(box.querySelector('[data-task="T-1"]')).toBeNull();
  expect(box.querySelector('[data-task="T-3"]')).toBeNull();
  expect(within(box).getByRole("link", { name: "新しいタスク" })).toBeTruthy();
  expect(box.textContent).not.toContain("—");
});

it("計画を作り直しても残る前の版の済みのタスクは、一覧に「済み」と使った額・枠つきで出る（詳細は開けないのでリンクにしない）", async () => {
  setup({
    tasks: [
      task("T-1", { plan_version: 1, status: "done", title: "前の版の済み" }),
      task("T-2", { plan_version: 2, title: "新しいタスク" }),
    ],
    treeTaskIds: ["T-2"],
  });
  const m = await open();
  const box = within(m).getByTestId("completion-tasks");
  const old = box.querySelector('[data-task="T-1"]') as HTMLElement;
  expect(old.textContent).toContain("前の版の済み");
  expect(within(old).queryByRole("link")).toBeNull();
  expect(within(old).getByTestId("completion-task-state").textContent).toBe(
    "済み",
  );
  expect(within(old).getByTestId("completion-task-cost").textContent).toContain(
    "3.5",
  );
  expect(box.textContent).not.toContain("—");
});

it("AC-15・AC-17: 未のフォローアップの件数と題名が出て、あってもボタンは出る", async () => {
  setup({
    followups: [
      followup("F-1", "open"),
      followup("F-2", "open"),
      followup("F-3", "closed"),
    ],
  });
  const m = await open();
  const box = within(m).getByTestId("completion-followups");
  expect(box.textContent).toContain("2 件");
  expect(box.textContent).toContain("フォローアップ F-1");
  expect(box.textContent).toContain("フォローアップ F-2");
  expect(box.textContent).not.toContain("フォローアップ F-3");
  await button(m);
});

it("AC-16: 未のフォローアップが 0 件なら「未のフォローアップはありません」が出る", async () => {
  setup({ followups: [followup("F-3", "closed")] });
  const m = await open();
  expect(NO_OPEN_FOLLOWUPS_TEXT).toBe("未のフォローアップはありません");
  expect(within(m).getByText("未のフォローアップはありません")).toBeTruthy();
});

it("AC-18・AC-19・AC-22・AC-23・AC-31・AC-32: ボタンの文言・完了だけを記録する旨・対話の案内があり、コマンドの例と差し戻しのボタンは無い", async () => {
  setup();
  const m = await open();
  const b = await button(m);
  expect(b.textContent).toBe("完了を承認する");
  expect(within(m).getAllByRole("button")).toHaveLength(1);
  const note = within(m).getByTestId("completion-button-note");
  expect(note.textContent).toBe(COMPLETION_BUTTON_NOTE);
  expect(
    b.compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(within(m).getByTestId("completion-release-note").textContent).toBe(
    COMPLETION_RELEASE_NOTE,
  );
  expect(within(m).getByTestId("completion-reject-note").textContent).toBe(
    APPROVAL_REJECT_NOTE,
  );
  expect(m.textContent).not.toMatch(/flywheel /);
  expect(m.querySelector("pre, code[data-guide]")).toBeNull();
});

it("AC-180 #995: 承認の応答の cycle.reason が no_work → 「いま進められるものが無いので、サイクルは始めていません。」が出る", async () => {
  setup(
    { challenge: { version: 7 } },
    {
      [APPROVAL]: summary({ version: 7 }),
      [APPROVE]: () =>
        json(200, { cycle: { reason: "no_work", started: false } }),
    },
  );
  const m = await open();
  fireEvent.click(await button(m));
  const line = await screen.findByTestId(
    "cycle-start-line",
    {},
    { timeout: 2000 },
  );
  expect(line.textContent).toBe(
    "いま進められるものが無いので、サイクルは始めていません。",
  );
});

it("AC-20・AC-21・AC-24・AC-25・AC-33: ボタンは読んだ版を CSRF つきで 1 回だけ POST し、成功すると記録した旨と対話で行う旨が出る", async () => {
  const { requests } = setup(
    { challenge: { version: 7 } },
    {
      [APPROVAL]: summary({ version: 7 }),
      [APPROVE]: () =>
        json(200, { cycle: { reason: "started", started: true } }),
    },
  );
  const m = await open();
  fireEvent.click(await button(m));
  const out = await screen.findByRole("status", {}, { timeout: 2000 });
  expect(out.textContent).toBe(COMPLETION_APPROVED_NOTE);
  expect(out.textContent).toContain("承認を記録しました");
  expect(out.textContent).toContain("対話で行います");
  const posts = requests.filter((r) => r.method === "POST");
  expect(posts).toHaveLength(1);
  expect(posts[0]?.url).toBe(`${BASE}/challenges/C-1/approve`);
  expect(posts[0]?.body).toBe('{"version":7}');
  expect(posts[0]?.headers["X-Flywheel-CSRF"]).toBe("tok-123");
  // AC-33: GET と …/approve の POST だけ
  for (const r of requests) {
    if (r.method === "GET") continue;
    expect(r.method).toBe("POST");
    expect(r.url).toMatch(/\/challenges\/C-1\/approve$/);
  }
});

it("AC-26: 版の食い違いのとき、要約を読み直し、承認の要求は送り直さない。押し直すと新しい版を送る", async () => {
  let call = 0;
  const responder: FakeResponder = () => {
    call++;
    return call === 1
      ? json(409, { error: { code: "conflict", message: "版が違う" } })
      : json(200, { cycle: { reason: "started", started: true } });
  };
  let reads = 0;
  const { requests } = setup();
  const s = showBody();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase();
      const rec: Recorded = {
        url: String(input),
        method,
        headers: {},
        body: typeof init?.body === "string" ? init.body : null,
      };
      requests.push(rec);
      if (rec.url === APPROVAL) {
        reads++;
        return json(
          200,
          summary({
            version: reads === 1 ? 3 : 4,
            done_criteria: reads === 1 ? "古い条件" : "新しい条件",
          }),
        );
      }
      if (method === "POST") return responder(rec);
      const f = fixtures() as Record<string, unknown>;
      f[`${BASE}/challenges`] = {
        challenges: [{ ...s.challenge, card: s.card }],
      };
      const b = f[rec.url];
      return b === undefined ? json(404, {}) : json(200, b);
    }),
  );
  const m = await open();
  fireEvent.click(await button(m));
  await within(m).findByText(
    /最新の要約を読み直しました/,
    {},
    { timeout: 2000 },
  );
  await within(m).findByText("新しい条件");
  expect(requests.filter((r) => r.method === "POST")).toHaveLength(1);
  fireEvent.click(await button(m));
  await screen.findByText(COMPLETION_APPROVED_NOTE);
  expect(
    requests.filter((r) => r.method === "POST").map((p) => p.body),
  ).toEqual(['{"version":3}', '{"version":4}']);
});

it("版の食い違いのとき、読み直しが終わるまでボタンは押せず、通知は読み直しの後に 1 つだけ出る", async () => {
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => {
    release = r;
  });
  let reads = 0;
  const { requests } = setup();
  const s = showBody();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase();
      requests.push({
        url: String(input),
        method,
        headers: {},
        body: typeof init?.body === "string" ? init.body : null,
      });
      if (String(input) === APPROVAL) {
        reads++;
        if (reads === 2) await gate;
        return json(
          200,
          summary({
            version: reads === 1 ? 3 : 4,
            done_criteria: reads === 1 ? "古い条件" : "新しい条件",
          }),
        );
      }
      if (method === "POST") {
        return json(409, { error: { code: "conflict", message: "版が違う" } });
      }
      const f = fixtures() as Record<string, unknown>;
      f[`${BASE}/challenges`] = {
        challenges: [{ ...s.challenge, card: s.card }],
      };
      const b = f[String(input)];
      return b === undefined ? json(404, {}) : json(200, b);
    }),
  );
  const m = await open();
  fireEvent.click(await button(m));
  // 読み直しが終わるまで: ボタンは押せず、どの通知も出ない
  await waitFor(() => expect(reads).toBe(2));
  const b = await button(m);
  expect((b as HTMLButtonElement).disabled).toBe(true);
  expect(within(m).queryByText(/最新の要約を読み直しました/)).toBeNull();
  expect(within(m).queryByText(/承認の対象が更新されました/)).toBeNull();
  release();
  // 読み直しが終わったら: 食い違いの通知だけが出て、ボタンは押せる
  await within(m).findByText("新しい条件");
  await within(m).findByText(/最新の要約を読み直しました/);
  expect((await button(m)) as HTMLButtonElement).toHaveProperty(
    "disabled",
    false,
  );
  expect(within(m).queryByText(/承認の対象が更新されました/)).toBeNull();
});

it("確定ボタンは、効くセレクタのある .approval-control で包まれている", async () => {
  setup();
  const m = await open();
  const b = await button(m);
  expect(b.parentElement?.classList.contains("approval-control")).toBe(true);
  expect(b.classList.contains("approval-control-confirm")).toBe(true);
});

it.each([
  ["403 forbidden_origin", 403, "forbidden_origin"],
  ["403 だけ", 403, "something"],
  ["forbidden_origin だけ", 400, "forbidden_origin"],
])(
  "403・forbidden_origin（%s）では再読み込みを促し、再送しない",
  async (_n, status, code) => {
    const { requests } = setup(
      {},
      { [APPROVE]: () => json(status, { error: { code, message: "拒否" } }) },
    );
    const m = await open();
    fireEvent.click(await button(m));
    await within(m).findByText(/再読み込み/, {}, { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 50));
    expect(requests.filter((r) => r.method === "POST")).toHaveLength(1);
  },
);

it.each([
  ["AC-27: 計画の承認待ち", { kind: "plan" }],
  ["AC-28: 何も待っていない", { kind: "none" }],
])(
  "%s のとき、ボタンが出ず、完了の承認待ちでない旨と課題の詳細へのリンクが出る",
  async (_n, over) => {
    setup(
      { status: "in_progress", actions: [] },
      { [APPROVAL]: summary(over) },
    );
    const m = await openNot();
    expect(within(m).queryByRole("button")).toBeNull();
    expect(m.textContent).toContain(NOT_AWAITING_COMPLETION_TEXT);
    expect(
      within(m).getByRole("link", { name: "課題の詳細を開く" }),
    ).toHaveAttribute("href", "#/a/alpha/challenges/C-1");
  },
);

async function openNot(): Promise<HTMLElement> {
  window.location.hash = "#/a/alpha/challenges/C-1/approve-completion";
  render(<App />);
  act(() => FakeEventSource.latest.open());
  return await screen.findByTestId("completion-not-awaiting");
}

it("要約の API が invalid_transition を返したときも、完了の承認待ちでない旨を出す", async () => {
  setup(
    { status: "in_progress", actions: [] },
    {
      [APPROVAL]: () =>
        json(409, { error: { code: "invalid_transition", message: "x" } }),
    },
  );
  const m = await openNot();
  expect(within(m).queryByRole("button")).toBeNull();
  await waitFor(() =>
    expect(m.textContent).toContain(NOT_AWAITING_COMPLETION_TEXT),
  );
});

it("要約の API が terminal_state を返したとき（完了した課題）も、完了の承認待ちでない旨を出し、内部の英語の文は出ない", async () => {
  setup(
    { status: "done", actions: [] },
    {
      [APPROVAL]: () =>
        json(409, {
          error: {
            code: "terminal_state",
            message: "core: challenge is in a terminal state",
          },
        }),
    },
  );
  const m = await openNot();
  expect(within(m).queryByRole("button")).toBeNull();
  await waitFor(() =>
    expect(m.textContent).toContain(NOT_AWAITING_COMPLETION_TEXT),
  );
  expect(document.body.textContent).not.toContain("terminal state");
});

it("承認の口が terminal_state を返したとき、完了の承認待ちでない旨を出し、内部の英語の文は出ない", async () => {
  setup(
    {},
    {
      [APPROVE]: () =>
        json(409, {
          error: {
            code: "terminal_state",
            message: "core: challenge is in a terminal state",
          },
        }),
    },
  );
  const m = await open();
  fireEvent.click(await button(m));
  const n = await screen.findByTestId("completion-not-awaiting");
  expect(n.textContent).toContain(NOT_AWAITING_COMPLETION_TEXT);
  expect(document.body.textContent).not.toContain("terminal state");
});

it("承認の口が invalid_transition を返したとき、完了の承認待ちでない旨を出し、内部の英語の文は出ない", async () => {
  setup(
    {},
    {
      [APPROVE]: () =>
        json(409, {
          error: {
            code: "invalid_transition",
            message: "core: invalid transition",
          },
        }),
    },
  );
  const m = await open();
  fireEvent.click(await button(m));
  const n = await screen.findByTestId("completion-not-awaiting");
  expect(n.textContent).toContain(NOT_AWAITING_COMPLETION_TEXT);
  expect(document.body.textContent).not.toContain("invalid transition");
});

it("AC-2: 「いま進められること」の完了の承認待ちの行は、完了承認画面へのリンク", async () => {
  const { buildWaitingRows } = await import("../shell/waitingRows.ts");
  const rows = buildWaitingRows(
    { name: "alpha" },
    {
      budget_exhausted: [],
      challenges: [
        {
          id: "C-1",
          title: "題名",
          status: "awaiting_completion_approval",
        } as never,
      ],
      discrepancies: [],
      operations: [],
      slots: [],
      triage: [],
    },
    [],
    () => "",
  );
  expect(rows[0]?.href).toBe("#/a/alpha/challenges/C-1/approve-completion");
});

it("AC-3・AC-4: 課題の詳細の「あなたを待っているもの」には完了承認画面へのリンクがあり、承認のボタンも要約も無い", async () => {
  const { requests } = setup();
  window.location.hash = "#/a/alpha/challenges/C-1";
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const d = await screen.findByTestId("challenge-detail");
  const link = await within(d).findByRole("link", {
    name: "完了の承認の画面を開く",
    hidden: true,
  });
  expect(link).toHaveAttribute(
    "href",
    "#/a/alpha/challenges/C-1/approve-completion",
  );
  expect(
    within(d).queryByRole("button", { name: /承認/, hidden: true }),
  ).toBeNull();
  expect(within(d).queryByTestId("approval-summary")).toBeNull();
  expect(requests.some((r) => r.url === APPROVAL)).toBe(false);
});

it("完了承認画面は、承認の要約 1 つだけを読む（課題の詳細は読まない）", async () => {
  const { requests } = setup({
    tasks: [task("T-1")],
    followups: [followup("F-1", "open")],
  });
  const m = await open();
  expect(requests.filter((r) => r.url === APPROVAL)).toHaveLength(1);
  expect(requests.some((r) => r.url === `${BASE}/challenges/C-1`)).toBe(false);
  // タスクとフォローアップは、同じ要約の応答から出る。
  expect(within(m).getByText("タスク T-1")).toBeTruthy();
  expect(within(m).getByText("フォローアップ F-1")).toBeTruthy();
});

it("親が直接行った完了: 成果物は http・https だけがリンクで、完了の検証を通っていない旨が出る。通常の完了では欄を出さない", async () => {
  setup(
    {},
    {
      [APPROVAL]: summary({
        direct_completion: {
          artifacts: [
            "https://example.com/a#1",
            "o/r#12",
            "javascript:alert(1)",
          ],
          created_at: "2026-11-05T01:02:03.004Z",
          note: "案 B を動かして確かめた",
        },
      }),
    },
  );
  const m = await open();
  const block = within(m).getByTestId("direct-completion");
  const link = within(block).getByRole("link");
  expect(link).toHaveAttribute("href", "https://example.com/a#1");
  expect(link).toHaveAttribute("rel", "noopener noreferrer");
  expect(within(block).getAllByRole("link")).toHaveLength(1);
  expect(within(block).getByText("javascript:alert(1)")).toBeTruthy();
  expect(block.textContent).toContain("完了の検証");
  cleanup();
  setup();
  const m2 = await open();
  expect(within(m2).queryByTestId("direct-completion")).toBeNull();
});

it("取り込みの可否の注記は、あるときだけ出す", async () => {
  setup({}, { [APPROVAL]: summary({ mergeability_note: "未確認" }) });
  const m = await open();
  expect(within(m).getByTestId("approval-mergeability").textContent).toContain(
    "未確認",
  );
  cleanup();
  setup();
  const m2 = await open();
  expect(within(m2).queryByTestId("approval-mergeability")).toBeNull();
});

it("反映の後に確かめる条件の見出しは、サーバが返す見出しをそのまま出す", async () => {
  const [heading] = CONDITIONS_AFTER_RELEASE_HEADINGS;
  setup(
    {},
    {
      [APPROVAL]: summary({
        conditions_after_release: ["通知が届く"],
        conditions_after_release_heading: heading,
      }),
    },
  );
  const m = await open();
  const box = within(m).getByTestId("approval-conditions-after-release");
  expect(within(box).getByText(heading)).toBeTruthy();
});

it("開いた後の読み直しで版が変わると、更新された旨が出る", async () => {
  // n 回目の読み取りの版は n + 2 にする（呼び出しの順に頼らない）。
  let n = 0;
  setup({}, { [APPROVAL]: () => json(200, summary({ version: 2 + ++n })) });
  const m = await open();
  await button(m);
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await within(m).findByText(/承認の対象が更新されました/);
});

const fix = (over: Record<string, unknown> = {}) => ({
  at: "2026-11-20T14:12:00Z",
  files: ["runtime/internal/cli/ticket.go"],
  from_ref: "a",
  operation_id: null,
  run_id: "R-1",
  target: "integration",
  task_id: "T-1",
  to_ref: "b",
  ...over,
});

it("AC-126: 反映するものの下に、通したコンフリクトの解消の件数と、宛先・直したファイルの名前が出る", async () => {
  setup({
    tasks: [task("T-1")],
    conflictFixes: [
      fix(),
      fix({
        run_id: "R-2",
        target: "release",
        task_id: null,
        operation_id: "O-1",
        files: ["a.go", "<b>.go"],
      }),
    ],
  });
  const m = await open();
  const reflect = within(m).getByTestId("completion-reflect");
  const box = within(reflect).getByTestId("conflict-fixes");
  expect(within(box).getByTestId("conflict-fixes-count").textContent).toBe("2");
  const rows = within(box).getAllByTestId("conflict-fix");
  expect(rows[0]?.textContent).toContain("統合先（タスク T-1）");
  expect(rows[0]?.textContent).toContain("runtime/internal/cli/ticket.go");
  expect(rows[1]?.textContent).toContain("反映先");
  // 文字として出す（HTML として解釈しない）
  expect(rows[1]?.textContent).toContain("<b>.go");
  expect(rows[1]?.querySelector("b")).toBeNull();
});

it("AC-128: 通したコンフリクトの解消が 0 件なら、完了承認画面に区画を出さない", async () => {
  setup();
  const m = await open();
  expect(within(m).queryByTestId("conflict-fixes")).toBeNull();
});

it("完了の承認待ちでない要約は、読み直さず、承認待ちでない旨を出す", async () => {
  let reads = 0;
  setup(
    {},
    {
      [APPROVAL]: () => {
        reads++;
        return json(200, summary({ kind: "plan", version: 3 }));
      },
    },
  );
  window.location.hash = "#/a/alpha/challenges/C-1/approve-completion";
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await screen.findByText(NOT_AWAITING_COMPLETION_TEXT);
  expect(reads).toBe(1);
});
