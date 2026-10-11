import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

afterEach(() => vi.unstubAllGlobals());

const ch = (id: string, status: string, o: Record<string, unknown> = {}) => ({
  created_at: "",
  description: "",
  done_criteria: "",
  id,
  priority: null,
  reporter: "",
  status,
  status_label: "",
  title: `題名${id}`,
  updated_at: "",
  urgency: null,
  version: 1,
  ...o,
});
const cc = (id: string, status: string, card: Record<string, unknown>) => ({
  ...ch(id, status),
  card: {
    context: null,
    headline: null,
    modifiers: [],
    next_human_actions: [],
    state: status,
    ...card,
  },
});

function bodies() {
  const f = fixtures() as Record<string, unknown>;
  const needs = {
    budget_exhausted: [
      {
        challenge_id: "C-5",
        impl_remaining_usd: 0,
        plan_version: 2,
        review_remaining_usd: 1,
      },
    ],
    challenges: [
      ch("C-1", "awaiting_plan_approval", { priority: "P1" }),
      ch("C-2", "awaiting_completion_approval"),
      ch("C-3", "awaiting_human"),
    ],
    discrepancies: [{ challenge_id: "C-4", kinds: ["upstream_closed"] }],
    operations: [
      {
        challenge_id: "C-2",
        created_at: "",
        id: "op1",
        kind: "merge",
        ref: null,
        state: "pending",
        summary: "main へマージ",
        version: 1,
      },
    ],
    slots: [{ path: "/s/1", repo: "r", run_id: null, slot_id: "slot-1" }],
    triage: [{ challenge_id: "C-6", reason: "失敗が続く", run_id: "R-1" }],
  };
  f["/api/v1/needs-human"] = {
    unreadable_workspaces: [],
    workspaces: [{ name: "alpha", needs_human: needs }],
  };
  f["/api/v1/workspaces"] = {
    workspaces: [
      { error: null, name: "alpha", path: "/w/alpha", state: "ok" },
      {
        error: "壊れている",
        name: "beta",
        path: "/w/beta",
        state: "store_error",
      },
    ],
  };
  f["/api/v1/workspaces/alpha/challenges"] = {
    challenges: [
      cc("C-1", "awaiting_plan_approval", {
        next_human_actions: ["approve_plan"],
      }),
      cc("C-7", "in_progress", { modifiers: ["running", "unresponsive"] }),
      cc("C-8", "classified", {}),
    ],
  };
  const st = f["/api/v1/workspaces/alpha/status"] as Record<string, unknown>;
  f["/api/v1/workspaces/alpha/status"] = {
    ...st,
    needs_human: needs,
    actionable: {
      challenges: [ch("C-7", "in_progress"), ch("C-8", "classified")],
    },
    waiting_external: {
      challenges: [
        { challenge_id: "C-8", checks: "pending", pr_url: "https://x/pr/1" },
      ],
    },
    approved: {
      operations: [
        {
          challenge_id: "C-9",
          created_at: "",
          id: "op2",
          kind: "deploy",
          ref: null,
          state: "approved",
          summary: "承認済みの操作",
          version: 1,
        },
      ],
    },
  };
  return f;
}

async function mount() {
  installFakes(bodies());
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await screen.findByText("C-1");
  // 既定はボードの画面。人間の対応待ちの画面（S1 の AC）へ切り替えて検査する。
  fireEvent.click(screen.getByRole("button", { name: "人間の対応待ち" }));
  await screen.findByRole("heading", { name: "計画承認待ち（1）" });
}

it("AC-92〜97: 最上位はワークスペースごとに状態で分けて表示し、ok でないワークスペースはエラー表示で他は続く（AC-122）", async () => {
  await mount();
  const top = screen.getByRole("region", { name: "alpha の対応待ち" });
  for (const t of [
    "計画承認待ち（1）",
    "完了確認待ち（1）",
    "人間対応待ち（1）",
  ]) {
    expect(within(top).getByRole("heading", { name: t })).toBeInTheDocument();
  }
  expect(within(top).getByText(/main へマージ/)).toBeInTheDocument();
  expect(within(top).getByText(/上流で閉じられた/)).toBeInTheDocument();
  expect(within(top).getByText(/失敗が続く/)).toBeInTheDocument();
  expect(within(top).getByText(/C-5/)).toBeInTheDocument();
  expect(within(top).getByText(/slot-1/)).toBeInTheDocument();
  // カードは一覧の card から引く
  expect(within(top).getByText(/計画を承認する/)).toBeInTheDocument();
  const beta = screen.getByRole("region", { name: "beta の対応待ち" });
  expect(within(beta).getByRole("alert")).toHaveTextContent("store_error");
});

it("AC-98〜100: ワークスペースの画面は進められるもの・外部待ち・承認済みを別に表示する", async () => {
  await mount();
  fireEvent.click(screen.getByRole("button", { name: "ワークスペース" }));
  const alpha = screen.getByRole("region", { name: "alpha の状況" });
  expect(
    within(alpha).getByRole("heading", {
      name: /システムが次に進められるもの（2）/,
    }),
  ).toBeInTheDocument();
  expect(
    within(alpha).getByRole("heading", { name: /外部を待っているもの（1）/ }),
  ).toBeInTheDocument();
  expect(within(alpha).getByText(/承認済みの操作/)).toBeInTheDocument();
  expect(within(alpha).getByText(/応答なし/)).toBeInTheDocument();
  expect(within(alpha).getByText("https://x/pr/1")).toBeInTheDocument();
  const beta = screen.getByRole("region", { name: "beta の状況" });
  expect(within(beta).getByRole("alert")).toHaveTextContent("store_error");
});

it("AC-110a: needs-human・questions から除かれたワークスペースは、読み取れなかったことを画面に示す", async () => {
  const f = bodies() as Record<string, unknown>;
  f["/api/v1/workspaces"] = {
    workspaces: [
      { error: null, name: "alpha", path: "/w/alpha", state: "ok" },
      { error: null, name: "gamma", path: "/w/gamma", state: "ok" },
    ],
  };
  f["/api/v1/workspaces/gamma/status"] = f["/api/v1/workspaces/beta/status"];
  f["/api/v1/workspaces/gamma/challenges"] = { challenges: [] };
  f["/api/v1/needs-human"] = {
    ...(f["/api/v1/needs-human"] as Record<string, unknown>),
    unreadable_workspaces: ["gamma"],
  };
  f["/api/v1/questions"] = { questions: [], unreadable_workspaces: ["gamma"] };
  installFakes(f);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await screen.findByText("C-1");
  fireEvent.click(screen.getByRole("button", { name: "人間の対応待ち" }));
  await screen.findByText(/main へマージ/);
  const g = screen.getByRole("region", { name: "gamma の対応待ち" });
  expect(within(g).getByRole("alert")).toHaveTextContent(
    "読み取れませんでした",
  );
  expect(screen.queryByText("読み込み中")).toBeNull();
  expect(screen.getAllByTestId("unreadable-workspaces")[0]).toHaveTextContent(
    "gamma",
  );
  // 読めたワークスペースの分は表示される。
  expect(
    within(screen.getByRole("region", { name: "alpha の対応待ち" })).getByText(
      /main へマージ/,
    ),
  ).toBeInTheDocument();
});
