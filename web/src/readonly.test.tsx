import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { type MermaidLike, setMermaidLoader } from "./parts/loadMermaid.ts";
import { LOADING_TEXT } from "./shell/Shell.tsx";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

afterEach(() => {
  window.location.hash = "";
  setMermaidLoader();
  vi.unstubAllGlobals();
  document.head.innerHTML = "";
});

// いまある行き先の一覧。後の課題が行き先を足したら、ここへ 1 行足す（操作の検査はこの一覧を全部たどる）。
// wait は、その行き先の画面が出たことを待つ口。
interface Destination {
  id: string;
  hash: string;
  wait: () => Promise<unknown>;
}
const title = (name: string) => () =>
  screen.findByRole("heading", { level: 2, name });
function destinations(agents: string[], challengeId?: string): Destination[] {
  const out: Destination[] = [
    { id: "all", hash: "#/all", wait: title("すべてのエージェント") },
  ];
  for (const a of agents) {
    out.push(
      {
        id: `${a}/now`,
        hash: `#/a/${a}/now`,
        wait: title("いま進められること"),
      },
      {
        id: `${a}/challenges`,
        hash: `#/a/${a}/challenges`,
        wait: title("課題"),
      },
      {
        id: `${a}/followups`,
        hash: `#/a/${a}/followups`,
        wait: title("フォローアップ"),
      },
      { id: `${a}/cycle`, hash: `#/a/${a}/cycle`, wait: title("サイクル") },
    );
  }
  const first = agents[0];
  if (first !== undefined && challengeId !== undefined) {
    const c = `#/a/${first}/challenges/${challengeId}`;
    out.push(
      {
        id: `${first}/approve-plan`,
        hash: `${c}/approve-plan`,
        wait: title("計画の承認: 題名"),
      },
      {
        id: `${first}/approve-completion`,
        hash: `${c}/approve-completion`,
        wait: () =>
          screen.findByRole("heading", { level: 2, name: /^完了の承認/ }),
      },
      {
        id: `${first}/task`,
        hash: `${c}/tasks/T-1`,
        wait: () =>
          waitFor(() =>
            expect(screen.queryByText(LOADING_TEXT)).not.toBeInTheDocument(),
          ),
      },
      {
        id: `${first}/challenge-history`,
        hash: `${c}/history`,
        wait: () => screen.findByTestId("challenge-detail"),
      },
      {
        id: `${first}/challenge-plan`,
        hash: `${c}/plan`,
        wait: () => screen.findByTestId("challenge-detail"),
      },
      // 課題の詳細は最後に開く（この後、詳細の中を操作する）。
      {
        id: `${first}/challenge`,
        hash: c,
        wait: () => screen.findByTestId("challenge-detail"),
      },
    );
  }
  return out;
}

