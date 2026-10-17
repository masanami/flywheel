import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import { NO_ACTION_LABEL } from "../labels.ts";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";

afterEach(() => vi.unstubAllGlobals());

const SIXTY = "あ".repeat(60);
const LONG = `${SIXTY}い`;

const cc = (
  id: string,
  state: string,
  card: Record<string, unknown> = {},
  o: Record<string, unknown> = {},
) => ({
  created_at: "",
  description: `説明${id}`,
  done_criteria: `完了条件${id}`,
  id,
  priority: null,
  reporter: "",
  status: state,
  status_label: "",
  title: `題名${id}`,
  updated_at: "",
  urgency: null,
  version: 1,
  card: {
    context: null,
    headline: null,
    modifiers: [],
    next_human_actions: [],
    state,
    ...card,
  },
  ...o,
});

const op = {
  challenge_id: "C-6",
  created_at: "",
  id: "OP-1",
  kind: "merge",
  ref: null,
  state: "approved",
  summary: "承認済み",
  version: 1,
};

function bodies() {
  const f = fixtures();
  f["/api/v1/workspaces/alpha/challenges"] = {
    challenges: [
      cc(
        "C-1",
        "classified",
        { next_human_actions: ["approve_plan"] },
        { priority: "P1" },
      ),
      cc("C-2", "verifying", { modifiers: ["waiting_ci"] }),
      cc("C-3", "classified"),
      cc("C-4", "in_progress", { modifiers: ["running"] }),
      cc("C-5", "in_progress", { modifiers: ["running", "unresponsive"] }),
      cc("C-6", "in_progress"),
      cc("C-7", "in_progress", { headline: LONG }),
      cc("C-8", "in_progress", { headline: SIXTY }),
      cc("C-9", "awaiting_human", { next_human_actions: ["answer_question"] }),
    ],
  };
  const st = f["/api/v1/workspaces/alpha/status"] as Record<string, unknown>;
  f["/api/v1/workspaces/alpha/status"] = {
    ...st,
    actionable: { challenges: [{ id: "C-3" }] },
    approved: { operations: [op] },
  };
  for (const id of ["C-7", "C-9"]) {
    const c = (
      f["/api/v1/workspaces/alpha/challenges"] as {
        challenges: ReturnType<typeof cc>[];
      }
    ).challenges.find((x) => x.id === id) as ReturnType<typeof cc>;
    f[`/api/v1/workspaces/alpha/challenges/${id}`] = {
      approvals: [],
      card: c.card,
      challenge: c,
      holds:
        id === "C-9"
          ? [
              {
                answer: null,
                answered_at: null,
                answered_by: null,
                from_status: "in_progress",
                from_status_label: "着手中",
                question: "どちらにしますか",
                raised_at: "2026-02-01T00:00:00Z",
              },
            ]
          : [],
      operations: [],
      plans: [],
      runs: [
        {
          challenge_id: id,
          id: "R-1",
          kind: "delegation",
          started_at: "2026-01-01T00:00:00Z",
          ended_at: null,
          result: null,
        },
        {
          challenge_id: id,
          id: "R-2",
          kind: "delegation",
          started_at: "2026-03-01T00:00:00Z",
          ended_at: null,
          result: null,
        },
      ],
      source_binding: null,
    };
    f[`/api/v1/workspaces/alpha/log?challenge=${id}`] = { activities: [] };
  }
  return f;
}

async function mount() {
  const { requests } = installFakes(bodies());
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await screen.findByText("題名C-1");
  return { requests };
}

const face = (id: string) => screen.getByTestId(`card-${id}`);
const marksOf = (id: string) =>
  Array.from(face(id).querySelectorAll("[data-mark]")).map((e) =>
    e.getAttribute("data-mark"),
  );

it("AC-182: 人間の対応待ち・ワークスペース・問いの一覧のタブを描かず、ボードだけを持つ", async () => {
  await mount();
  expect(screen.queryByRole("navigation")).toBeNull();
  for (const n of ["人間の対応待ち", "ワークスペース", "問いの一覧"])
    expect(screen.queryByRole("button", { name: n })).toBeNull();
  expect(screen.getByRole("region", { name: "ボード" })).toBeInTheDocument();
});

it("AC-183: カードを押すと詳細がボードの上のモーダルで開き、閉じるとボードに戻る", async () => {
  await mount();
  expect(screen.queryByTestId("card-detail-content")).toBeNull();
  fireEvent.click(within(face("C-1")).getByRole("button"));
  const modal = await screen.findByTestId("card-detail-content");
  expect(modal.closest("dialog")).not.toBeNull();
  expect(screen.getByRole("region", { name: "ボード" })).toBeInTheDocument();
  fireEvent.click(within(modal).getByText("閉じる"));
  expect(screen.queryByTestId("card-detail-content")).toBeNull();
  expect(screen.getByRole("region", { name: "ボード" })).toBeInTheDocument();
});

