import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type {
  Ingestable,
  IngestableItem,
  Question,
  StatusResponse,
} from "../api/types.gen.ts";
import { RELOAD_TEXT } from "../labels.ts";
import { POLL_MS } from "../live/useLive.ts";
import {
  FakeEventSource,
  type FakeResponder,
  emptyStatus,
  installFakes,
} from "../testing/fakes.ts";

// 「いま進められること」の受入基準（AC-115〜AC-178）を 1 つずつ直接確かめる。Issue #805。

const P = "/api/v1/workspaces/alpha";
const AGENT_PATH = "/w/alpha";

const ch = (id: string, status: string, title = `題名 ${id}`) => ({
  context: null,
  created_at: "2026-11-01T00:00:00Z",
  description: "",
  done_criteria: "",
  headline: null,
  id,
  priority: null,
  reporter: "x",
  status,
  status_label: status,
  title,
  updated_at: "2026-11-01T00:00:00Z",
  urgency: null,
  version: 1,
});

type NH = Partial<StatusResponse["needs_human"]>;
const stagesZero = { ...emptyStatus.cycle_work.stages };
const item = (
  n: number,
  over: Partial<IngestableItem> = {},
): IngestableItem => ({
  children: [],
  external_key: `o/r#${n}`,
  number: n,
  parent_challenge_id: null,
  parent_external_key: null,
  repo: "o/r",
  title: `取り込み元 ${n}`,
  url: `https://example.invalid/${n}`,
  ...over,
});

interface Setup {
  nh?: NH;
  questions?: Partial<Question>[];
  challenges?: ReturnType<typeof ch>[];
  stages?: Partial<typeof stagesZero>;
  followups?: number;
  cycle?: string;
  ingestable?: Ingestable | FakeResponder;
  post?: unknown;
}

function setup(s: Setup = {}) {
  const stages = { ...stagesZero, ...(s.stages ?? {}) };
  const total = Object.values(stages).reduce((a, b) => a + b, 0);
  const status = {
    ...emptyStatus,
    cycle_work: { stages, total },
    followups: { open: s.followups ?? 0 },
    needs_human: { ...emptyStatus.needs_human, ...(s.nh ?? {}) },
  };
  const bodies: Record<string, unknown> = {
    "/api/v1/workspaces": {
      workspaces: [
        { error: null, name: "alpha", path: AGENT_PATH, state: "ok" },
      ],
    },
    "/api/v1/needs-human": { unreadable_workspaces: [], workspaces: [] },
    "/api/v1/questions": {
      questions: (s.questions ?? []).map((q) => ({
        challenge_id: "C-1",
        from_status: "in_progress",
        question: "問い",
        raised_at: "2026-11-01T00:00:00Z",
        run_id: null,
        task_id: null,
        title: "t",
        workspace: "alpha",
        ...q,
      })),
      unreadable_workspaces: [],
    },
    [`${P}/status`]: status,
    [`${P}/challenges`]: { challenges: s.challenges ?? [] },
    [`${P}/cycle`]: {
      last_ended_at: null,
      last_result: null,
      last_started_at: null,
      state: s.cycle ?? "idle",
    },
    [`POST ${P}/cycle`]: s.post ?? { started: true },
  };
  const ing = s.ingestable ?? { error: null, items: [] };
  bodies[`${P}/status?ingestable=true`] =
    typeof ing === "function" ? ing : { ...status, ingestable: ing };
  return bodies;
}

async function openNow(s: Setup = {}) {
  window.location.hash = "#/a/alpha/now";
  const f = installFakes(setup(s));
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  await screen.findByRole("heading", { name: "いま進められること" });
  return f;
}

const waitingList = () => screen.queryByTestId("waiting-list");
const rowOf = (kind: string) =>
  within(screen.getByTestId("waiting-list")).getByText(
    (_, el) => el?.getAttribute("data-kind") === kind,
    { selector: "li" },
  );
const ingestCalls = (requests: { url: string }[]) =>
  requests.filter((r) => r.url.includes("ingestable=true"));