// いまある行き先を全部たどり、行き先ごとに check を呼ぶ（課題の詳細の中身は detail.test.tsx の T12 が検査する）。
async function operate(
  dests: Destination[],
  check: (container: HTMLElement, id: string) => void,
) {
  window.location.hash = "";
  const { container } = render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() =>
    expect(screen.getAllByText(/alpha/).length).toBeGreaterThan(0),
  );
  for (const d of dests) {
    await act(async () => {
      window.location.hash = d.hash;
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    await d.wait();
    check(container, d.id);
  }
}

// S4 の画面（図・段階の欄・承認の要約）を操作しても要求の集合が増えないことを確かめるための値。
const S4_STAGES = {
  actual: [{ scene: "画面1", after: "実物後" }],
  expected: [
    { kind: "ui", scene: "画面1", before: "前", after: "予想後" },
    { kind: "cli", scene: "画面2", before: "前", after: "予想2" },
  ],
  figure: "graph TD; A-->B",
  issues: [
    {
      question: "どちらにする？",
      recommended: 1,
      options: [
        { label: "案A", example: null },
        { label: "案B", example: "例B" },
      ],
    },
  ],
  issues_answer: "案Aで決定",
  non_goals: ["やらない1"],
};
const S4_PAIRS = [
  {
    expected: S4_STAGES.expected[0],
    actual: S4_STAGES.actual[0],
  },
  { expected: S4_STAGES.expected[1], actual: null },
];

// AC-221 の fleet に置く承認待ちの課題（課題の詳細 → 承認のボタンまで操作するため）。
const idleCycle = {
  last_ended_at: null,
  last_result: null,
  last_started_at: null,
  state: "idle",
};
const noNeedsHuman = {
  budget_exhausted: [],
  challenges: [],
  discrepancies: [],
  operations: [],
  slots: [],
  triage: [],
};
function awaitingApproval(): Record<string, unknown> {
  const card = {
    context: null,
    headline: null,
    modifiers: [],
    next_human_actions: ["approve_plan"],
    state: "awaiting_plan_approval",
  };
  const challenge = {
    created_at: "",
    dependencies: [],
    dependents: [],
    description: "説明",
    done_criteria: "完了条件",
    id: "C-1",
    priority: null,
    reporter: "me",
    status: "awaiting_plan_approval",
    status_label: "",
    title: "題名",
    updated_at: "",
    urgency: null,
    version: 3,
  };
  const status = {
    actionable: { challenges: [] },
    approved: { operations: [] },
    binary_commit: "unknown",
    cycle_work: {
      stages: {
        classify: 0,
        cleanup: 0,
        improve: 0,
        learn: 0,
        plan: 0,
        release: 0,
        run: 0,
        verify: 0,
      },
      total: 1,
    },
    followups: { open: 0 },
    ingestable: null,
    needs_human: {
      budget_exhausted: [],
      challenges: [challenge],
      discrepancies: [],
      operations: [],
      slots: [],
      triage: [],
    },
    waiting_external: { challenges: [] },
  };
  const base = "/api/v1/workspaces/alpha";
  return {
    [`${base}/status`]: status,
    // 「サイクルを回す」を押せるのは、進むものが 1 件以上あり、サイクルが走っていないときだけ（#803）。
    [`${base}/cycle`]: idleCycle,
    "/api/v1/workspaces/beta/status": { ...status, needs_human: noNeedsHuman },
    [`${base}/challenges`]: { challenges: [{ ...challenge, card }] },
    [`${base}/challenges/C-1`]: {
      approvals: [],
      card,
      challenge,
      conflict_fixes: [],
      direct_completions: [],
      followups: [],
      holds: [],
      operations: [],
      plans: [],
      runs: [],
      tasks: [],
      tree: {
        budget_usd: null,
        children: [],
        cost_usd: 0,
        dependencies: [],
        followups_open: 0,
        id: "C-1",
        next_human_actions: ["approve_plan"],
        parent: null,
        status: "awaiting_plan_approval",
        status_label: "",
        tasks: [],
        tasks_done: 0,
        tasks_total: 0,
        title: "題名",
        updated_at: "2026-11-01T00:00:00.000Z",
        uses_integration: null,
      },
      pairs: S4_PAIRS,
      source_binding: null,
      source_children: [],
      stages: S4_STAGES,
      waiting: { prerequisites: [] },
    },
    [`${base}/log?challenge=C-1`]: { activities: [] },
    [`${base}/challenges/C-1/approval`]: {
      approval: {
        challenge_id: "C-1",
        done_criteria: "完了条件",
        kind: "plan",
        other_pending_operations: [],
        pairs: S4_PAIRS,
        plan: {
          body: "承認する計画",
          budget_limit_reason: null,
          budget_limit_usd: null,
          budget_usd: null,
          summary: "方針の本文",
          tasks: [],
          topics: [],
          uses_integration: null,
          version: 1,
        },
        release_held: false,
        releases: [],
        stages: S4_STAGES,
        title: "題名",
        version: 3,
      },
    },
    [`POST ${base}/challenges/C-1/approve`]: {
      cycle: { reason: "started", started: true },
    },
    "/api/v1/needs-human": {
      unreadable_workspaces: [],
      workspaces: [{ name: "alpha", needs_human: status.needs_human }],
    },
  };
}

it("AC-221: 全画面を操作しても fetch は GET と POST …/approve・POST …/cycle だけ、EventSource は events の購読だけ", async () => {
  document.head.innerHTML = '<meta name="flywheel-csrf" content="tok">';
  const mermaid: MermaidLike = {
    initialize: vi.fn(),
    render: vi.fn(async () => ({ svg: "<svg></svg>" })),
  };
  setMermaidLoader(vi.fn(async () => mermaid));
  const { requests } = installFakes({
    ...fixtures(),
    ...awaitingApproval(),
    "POST /api/v1/workspaces/alpha/cycle": { started: true },
    "POST /api/v1/workspaces/beta/cycle": { started: true },
  });
  // 行き先を全部たどり、「サイクルを回す」のボタンがある行き先（いま進められること・サイクル）では押す。
  await operate(destinations(["alpha", "beta"], "C-1"), () => {
    for (const b of screen.queryAllByRole("button", {
      name: "サイクルを回す",
    })) {
      fireEvent.click(b);
    }
  });
  const m = await screen.findByTestId("challenge-detail");
  // 「計画の本文」の切り替えで段階の欄・図が出る
  fireEvent.click(within(m).getByRole("link", { name: "計画の本文" }));
  await within(m).findByTestId("stages-detail");
  await within(m).findByTestId("figure-svg");
  // 計画の承認は、計画の承認の画面のボタンで行う
  window.location.hash = "#/a/alpha/challenges/C-1/approve-plan";
  fireEvent.click(
    await screen.findByRole("button", { name: "この計画を承認する" }),
  );
  await screen.findByText("承認しました。", {}, { timeout: 2000 });
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(requests.length).toBeGreaterThan(5));
  const posts = () => requests.filter((r) => r.method === "POST");
  // 承認が 1 回と、サイクルを回すのが 2 つのエージェント × 2 つの画面（いま進められること・サイクル）。
  await waitFor(() => expect(posts()).toHaveLength(5));
  // 承認の操作で記録した要求に POST …/approve が実際に現れる
  expect(
    posts().filter(
      (r) => r.url === "/api/v1/workspaces/alpha/challenges/C-1/approve",
    ),
  ).toHaveLength(1);
  expect(
    posts()
      .filter((r) => /\/cycle$/.test(r.url))
      .map((r) => r.url),
  ).toEqual([
    "/api/v1/workspaces/alpha/cycle",
    "/api/v1/workspaces/alpha/cycle",
    "/api/v1/workspaces/beta/cycle",
    "/api/v1/workspaces/beta/cycle",
  ]);
  const allowed = [
    /^\/api\/v1\/workspaces\/[^/]+\/challenges\/[^/]+\/approve$/,
    /^\/api\/v1\/workspaces\/[^/]+\/cycle$/,
    // 対話の欄（#1002）の書き込み
    /^\/api\/v1\/workspaces\/[^/]+\/chat\/(messages|stop|new)$/,
  ];
  for (const r of requests) {
    if (r.method === "GET") continue;
    expect(r.method).toBe("POST");
    expect(
      allowed.some((re) => re.test(r.url)),
      r.url,
    ).toBe(true);
    // AC-222: POST はすべて CSRF の値を要求ヘッダに持つ
    expect(r.headers["X-Flywheel-CSRF"]).toBe("tok");
  }
  expect(
    FakeEventSource.instances.every((e) => e.url === "/api/v1/events"),
  ).toBe(true);
  document.head.innerHTML = "";
});