it("AC-188〜193: 区分の印 6 種が、それぞれの課題のカードに出る", async () => {
  await mount();
  expect(marksOf("C-1")).toEqual(["needs_human"]);
  expect(marksOf("C-2")).toEqual(["external"]);
  expect(marksOf("C-3")).toEqual(["actionable"]);
  expect(marksOf("C-4")).toEqual(["running"]);
  // 応答なしは実行中の印と区別できる（running の印を持たない）
  expect(marksOf("C-5")).toEqual(["unresponsive"]);
  expect(within(face("C-5")).getByText("応答なし")).toBeInTheDocument();
  expect(marksOf("C-6")).toEqual(["approved_operation"]);
  expect(
    within(face("C-6")).getByText("承認済みの操作が未実行"),
  ).toBeInTheDocument();
});

it("AC-194〜196: カードの面は見出し・ID・優先度・区分の印だけで、説明・状態・次の操作の文言を出さない", async () => {
  await mount();
  const c1 = face("C-1");
  // 見出しは card.headline が null ならタイトル
  expect(within(c1).getByText("題名C-1")).toBeInTheDocument();
  expect(within(c1).getByText("C-1")).toBeInTheDocument();
  expect(within(c1).getByText("P1")).toBeInTheDocument();
  expect(within(c1).getByText("人間の操作待ち")).toBeInTheDocument();
  expect(within(face("C-3")).getByText("優先度なし")).toBeInTheDocument();
  for (const id of ["C-1", "C-3", "C-9"]) {
    const t = face(id).textContent ?? "";
    expect(t).not.toContain(`説明${id}`);
    expect(t).not.toContain(NO_ACTION_LABEL);
    expect(t).not.toContain("計画を承認する");
    expect(t).not.toContain("分類済");
    expect(t).not.toContain("人間対応待ち");
    expect(t).not.toContain("いまの状態");
    expect(t).not.toContain("次に人間がすること");
  }
  // 見出しが headline のカードは、タイトルでなく headline を出す
  expect(face("C-8").textContent).not.toContain("題名C-8");
});

it("AC-197/198: 60 文字を超える見出しは先頭 60 文字と「…」、ちょうどは全文。切り詰めた詳細に全文が出る", async () => {
  await mount();
  expect(within(face("C-7")).getByText(`${SIXTY}…`)).toBeInTheDocument();
  expect(face("C-7").textContent).not.toContain(LONG);
  expect(within(face("C-8")).getByText(SIXTY)).toBeInTheDocument();
  fireEvent.click(within(face("C-7")).getByRole("button"));
  const modal = await screen.findByTestId("card-detail-content");
  expect(within(modal).getByTestId("detail-headline")).toHaveTextContent(LONG);
});

it("AC-202: 詳細に card.state と card.next_human_actions の文言（[] なら「なし（エージェントが進める）」）が出る", async () => {
  await mount();
  fireEvent.click(within(face("C-9")).getByRole("button"));
  let modal = await screen.findByTestId("card-detail-content");
  expect(modal.textContent).toContain("人間対応待ち");
  expect(modal.textContent).toContain("問いに答える");
  fireEvent.click(within(modal).getByText("閉じる"));
  fireEvent.click(within(face("C-7")).getByRole("button"));
  modal = await screen.findByTestId("card-detail-content");
  expect(modal.textContent).toContain("着手中");
  expect(modal.textContent).toContain(NO_ACTION_LABEL);
});

it("AC-203: 未回答の保留の詳細に、問いの文・保留に入った日時・入る直前の状態・原因の run が出る", async () => {
  await mount();
  fireEvent.click(within(face("C-9")).getByRole("button"));
  const modal = await screen.findByTestId("card-detail-content");
  const hold = await vi.waitFor(() => {
    const el = modal.querySelector('[data-hold="open"]');
    if (!el) throw new Error("保留がまだ無い");
    return el;
  });
  expect(hold.textContent).toContain("どちらにしますか");
  expect(hold.textContent).toContain("2026-02-01T00:00:00Z");
  expect(hold.textContent).toContain("入る直前の状態: 着手中");
  // 詳細の API は保留の run を返さないため、推測せず「不明」と出す
  expect(hold.textContent).toContain("原因の run: 不明");
});

it("AC-206: UI はどの画面を操作しても GET /api/v1/questions を要求しない", async () => {
  const { requests } = await mount();
  fireEvent.click(within(face("C-9")).getByRole("button"));
  await screen.findByTestId("card-detail-content");
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await vi.waitFor(() =>
    expect(
      requests.filter((r) => r.url === "/api/v1/needs-human").length,
    ).toBeGreaterThan(1),
  );
  expect(requests.some((r) => r.url.includes("/questions"))).toBe(false);
});

it("集約から除かれたワークスペースは、読み取れなかったことをボードに示す", async () => {
  const f = bodies();
  f["/api/v1/needs-human"] = {
    unreadable_workspaces: ["gamma"],
    workspaces: [],
  };
  installFakes(f);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const note = await screen.findByTestId("unreadable-workspaces");
  expect(note).toHaveTextContent("gamma");
});