const planOnly: NH = {
  challenges: [ch("C-1", "awaiting_plan_approval", "計画の題名")],
};

beforeEach(() => {
  window.location.hash = "";
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("取り込めるものを読むとき", () => {
  it("AC-115 #805: 変化の合図を受けても、ingestable=true の状況の API は呼ばれない", async () => {
    const { requests } = await openNow();
    await screen.findByText("取り込みは、ターミナルか対話で行います。");
    expect(ingestCalls(requests)).toHaveLength(1);
    await act(async () => {
      FakeEventSource.latest.send({
        type: "workspace_changed",
        workspace: "alpha",
      });
    });
    await waitFor(() =>
      expect(
        requests.filter((r) => r.url === `${P}/status`).length,
      ).toBeGreaterThan(1),
    );
    expect(ingestCalls(requests)).toHaveLength(1);
  });

  it("AC-116 #805: 60 秒ごとの読み直しでも、ingestable=true の状況の API は呼ばれない", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { requests } = await openNow();
    await screen.findByText("取り込みは、ターミナルか対話で行います。");
    const before = requests.filter((r) => r.url === `${P}/cycle`).length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS + 100);
    });
    expect(
      requests.filter((r) => r.url === `${P}/cycle`).length,
    ).toBeGreaterThan(before);
    expect(ingestCalls(requests)).toHaveLength(1);
  });

  it("AC-156 #805: 画面を開くと、ingestable=true の状況の API が 1 回呼ばれる", async () => {
    const { requests } = await openNow();
    await screen.findByText("取り込みは、ターミナルか対話で行います。");
    expect(ingestCalls(requests)).toHaveLength(1);
  });

  it("AC-157 #805: 「読み直す」を押すと、ingestable=true の状況の API が、もう 1 回呼ばれる", async () => {
    const { requests } = await openNow();
    const btn = await screen.findByRole("button", { name: "読み直す" });
    await waitFor(() => expect(btn).toBeEnabled());
    fireEvent.click(btn);
    await waitFor(() => expect(ingestCalls(requests)).toHaveLength(2));
  });

  it("AC-158 #805: 応答が返るまで、取り込めるものの区分に「読んでいます」が出る", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    const responder = (async () => {
      await gate;
      return new Response(
        JSON.stringify({
          ...emptyStatus,
          ingestable: { error: null, items: [] },
        }),
      );
    }) as unknown as FakeResponder;
    await openNow({ ingestable: responder });
    const block = screen
      .getByRole("heading", { name: /^取り込めるもの/ })
      .closest("section") as HTMLElement;
    expect(within(block).getByText("読んでいます")).toBeInTheDocument();
    await act(async () => {
      release();
    });
    await waitFor(() =>
      expect(within(block).queryByText("読んでいます")).toBeNull(),
    );
  });
});

describe("件数の行と見出し", () => {
  it("AC-119 #805: 見出し「いま進められること」が出る", async () => {
    await openNow();
    expect(
      screen.getByRole("heading", { name: "いま進められること" }),
    ).toBeInTheDocument();
  });

  it("AC-120 #805: 件数の行に、あなたを待っているものの件数が出る", async () => {
    await openNow({
      nh: {
        challenges: [
          ch("C-1", "awaiting_plan_approval"),
          ch("C-2", "awaiting_completion_approval"),
        ],
      },
    });
    expect(screen.getByTestId("count-waiting")).toHaveTextContent(
      "あなたを待っているもの 2 件",
    );
  });

  it("AC-121 #805: 件数の行に、取り込めるものの件数（ingestable.items の件数）が出る", async () => {
    await openNow({
      ingestable: { error: null, items: [item(1), item(2), item(3)] },
    });
    await waitFor(() =>
      expect(screen.getByTestId("count-ingestable")).toHaveTextContent(
        "取り込めるもの 3 件",
      ),
    );
  });

  it("AC-122 #805: 件数の行に、サイクルを回すと進むものの件数（cycle_work.total）が出る", async () => {
    await openNow({ stages: { plan: 2, run: 3 } });
    expect(screen.getByTestId("count-cycle-work")).toHaveTextContent(
      "サイクルを回すと進むもの 5 件",
    );
  });

  it("AC-123 #805: 件数の行に、未のフォローアップの件数（followups.open）が出る", async () => {
    await openNow({ followups: 3 });
    expect(screen.getByTestId("count-followups")).toHaveTextContent(
      "未のフォローアップ 3 件",
    );
  });

  it("AC-176 #805: followups.open が 3 のとき、件数の 3 と、フォローアップの一覧へのリンクが出る", async () => {
    await openNow({ followups: 3 });
    const link = screen.getByTestId("count-followups");
    expect(link).toHaveTextContent("3");
    expect(link.getAttribute("href")).toBe("#/a/alpha/followups");
  });
});

