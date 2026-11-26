import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import type { ApprovalSummary } from "./api/types.gen.ts";
import { CONVERSATION_NOTE } from "./guidance.ts";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

const meta = () => {
  const m = document.createElement("meta");
  m.name = "flywheel-csrf";
  m.content = "tok-123";
  document.head.append(m);
};

beforeEach(meta);
afterEach(() => {
  vi.unstubAllGlobals();
  for (const m of document.head.querySelectorAll("meta[name=flywheel-csrf]"))
    m.remove();
  cleanup();
});

const APPROVAL = "/api/v1/workspaces/alpha/challenges/C-1/approval";

function card(state: string, actions: string[], modifiers: string[] = []) {
  return {
    context: null,
    headline: null,
    modifiers,
    next_human_actions: actions,
    state,
  };
}

function challenge(status: string, version = 3) {
  return {
    created_at: "",
    dependencies: [],
    dependents: [],
    description: "説明",
    done_criteria: "完了条件",
    id: "C-1",
    priority: null,
    reporter: "me",
    chat_sessions: [],
    spec_cost_usd: 0,
    spec_draft: null,
    status,
    status_label: "",
    title: "題名",
    updated_at: "",
    urgency: null,
    version,
  };
}

function summary(over: Partial<ApprovalSummary> = {}): {
  approval: ApprovalSummary;
} {
  return {
    approval: {
      added_tasks: [],
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
      other_pending_operations: [
        { id: "O-2", kind: "delete", summary: "古いデータの削除" },
      ],
      pairs: [],
      plan: null,
      release_held: true,
      supervisor_hands_taken: {},
      supervisor_questions: [],
      stages: {
        actual: null,
        expected: null,
        figure: null,
        issues: null,
        issues_answer: null,
        non_goals: null,
      },
      releases: [
        {
          head_sha: "abc123def456",
          id: "O-1",
          ref: "refs/pull/9/head",
          summary: "v1.0 を出す",
        },
        { head_sha: null, id: "O-3", ref: null, summary: "別の release" },
      ],
      tasks: [],
      title: "題名",
      uses_integration: null,
      version: 3,
      ...over,
    },
  };
}

function setup(
  state: string,
  actions: string[],
  extra: Record<string, unknown> = {},
  modifiers: string[] = [],
) {
  const f = fixtures();
  const c = card(state, actions, modifiers);
  f["/api/v1/workspaces/alpha/challenges"] = {
    challenges: [{ ...challenge(state), card: c }],
  };
  f["/api/v1/workspaces/alpha/challenges/C-1"] = {
    approvals: [],
    card: c,
    challenge: challenge(state),
    conflict_fixes: [],
    direct_completions: [],
    followups: [],
    holds: [],
    operations: [],
    plans: [],
    runs: [],
    pairs: [],
    source_binding: null,
    source_children: [],
    supervisor_actions: [],
    supervisor_hands_taken: {},
    stages: {
      actual: null,
      expected: null,
      figure: null,
      issues: null,
      issues_answer: null,
      non_goals: null,
    },
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
      status: "in_progress",
      status_label: "着手中",
      tasks: [],
      tasks_done: 0,
      tasks_total: 0,
      title: "t",
      updated_at: "2026-11-01T00:00:00.000Z",
      uses_integration: null,
    },
  };
  f["/api/v1/workspaces/alpha/log?challenge=C-1"] = { activities: [] };
  return installFakes({ ...f, ...extra });
}

async function open() {
  // 課題の詳細は URL のハッシュで開く（#803。前はボードのカードを押して開いていた）。
  window.location.hash = "#/a/alpha/challenges/C-1";
  render(<App />);
  act(() => FakeEventSource.latest.open());
  return await screen.findByTestId("challenge-detail");
}

it("AC-207: approve_plan を持つ課題の詳細には、承認のボタンではなく計画の承認の画面へのリンクが出る（#811）", async () => {
  const { requests } = setup("awaiting_plan_approval", ["approve_plan"]);
  const m = await open();
  const link = await within(m).findByRole("link", {
    name: "計画の承認の画面を開く",
    hidden: true,
  });
  expect(link).toHaveAttribute("href", "#/a/alpha/challenges/C-1/approve-plan");
  expect(
    within(m).queryByRole("button", { name: /承認/, hidden: true }),
  ).toBeNull();
  // 詳細を開いただけでは承認の要約を読まない（読むのは承認の画面）
  expect(requests.some((r) => r.url === APPROVAL)).toBe(false);
});

const guideText = async (m: HTMLElement) =>
  (
    await within(m).findByRole("region", {
      name: "承認方法の案内",
      hidden: true,
    })
  ).textContent ?? "";

it("AC-214: approve_plan には reject のターミナルのコマンドと対話の案内", async () => {
  setup("awaiting_plan_approval", ["approve_plan"]);
  const t = await guideText(await open());
  expect(t).toContain(
    "flywheel reject C-1 --workspace '/w/alpha' --reason <理由>",
  );
  expect(t).toContain(CONVERSATION_NOTE);
  // AC-214a: 計画の承認は画面のボタンで行うので approve のコマンドは出さない
  expect(t).not.toContain("flywheel approve");
});

it("AC-215: approve_completion には本番反映を同時に承認する approve のコマンドと対話の案内", async () => {
  setup("awaiting_completion_approval", ["approve_completion"], {
    [APPROVAL]: summary(),
  });
  const t = await guideText(await open());
  expect(t).toContain("flywheel approve C-1 --workspace '/w/alpha'");
  expect(t).toContain("本番反映");
  expect(t).toContain(CONVERSATION_NOTE);
});

it("AC-216: approve_operation には approve <OP-ID> のコマンドと対話の案内（ボタンは無い）", async () => {
  setup("in_progress", ["approve_operation"]);
  const m = await open();
  const t = await guideText(m);
  expect(t).toContain("flywheel approve <OP-ID> --workspace '/w/alpha'");
  expect(t).toContain(CONVERSATION_NOTE);
  expect(
    within(m).queryByRole("button", { name: /承認/, hidden: true }),
  ).toBeNull();
});

// 案内の文言は UI の定数（CONVERSATION_NOTE）。テストは文言を直書きせず、定数の表示で確かめる。
it("AC-217: 案内の文言の定数は空でなく、5 つの操作それぞれの詳細に定数の文言がそのまま出る", async () => {
  expect(CONVERSATION_NOTE.trim()).not.toBe("");
  const cases: [string, string][] = [
    ["awaiting_plan_approval", "approve_plan"],
    ["awaiting_completion_approval", "approve_completion"],
    ["awaiting_human", "answer_question"],
    ["in_progress", "approve_operation"],
    ["in_progress", "increase_budget"],
  ];
  for (const [state, code] of cases) {
    cleanup();
    setup(
      state,
      [code],
      state === "awaiting_plan_approval"
        ? {}
        : state === "awaiting_completion_approval"
          ? { [APPROVAL]: summary() }
          : {},
    );
    const m = await open();
    const note = await within(m).findByTestId(
      "conversation-note",
      {},
      {
        timeout: 3000,
      },
    );
    expect(note.textContent, code).toBe(CONVERSATION_NOTE);
  }
});

it.each(["triage", "review_discrepancy", "clear_slot"])(
  "AC-218: %s だけなら対話の案内は出ない",
  async (code) => {
    setup("in_progress", [code]);
    const m = await open();
    await within(m).findByTestId("detail-waiting");
    expect(m.textContent).not.toContain(CONVERSATION_NOTE);
    expect(within(m).queryByTestId("conversation-note")).toBeNull();
  },
);
