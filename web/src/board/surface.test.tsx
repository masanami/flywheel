import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import { NO_ACTION_LABEL } from "../labels.ts";
import { POLL_MS, RECONNECT_MS } from "../live/useLive.ts";
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
  dependencies: [],
  dependents: [],
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
                run_id: "R-2",
              },
              {
                answer: null,
                answered_at: null,
                answered_by: null,
                from_status: "in_progress",
                from_status_label: "着手中",
                question: "run の無い保留",
                raised_at: "2026-02-02T00:00:00Z",
                run_id: null,
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
      pairs: [],
      source_binding: null,
      stages: {
        actual: null,
        expected: null,
        figure: null,
        issues: null,
        issues_answer: null,
        non_goals: null,
      },
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
  // 更新なしは実行中の印と区別できる（running の印を持たない）
  expect(marksOf("C-5")).toEqual(["unresponsive"]);
  expect(within(face("C-5")).getByText("更新なし")).toBeInTheDocument();
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
  // 「一言で」と同じ文を属性の表に 2 度出さない（見出しの行は無い）
  expect(within(modal).queryByText("見出し")).toBeNull();
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
  expect(hold.textContent).toContain("原因の run: R-2");
  const noRun = Array.from(modal.querySelectorAll('[data-hold="open"]')).find(
    (el) => el.textContent?.includes("run の無い保留"),
  );
  expect(noRun?.textContent).toContain(
    "原因の run: なし（原因の run は記録されていない）",
  );
});

it("AC-206: UI は全画面を操作しても（全カードの詳細・全区分の絞り込み・通知・再接続・定期の取り直し）GET /api/v1/questions を要求しない", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    const f = bodies();
    const nhEmpty = {
      budget_exhausted: [],
      challenges: [],
      discrepancies: [],
      operations: [],
      slots: [],
      triage: [],
    };
    f["/api/v1/needs-human"] = {
      unreadable_workspaces: [],
      workspaces: [
        {
          name: "alpha",
          needs_human: {
            ...nhEmpty,
            challenges: [
              { id: "C-1", status: "awaiting_plan_approval" },
              { id: "C-9", status: "awaiting_human" },
            ],
          },
        },
      ],
    };
    const { requests } = installFakes(f);
    render(<App />);
    act(() => FakeEventSource.latest.open());
    await screen.findByText("題名C-1");
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    const ids = Array.from({ length: 9 }, (_, i) => `C-${i + 1}`);
    // すべてのカードの詳細を開いて閉じる（問いの詳細を含む）
    for (const id of ids) {
      fireEvent.click(within(face(id)).getByRole("button"));
      const modal = await screen.findByTestId("card-detail-content");
      fireEvent.click(within(modal).getByText("閉じる"));
      await vi.waitFor(() =>
        expect(screen.queryByTestId("card-detail-content")).toBeNull(),
      );
    }
    // 帯のすべての区分の押し子を押して絞り込み、解除する
    const chips = Array.from(document.querySelectorAll(".strip-chip"));
    expect(chips.length).toBeGreaterThan(0);
    for (const chip of chips) {
      fireEvent.click(chip);
      fireEvent.click(chip);
    }
    // ワークスペースごとの変更の通知
    for (const workspace of ["alpha", "beta"]) {
      act(() =>
        FakeEventSource.latest.send({ type: "workspace_changed", workspace }),
      );
    }
    // 接続が切れて再接続し、すべてを読み直す
    const sources = FakeEventSource.instances.length;
    act(() => FakeEventSource.latest.fail());
    act(() => {
      vi.advanceTimersByTime(RECONNECT_MS);
    });
    expect(FakeEventSource.instances.length).toBeGreaterThan(sources);
    const beforeOpen = requests.length;
    act(() => FakeEventSource.latest.open());
    // 保留中の読み込み（通知・再接続）が出し切るのを待ち、定期の取り直しと切り分ける
    let seen = -1;
    await vi.waitFor(() => {
      const n = requests.length;
      const settled = n === seen;
      seen = n;
      expect(settled).toBe(true);
    });
    expect(requests.length).toBeGreaterThan(beforeOpen);
    // 定期の取り直し（60 秒ごとの cycle と challenges）
    const mark = requests.length;
    act(() => {
      vi.advanceTimersByTime(POLL_MS * 2);
    });
    await vi.waitFor(() =>
      expect(requests.length - mark).toBeGreaterThanOrEqual(8),
    );
    // 60 秒 2 回分 × 2 ワークスペース × (cycle・challenges) だけで、status は要求しない
    const polled = requests.slice(mark).map((r) => r.url);
    for (const ws of ["alpha", "beta"])
      for (const leaf of ["cycle", "challenges"])
        expect(
          polled.filter((u) => u === `/api/v1/workspaces/${ws}/${leaf}`),
        ).toHaveLength(2);
    expect(polled.some((u) => u.endsWith("/status"))).toBe(false);
    await vi.waitFor(() =>
      expect(
        requests.filter((r) => r.url === "/api/v1/needs-human").length,
      ).toBeGreaterThan(1),
    );
    // 操作が実際に要求を発行したこと（空振りでないこと）
    expect(
      requests.filter((r) => /question/i.test(r.url)),
      "questions を要求した",
    ).toEqual([]);
    expect(
      FakeEventSource.instances.filter((e) => /question/i.test(e.url)),
    ).toEqual([]);
  } finally {
    Reflect.deleteProperty(document, "visibilityState");
    vi.useRealTimers();
  }
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