describe("あなたを待っている", () => {
  it("AC-124 #805: 計画の承認待ちの課題は、種類「計画の承認」と課題の題名が出る", async () => {
    await openNow({ nh: planOnly });
    const row = rowOf("plan_approval");
    expect(within(row).getByText("計画の承認")).toBeInTheDocument();
    expect(within(row).getByText("計画の題名")).toBeInTheDocument();
  });

  it("AC-125 #805: 計画の承認の行のリンクは、計画の承認の画面を指す", async () => {
    await openNow({ nh: planOnly });
    expect(
      within(rowOf("plan_approval")).getByRole("link").getAttribute("href"),
    ).toBe("#/a/alpha/challenges/C-1/approve-plan");
  });

  const completion: NH = {
    challenges: [ch("C-2", "awaiting_completion_approval", "完了の題名")],
  };
  it("AC-126 #805: awaiting_completion_approval の課題は、種類「完了の承認」と題名が出る", async () => {
    await openNow({ nh: completion });
    const row = rowOf("completion_approval");
    expect(within(row).getByText("完了の承認")).toBeInTheDocument();
    expect(within(row).getByText("完了の題名")).toBeInTheDocument();
  });
  it("AC-127 #805: 完了の承認の行のリンクは、その課題の完了承認画面を指す", async () => {
    await openNow({ nh: completion });
    expect(
      within(rowOf("completion_approval"))
        .getByRole("link")
        .getAttribute("href"),
    ).toBe("#/a/alpha/challenges/C-2/approve-completion");
  });

  const human: NH = { challenges: [ch("C-3", "awaiting_human", "回答の題名")] };
  it("AC-128 #805: awaiting_human の課題は、種類「回答」と問いの一覧の問いの文が出る", async () => {
    await openNow({
      nh: human,
      questions: [{ challenge_id: "C-3", question: "どちらにしますか" }],
    });
    const row = rowOf("answer");
    expect(within(row).getByText("回答")).toBeInTheDocument();
    expect(within(row).getByText("どちらにしますか")).toBeInTheDocument();
  });
  it("AC-148 #805: 同じ課題に問いが 2 つあっても、回答の行は課題ごとに 1 行（左の列の数え方と同じ）", async () => {
    await openNow({
      nh: human,
      questions: [
        { challenge_id: "C-3", question: "問い A" },
        { challenge_id: "C-3", question: "問い B" },
      ],
    });
    expect(screen.getByTestId("count-waiting")).toHaveTextContent("1 件");
    expect(within(rowOf("answer")).getByText(/問い B/)).toBeInTheDocument();
  });
  it("AC-129 #805: 問いの task_id があるとき、回答の行のリンクは、そのタスクの詳細を指す", async () => {
    await openNow({
      nh: human,
      questions: [{ challenge_id: "C-3", task_id: "T-9" }],
    });
    expect(within(rowOf("answer")).getByRole("link").getAttribute("href")).toBe(
      "#/a/alpha/challenges/C-3/tasks/T-9",
    );
  });
  it("AC-130 #805: 問いの task_id が null のとき、回答の行のリンクは、その課題の詳細を指す", async () => {
    await openNow({
      nh: human,
      questions: [{ challenge_id: "C-3", task_id: null }],
    });
    expect(within(rowOf("answer")).getByRole("link").getAttribute("href")).toBe(
      "#/a/alpha/challenges/C-3",
    );
  });
  it("AC-131 #805: 問いが一覧に無いとき、回答の行に課題の題名が出て、課題の詳細へ向かう", async () => {
    await openNow({ nh: human, questions: [] });
    const row = rowOf("answer");
    expect(within(row).getByText("回答の題名")).toBeInTheDocument();
    expect(within(row).getByRole("link").getAttribute("href")).toBe(
      "#/a/alpha/challenges/C-3",
    );
  });

  const op = {
    approved_head: null,
    challenge_id: "C-4",
    closed_at: null,
    closed_reason: null,
    consecutive_failures: 0,
    created_at: "2026-11-01T00:00:00Z",
    executed_at: null,
    id: "OP-1",
    kind: "release",
    last_execution_detail: null,
    last_execution_result: null,
    merge_commit: null,
    ref: null,
    state: "pending",
    summary: "本番へ反映する",
    superseded_by: null,
    supersedes: null,
    version: 1,
    waiting_since: null,
  };
  it("AC-132 #805: 承認を待つ不可逆操作は、種類「不可逆操作の承認」の行が出る", async () => {
    await openNow({ nh: { operations: [op] } as NH });
    expect(
      within(rowOf("operation")).getByText("不可逆操作の承認"),
    ).toBeInTheDocument();
  });
  it("AC-133 #805: 不可逆操作の承認の行のリンクは、その操作の課題の詳細を指す", async () => {
    await openNow({ nh: { operations: [op] } as NH });
    expect(
      within(rowOf("operation")).getByRole("link").getAttribute("href"),
    ).toBe("#/a/alpha/challenges/C-4");
  });

  const disc = (id: string, kinds = ["upstream_closed"]) => ({
    challenge_id: id,
    kinds,
  });
  it("AC-134 #805: 食い違いがあるとき、種類「食い違い」の行が出る", async () => {
    await openNow({ nh: { discrepancies: [disc("C-5")] } as NH });
    expect(
      within(rowOf("discrepancy")).getByText("食い違い"),
    ).toBeInTheDocument();
  });
  it("AC-135 #805: 食い違いの行のリンクは、その課題の詳細を指す", async () => {
    await openNow({ nh: { discrepancies: [disc("C-5")] } as NH });
    expect(
      within(rowOf("discrepancy")).getByRole("link").getAttribute("href"),
    ).toBe("#/a/alpha/challenges/C-5");
  });
  it("AC-136 #805: 同じ課題に食い違いの要素が 2 つあっても、食い違いの行は 1 行", async () => {
    await openNow({
      nh: {
        discrepancies: [disc("C-5"), disc("C-5", ["upstream_updated"])],
      } as NH,
    });
    expect(
      within(screen.getByTestId("waiting-list")).getAllByText(
        (_, el) => el?.getAttribute("data-kind") === "discrepancy",
        { selector: "li" },
      ),
    ).toHaveLength(1);
  });

  const budget = {
    challenge_id: "C-6",
    impl_remaining_usd: 0,
    plan_version: 1,
    review_remaining_usd: 0,
    task_id: null,
  };
  it("AC-137 #805: 予算を使い切った課題は、種類「予算の増額」の行が出る", async () => {
    await openNow({ nh: { budget_exhausted: [budget] } as NH });
    expect(within(rowOf("budget")).getByText("予算の増額")).toBeInTheDocument();
  });
  it("AC-138 #805: 予算の増額の行のリンクは、その課題の詳細を指す", async () => {
    await openNow({ nh: { budget_exhausted: [budget] } as NH });
    expect(within(rowOf("budget")).getByRole("link").getAttribute("href")).toBe(
      "#/a/alpha/challenges/C-6",
    );
  });

  const triage = { challenge_id: "C-7", reason: "判断できない", run_id: "R-1" };
  it("AC-139 #805: トリアージを待つ課題は、種類「トリアージ」の行が出る", async () => {
    await openNow({ nh: { triage: [triage] } as NH });
    expect(within(rowOf("triage")).getByText("トリアージ")).toBeInTheDocument();
  });
  it("AC-140 #805: トリアージの行のリンクは、その課題の詳細を指す", async () => {
    await openNow({ nh: { triage: [triage] } as NH });
    expect(within(rowOf("triage")).getByRole("link").getAttribute("href")).toBe(
      "#/a/alpha/challenges/C-7",
    );
  });

  const slot = { path: "/s/1", repo: "o/r", run_id: null, slot_id: "S-1" };
  it("AC-141 #805: 片付けを待つスロットは、種類「スロットの片付け」とスロットの ID が出る", async () => {
    await openNow({ nh: { slots: [slot] } as NH });
    const row = rowOf("slot");
    expect(within(row).getByText("スロットの片付け")).toBeInTheDocument();
    expect(within(row).getByText("S-1")).toBeInTheDocument();
  });
  it("AC-142 #805: スロットの片付けの行に、ターミナルのコマンドの案内が出る", async () => {
    await openNow({ nh: { slots: [slot] } as NH });
    expect(
      within(rowOf("slot")).getByText(
        /flywheel slot clear S-1 --workspace '\/w\/alpha'/,
      ),
    ).toBeInTheDocument();
  });

  const all: NH = {
    // 並びが API の順に引きずられないよう、逆順に渡す
    slots: [slot],
    triage: [triage],
    budget_exhausted: [budget],
    discrepancies: [disc("C-5")],
    operations: [op],
    challenges: [
      ch("C-3", "awaiting_human"),
      ch("C-2", "awaiting_completion_approval"),
      ch("C-1", "awaiting_plan_approval"),
    ],
  };
  it("AC-143 #805: 8 つの種類が 1 つずつあるとき、計画の承認・完了の承認・不可逆操作の承認・回答・食い違い・予算の増額・トリアージ・スロットの片付けの順", async () => {
    await openNow({ nh: all });
    const kinds = within(screen.getByTestId("waiting-list"))
      .getAllByRole("listitem")
      .map((li) => li.getAttribute("data-kind"));
    expect(kinds).toEqual([
      "plan_approval",
      "completion_approval",
      "operation",
      "answer",
      "discrepancy",
      "budget",
      "triage",
      "slot",
    ]);
  });
  it("AC-144 #805: 計画の承認の種類の印は、data-kind-group が approval", async () => {
    await openNow({ nh: all });
    expect(
      within(rowOf("plan_approval"))
        .getByText("計画の承認")
        .getAttribute("data-kind-group"),
    ).toBe("approval");
  });
  it("AC-145 #805: 完了の承認の種類の印は、data-kind-group が approval", async () => {
    await openNow({ nh: all });
    expect(
      within(rowOf("completion_approval"))
        .getByText("完了の承認")
        .getAttribute("data-kind-group"),
    ).toBe("approval");
  });
  it("AC-146 #805: 不可逆操作の承認の種類の印は、data-kind-group が approval", async () => {
    await openNow({ nh: all });
    expect(
      within(rowOf("operation"))
        .getByText("不可逆操作の承認")
        .getAttribute("data-kind-group"),
    ).toBe("approval");
  });
  it("AC-147 #805: 回答の種類の印は、data-kind-group を持たない", async () => {
    await openNow({ nh: all });
    expect(
      within(rowOf("answer")).getByText("回答").hasAttribute("data-kind-group"),
    ).toBe(false);
  });
  it("AC-148 #805: 見出しの件数は、その区分の行の数と同じ", async () => {
    await openNow({ nh: all });
    const rows = within(screen.getByTestId("waiting-list")).getAllByRole(
      "listitem",
    );
    expect(
      screen.getByRole("heading", {
        name: `あなたを待っている（${rows.length} 件）`,
      }),
    ).toBeInTheDocument();
    expect(rows).toHaveLength(8);
  });
  it("AC-149 #805: 0 件のとき、「あなたを待っているものはありません。」が出る", async () => {
    await openNow();
    expect(waitingList()).toBeNull();
    expect(
      screen.getByText("あなたを待っているものはありません。"),
    ).toBeInTheDocument();
  });
});

