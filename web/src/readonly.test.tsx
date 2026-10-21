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
import { type MermaidLike, setMermaidLoader } from "./board/loadMermaid.ts";
import { screens } from "./screens.tsx";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

afterEach(() => {
  setMermaidLoader();
  vi.unstubAllGlobals();
  document.head.innerHTML = "";
});

// 本番の画面の一覧はボードだけ。ボードを操作したときの要求と DOM を検査する（詳細のモーダルは detail.test.tsx の T12 が検査する）。
async function operate(check: (container: HTMLElement, id: string) => void) {
  const { container } = render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() =>
    expect(screen.getAllByText(/alpha/).length).toBeGreaterThan(0),
  );
  for (const s of screens) {
    expect(screen.getByRole("region", { name: s.title })).toBeInTheDocument();
    check(container, s.id);
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

// AC-221 の fleet に置く承認待ちの課題（帯 → 詳細のモーダル → 承認のボタンまで操作するため）。
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
    [`${base}/challenges`]: { challenges: [{ ...challenge, card }] },
    [`${base}/challenges/C-1`]: {
      approvals: [],
      card,
      challenge,
      holds: [],
      operations: [],
      plans: [],
      runs: [],
      pairs: S4_PAIRS,
      source_binding: null,
      stages: S4_STAGES,
    },
    [`${base}/log?challenge=C-1`]: { activities: [] },
    [`${base}/challenges/C-1/approval`]: {
      approval: {
        challenge_id: "C-1",
        done_criteria: "完了条件",
        kind: "plan",
        other_pending_operations: [],
        pairs: S4_PAIRS,
        plan: { body: "承認する計画", version: 1 },
        release_held: false,
        releases: [],
        stages: S4_STAGES,
        title: "題名",
        version: 3,
      },
    },
    [`POST ${base}/challenges/C-1/approve`]: {},
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
  });
  await operate(() => {});
  fireEvent.click(screen.getByRole("button", { name: "完了を表示" }));
  for (const b of screen.getAllByRole("button", { name: "1 周を起動" })) {
    fireEvent.click(b);
  }
  // 帯の承認待ち → カード → 詳細のモーダル → 承認のボタン
  const strip = screen.getByRole("region", { name: "人間待ちの帯" });
  fireEvent.click(within(strip).getByRole("button", { name: "承認待ち 1" }));
  const c = await screen.findByTestId("card-C-1");
  fireEvent.click(within(c).getByRole("button"));
  const m = await screen.findByTestId("card-detail-content");
  // S4 の画面（段階の欄・図・承認の要約）が出ている
  await within(m).findByTestId("stages-detail");
  await within(m).findByTestId("figure-svg");
  await within(m).findByTestId("approval-stages");
  fireEvent.click(
    await within(m).findByRole("button", {
      name: "計画を承認する",
      hidden: true,
    }),
  );
  await within(m).findByText("承認しました。", {}, { timeout: 2000 });
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(requests.length).toBeGreaterThan(5));
  const posts = () => requests.filter((r) => r.method === "POST");
  await waitFor(() => expect(posts()).toHaveLength(3));
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
    "/api/v1/workspaces/beta/cycle",
  ]);
  const allowed = [
    /^\/api\/v1\/workspaces\/[^/]+\/challenges\/[^/]+\/approve$/,
    /^\/api\/v1\/workspaces\/[^/]+\/cycle$/,
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

it("AC-124: 描画した画面の DOM に form 要素が無い", async () => {
  installFakes();
  const seen: string[] = [];
  await operate((container, id) => {
    seen.push(id);
    expect(container.querySelector("form"), id).toBeNull();
  });
  expect(seen).toEqual(screens.map((s) => s.id));
  expect(within(document.body).queryAllByRole("form")).toHaveLength(0);
});