// 対話の欄（#1002）は、入力と送る操作を持つ唯一の例外。欄の中の要素は、これらの規則の数えから外す。
const outsideChat = <T extends Element>(els: Iterable<T>): T[] =>
  [...els].filter((e) => !e.closest('[data-testid="chat-panel"]'));

it("AC-124: 描画した画面の DOM に form 要素が無い（対話の欄を除く）", async () => {
  installFakes({ ...fixtures(), ...awaitingApproval() });
  const dests = destinations(["alpha", "beta"], "C-1");
  const seen: string[] = [];
  await operate(dests, (container, id) => {
    seen.push(id);
    expect(outsideChat(container.querySelectorAll("form")), id).toHaveLength(0);
  });
  expect(seen).toEqual(dests.map((d) => d.id));
  expect(
    outsideChat(within(document.body).queryAllByRole("form")),
  ).toHaveLength(0);
});

// AC-118 #814: どの画面にも、文字を入力する欄は無い（対話の欄を除く。input・textarea・select・編集できる要素）。
// input は種類を問わず 0 件（チェックの入力も含む。足すときはこの規則を決め直す）。
it("AC-118 #814: 描画したどの画面にも、文字を入力する欄は無い", async () => {
  installFakes({ ...fixtures(), ...awaitingApproval() });
  const dests = destinations(["alpha", "beta"], "C-1");
  const seen: string[] = [];
  await operate(dests, (container, id) => {
    seen.push(id);
    expect(
      outsideChat(container.querySelectorAll("input, textarea, select")),
      id,
    ).toHaveLength(0);
    expect(
      outsideChat(container.querySelectorAll("[contenteditable]")),
      id,
    ).toHaveLength(0);
    for (const role of ["textbox", "searchbox", "combobox"]) {
      expect(
        outsideChat(within(container).queryAllByRole(role)),
        id,
      ).toHaveLength(0);
    }
  });
  expect(seen).toEqual(dests.map((d) => d.id));
});