describe("取り込めるもの", () => {
  const two: Ingestable = {
    error: null,
    items: [
      item(1, {
        children: [1, 2, 3].map((n) => ({
          external_key: `o/r#1${n}`,
          number: 10 + n,
          parent_external_key: "o/r#1",
          title: `子の題名 ${n}`,
          url: `https://example.invalid/1${n}`,
        })),
      }),
      item(2, { parent_challenge_id: "C-9" }),
    ],
  };
  const withTitles = {
    challenges: [ch("C-9", "in_progress", "親の課題の題名")],
  };

  it("AC-150 #805: 取り込めるものが 2 件のとき、2 件の題名が出る", async () => {
    await openNow({ ingestable: two });
    const list = await screen.findByTestId("ingestable-list");
    expect(within(list).getByText("取り込み元 1")).toBeInTheDocument();
    expect(within(list).getByText("取り込み元 2")).toBeInTheDocument();
  });
  it("AC-151 #805: 題名は、その url へのリンク", async () => {
    await openNow({ ingestable: two });
    const a = (await screen.findByText("取り込み元 1")).closest("a");
    expect(a?.getAttribute("href")).toBe("https://example.invalid/1");
  });
  it("#798: 取り込めるものの行と子の行は、行き先が http(s) 以外（javascript: など）なら、リンクにせず文字として出す", async () => {
    await openNow({
      ingestable: {
        error: null,
        items: [
          item(7, {
            title: "危ない行",
            url: "javascript:alert(1)",
            children: [
              {
                external_key: "o/r#71",
                number: 71,
                parent_external_key: "o/r#7",
                title: "危ない子",
                url: "javascript:alert(2)",
              },
            ],
          }),
        ],
      },
    });
    expect((await screen.findByText("危ない行")).closest("a")).toBeNull();
    expect(screen.getByText("危ない子").closest("a")).toBeNull();
    for (const el of document.querySelectorAll("a")) {
      expect(el.getAttribute("href") ?? "").not.toMatch(/^javascript:/i);
    }
  });
  it("AC-152 #805: 子が 3 件ある行に、子の件数の 3 が出る", async () => {
    await openNow({ ingestable: two });
    const row = (await screen.findByText("取り込み元 1")).closest("li");
    expect(within(row as HTMLElement).getByText("子 3 件")).toBeInTheDocument();
  });
  it("AC-153 #805: 子がある行の下に、子の題名が出る", async () => {
    await openNow({ ingestable: two });
    const row = (await screen.findByText("取り込み元 1")).closest("li");
    for (const n of [1, 2, 3]) {
      expect(
        within(row as HTMLElement).getByText(`子の題名 ${n}`),
      ).toBeInTheDocument();
    }
  });
  it("AC-154 #805: 行に、取り込むコマンド（flywheel ingest と external_key）が出る", async () => {
    await openNow({ ingestable: two });
    const row = (await screen.findByText("取り込み元 2")).closest("li");
    expect(
      within(row as HTMLElement).getByText(
        "flywheel ingest 'o/r#2' --workspace '/w/alpha'",
      ),
    ).toBeInTheDocument();
  });
  it("AC-155 #805: parent_challenge_id がある行に、その課題の題名が出る", async () => {
    await openNow({ ingestable: two, ...withTitles });
    const row = (await screen.findByText("取り込み元 2")).closest("li");
    expect(
      within(row as HTMLElement).getByText("親の課題の題名"),
    ).toBeInTheDocument();
  });
  it("AC-159 #805: ingestable.error があるとき、その文が出る", async () => {
    await openNow({ ingestable: { error: "gh が見つかりません", items: [] } });
    expect(await screen.findByText("gh が見つかりません")).toBeInTheDocument();
  });
  it("AC-160 #805: error と items の両方があるとき、読めた分の題名も出る", async () => {
    await openNow({
      ingestable: { error: "一部が読めません", items: [item(1)] },
    });
    expect(await screen.findByText("一部が読めません")).toBeInTheDocument();
    expect(screen.getByText("取り込み元 1")).toBeInTheDocument();
  });
  it("AC-161 #805: 取り込めるものの区分に、「取り込みは、ターミナルか対話で行います。」が出る", async () => {
    await openNow();
    const block = screen
      .getByRole("heading", { name: /^取り込めるもの/ })
      .closest("section") as HTMLElement;
    expect(
      within(block).getByText("取り込みは、ターミナルか対話で行います。"),
    ).toBeInTheDocument();
  });
  it("AC-162 #805: 取り込めるものの区分に、ボタンとチェックの欄は無い（「読み直す」を除く）", async () => {
    await openNow({ ingestable: two });
    const row = (await screen.findByText("取り込み元 1")).closest(
      "section",
    ) as HTMLElement;
    const names = within(row)
      .getAllByRole("button")
      .map((b) => b.textContent);
    expect(names).toEqual(["読み直す"]);
    expect(row.querySelector("input")).toBeNull();
  });
});

describe("サイクルを回すと進むもの", () => {
  it("AC-163 #805: 合計が 5 のとき、区分の見出しに 5 が出る", async () => {
    await openNow({ stages: { plan: 2, run: 3 } });
    expect(
      screen.getByRole("heading", { name: "サイクルを回すと進むもの（5 件）" }),
    ).toBeInTheDocument();
  });
  it("AC-164 #805: plan が 2 のとき、「計画」と件数の 2 が出る", async () => {
    await openNow({ stages: { plan: 2 } });
    expect(
      within(screen.getByTestId("cycle-stages"))
        .getByText("計画")
        .closest("li"),
    ).toHaveTextContent("計画 2");
  });
  it("AC-165 #805: 8 つの段がどれも 1 以上のとき、8 つの語が出る", async () => {
    await openNow({
      stages: {
        release: 1,
        classify: 1,
        plan: 1,
        run: 1,
        verify: 1,
        learn: 1,
        improve: 1,
        cleanup: 1,
      },
    });
    const items = within(screen.getByTestId("cycle-stages")).getAllByRole(
      "listitem",
    );
    expect(items.map((li) => li.textContent?.replace(/\s*1$/, ""))).toEqual([
      "反映",
      "分類",
      "計画",
      "委譲",
      "完了の検証",
      "学び",
      "改修の提案",
      "片付け",
    ]);
  });
  it("AC-166 #805: 件数が 0 の段は出ない", async () => {
    await openNow({ stages: { plan: 2 } });
    const list = screen.getByTestId("cycle-stages");
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect(within(list).queryByText("分類")).toBeNull();
  });

  it("AC-167 #805: 合計 1 以上で idle のとき、「サイクルを回す」を押すと POST …/cycle が 1 回呼ばれる", async () => {
    const { requests } = await openNow({ stages: { plan: 1 } });
    fireEvent.click(screen.getByRole("button", { name: "サイクルを回す" }));
    await waitFor(() =>
      expect(
        requests.filter((r) => r.method === "POST" && r.url === `${P}/cycle`),
      ).toHaveLength(1),
    );
  });
  it("AC-168 #805: 合計が 0 のとき、「サイクルを回す」は押せない", async () => {
    await openNow();
    expect(
      screen.getByRole("button", { name: "サイクルを回す" }),
    ).toBeDisabled();
  });
  it("AC-169 #805: 合計が 0 のとき、「進められるものが無いので、サイクルは始めません。」が出る", async () => {
    await openNow();
    expect(
      screen.getByText("進められるものが無いので、サイクルは始めません。"),
    ).toBeInTheDocument();
  });
  it("AC-170 #805: サイクルが走っている（running）とき、「サイクルを回す」は押せない", async () => {
    await openNow({ stages: { plan: 1 }, cycle: "running" });
    expect(
      screen.getByRole("button", { name: "サイクルを回す" }),
    ).toBeDisabled();
  });
  it("AC-171 #805: state が stale のとき、「サイクルを回す」は押せない", async () => {
    await openNow({ stages: { plan: 1 }, cycle: "stale" });
    expect(
      screen.getByRole("button", { name: "サイクルを回す" }),
    ).toBeDisabled();
  });
  it("AC-172 #805: state が stale のとき、ターミナルのコマンド flywheel cycle の案内が出る", async () => {
    await openNow({ stages: { plan: 1 }, cycle: "stale" });
    expect(
      screen.getByText("flywheel cycle --workspace '/w/alpha'"),
    ).toBeInTheDocument();
  });
  it("AC-173 #805: 応答の started が真のとき、「サイクルを始めました。」が出る", async () => {
    await openNow({ stages: { plan: 1 }, post: { started: true } });
    fireEvent.click(screen.getByRole("button", { name: "サイクルを回す" }));
    expect(
      await screen.findByText("サイクルを始めました。"),
    ).toBeInTheDocument();
  });
  it("AC-174 #805: 応答の started が偽のとき、「進められるものが無かったので、始めませんでした。」が出る", async () => {
    await openNow({ stages: { plan: 1 }, post: { started: false } });
    fireEvent.click(screen.getByRole("button", { name: "サイクルを回す" }));
    expect(
      await screen.findByText(
        "進められるものが無かったので、始めませんでした。",
      ),
    ).toBeInTheDocument();
  });
  it("AC-175 #805: コード locked で失敗したとき、ほかのサイクルが走っていて始められなかった旨が出る", async () => {
    const locked: FakeResponder = () =>
      new Response(
        JSON.stringify({ error: { code: "locked", message: "locked" } }),
        { status: 409 },
      );
    await openNow({ stages: { plan: 1 }, post: locked });
    fireEvent.click(screen.getByRole("button", { name: "サイクルを回す" }));
    expect(
      await screen.findByText(/ほかのサイクルが走っている/),
    ).toBeInTheDocument();
  });
});

describe("サイクルを回す: 拒否されたとき", () => {
  it.each([
    ["403 forbidden_origin", 403, "forbidden_origin"],
    ["403 だけ", 403, "something"],
    ["forbidden_origin だけ", 400, "forbidden_origin"],
  ])(
    "#798: 403・forbidden_origin（%s）では再読み込みを促し、再送しない",
    async (_n, status, code) => {
      const denied: FakeResponder = () =>
        new Response(JSON.stringify({ error: { code, message: "拒否" } }), {
          status,
        });
      const { requests } = await openNow({
        stages: { plan: 1 },
        post: denied,
      });
      fireEvent.click(screen.getByRole("button", { name: "サイクルを回す" }));
      const note = await screen.findByText(/ページを再読み込みして/);
      expect(note).toHaveTextContent(RELOAD_TEXT);
      expect(note.getAttribute("role")).toBe("alert");
      await new Promise((r) => setTimeout(r, 50));
      expect(requests.filter((r) => r.method === "POST")).toHaveLength(1);
    },
  );
});

describe("何も無いとき", () => {
  it("AC-177 #805: 待っているもの・取り込めるもの・進むものが全部 0 件のとき、「いま進められるものはありません。」が出る", async () => {
    await openNow();
    expect(
      await screen.findByText("いま進められるものはありません。"),
    ).toBeInTheDocument();
  });
  it("AC-178 #805: 応答が返るまで、「いま進められるものはありません。」は出ない", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    const responder = (async () => {
      await gate;
      return new Response(
        JSON.stringify({
          ...emptyStatus,
          ingestable: { error: null, items: [] },
        }),
      );
    }) as unknown as FakeResponder;
    await openNow({ ingestable: responder });
    expect(screen.queryByText("いま進められるものはありません。")).toBeNull();
    await act(async () => {
      release();
    });
    expect(
      await screen.findByText("いま進められるものはありません。"),
    ).toBeInTheDocument();
  });
});
