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
import type {
  ApprovalPlan,
  ApprovalSummary,
  PlanTopic,
} from "../api/types.gen.ts";
import {
  FakeEventSource,
  emptyStatus,
  fixtures,
  installFakes,
} from "../testing/fakes.ts";

// 計画の承認の画面の受入基準を 1 つずつ直接確かめる。Issue #811（親 #798）。
// 名前の AC-<番号> が受入基準の番号。

const APPROVAL = "/api/v1/workspaces/alpha/challenges/C-1/approval";
const APPROVE = "POST /api/v1/workspaces/alpha/challenges/C-1/approve";
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status });

beforeEach(() => {
  const m = document.createElement("meta");
  m.name = "flywheel-csrf";
  m.content = "tok";
  document.head.append(m);
});
afterEach(() => {
  vi.unstubAllGlobals();
  for (const m of document.head.querySelectorAll("meta[name=flywheel-csrf]"))
    m.remove();
  cleanup();
  window.location.hash = "";
});

const plan = (over: Partial<ApprovalPlan> = {}): ApprovalPlan => ({
  body: "過去の形の本文",
  budget_usd: 30,
  summary: "方針の本文",
  tasks: [
    { key: "a", title: "調べる", depends_on: [], phase: "next", source: null },
    {
      key: "b",
      title: "作る",
      depends_on: ["a"],
      phase: "blocked",
      source: null,
    },
  ],
  topics: [],
  uses_integration: null,
  version: 1,
  ...over,
});

const topic = (over: Partial<PlanTopic> = {}): PlanTopic => ({
  title: "論点の題",
  decision: "決めたこと",
  questions: [
    {
      number: 1,
      question: "どちらにしますか。",
      recommended: 2,
      options: [
        { label: "案A", example: null },
        { label: "案B", example: null },
      ],
      answer: null,
    },
  ],
  ...over,
});

const summary = (over: Partial<ApprovalSummary> = {}) => ({
  approval: {
    challenge_id: "C-1",
    conditions_after_release: [],
    conditions_after_release_heading: "",
    direct_completion: null,
    done_criteria: "完了条件",
    kind: "plan",
    mergeability_note: "",
    other_pending_operations: [],
    pairs: [],
    plan: plan(),
    release_held: false,
    releases: [],
    stages: {
      actual: null,
      expected: null,
      figure: null,
      issues: null,
      issues_answer: null,
      non_goals: null,
    },
    title: "通知の再送",
    version: 7,
    ...over,
  } as ApprovalSummary,
});

interface Opts {
  extra?: Record<string, unknown>;
  cycle?: string;
  total?: number;
}

async function open(approval: unknown, o: Opts = {}) {
  window.location.hash = "#/a/alpha/challenges/C-1/approve-plan";
  const f = fixtures();
  f["/api/v1/workspaces/alpha/challenges"] = {
    challenges: [
      {
        id: "C-1",
        title: "通知の再送",
        status: "awaiting_plan_approval",
        card: {
          context: null,
          headline: null,
          modifiers: [],
          next_human_actions: ["approve_plan"],
          state: "awaiting_plan_approval",
        },
      },
    ],
  };
  f["/api/v1/workspaces/alpha/cycle"] = {
    last_ended_at: null,
    last_result: null,
    last_started_at: null,
    state: o.cycle ?? "idle",
  };
  f["/api/v1/workspaces/alpha/status"] = {
    ...emptyStatus,
    cycle_work: { ...emptyStatus.cycle_work, total: o.total ?? 0 },
  };
  const r = installFakes({ ...f, [APPROVAL]: approval, ...o.extra });
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  return r;
}

const opened = async (s = summary(), o: Opts = {}) => {
  const r = await open(s, o);
  await screen.findByTestId("plan-approval");
  return r;
};

it("AC-93 #811: 上の帯の「課題」に、選んでいる印が付く", async () => {
  await opened();
  expect(screen.getByRole("link", { name: "課題" })).toHaveAttribute(
    "aria-current",
    "page",
  );
});

it("AC-311 #811: 画面を開くと、承認の要約の API が呼ばれる", async () => {
  const { requests } = await opened();
  expect(requests.some((r) => r.url === APPROVAL && r.method === "GET")).toBe(
    true,
  );
});

it("AC-312 #811: 見出し「計画の承認: 」と課題の題名が出る", async () => {
  await opened();
  expect(
    screen.getByRole("heading", { level: 2, name: "計画の承認: 通知の再送" }),
  ).toBeInTheDocument();
});

it("AC-313 #811: budget_usd が 30 のとき、予算の上限の 30 が出る", async () => {
  await opened();
  expect(screen.getByTestId("plan-budget")).toHaveTextContent("30");
});

it("AC-314 #811: budget_usd が null のとき、予算の上限に「未設定」が出る", async () => {
  await opened(summary({ plan: plan({ budget_usd: null }) }));
  expect(screen.getByTestId("plan-budget")).toHaveTextContent("未設定");
});

it("AC-315 #811: uses_integration が真のとき、統合先の「使う」が出る", async () => {
  await opened(summary({ plan: plan({ uses_integration: true }) }));
  expect(screen.getByTestId("plan-integration")).toHaveTextContent("使う");
});

it("AC-316 #811: uses_integration が null のとき、統合先は出ない", async () => {
  await opened();
  expect(screen.queryByText("統合先")).toBeNull();
  expect(screen.queryByTestId("plan-integration")).toBeNull();
});

it("AC-317 #811: 方針（plan.summary）が出る", async () => {
  await opened();
  expect(
    within(screen.getByTestId("plan-summary")).getByText("方針の本文"),
  ).toBeInTheDocument();
});

it("#798: 計画の中身（方針・ツリー）は左の区画に、「問いと推奨」はその隣の右の区画に入る", async () => {
  await opened(summary({ plan: plan({ summary: "方針の本文" }) }));
  const cols = screen.getByTestId("plan-cols");
  const main = screen.getByTestId("plan-main");
  const side = screen.getByTestId("plan-topics");
  // 2 つの区画が、並びの枠の直下に、左（計画の中身）・右（問いと推奨）の順で入る
  expect(Array.from(cols.children)).toEqual([main, side]);
  expect(main).toContainElement(screen.getByTestId("plan-summary"));
  expect(main).not.toContainElement(side);
});

it("AC-318 #811: 完了したら見えるものの、場面・いま・終わった後が出る", async () => {
  await opened(
    summary({
      stages: {
        actual: null,
        figure: null,
        issues: null,
        issues_answer: null,
        non_goals: null,
        expected: [
          {
            kind: "ui",
            scene: "通知の画面",
            before: "2 通届く",
            after: "1 通届く",
          },
        ],
      },
    }),
  );
  const s = screen.getByRole("region", { name: "完了したら見えるもの" });
  expect(s).toHaveTextContent("通知の画面");
  expect(s).toHaveTextContent("2 通届く");
  expect(s).toHaveTextContent("1 通届く");
});

it("AC-319 #811: やらないこと（non_goals）の要素が出る", async () => {
  await opened(
    summary({
      stages: {
        actual: null,
        figure: null,
        issues: null,
        issues_answer: null,
        expected: null,
        non_goals: ["受信側は作らない", "履歴は残さない"],
      },
    }),
  );
  const s = screen.getByRole("region", { name: "やらないこと" });
  expect(within(s).getByText("受信側は作らない")).toBeInTheDocument();
  expect(within(s).getByText("履歴は残さない")).toBeInTheDocument();
});

const five = Array.from({ length: 5 }, (_, i) => ({
  key: `k${i}`,
  title: `タスク${i}`,
  depends_on: i === 0 ? [] : [`k${i - 1}`],
  phase: i === 0 ? "next" : "blocked",
  source: null,
}));

it("AC-320 #811: tasks が 5 件のとき、ツリーに箱が 5 つ出る", async () => {
  await opened(summary({ plan: plan({ tasks: five }) }));
  expect(screen.getAllByTestId("task-box")).toHaveLength(5);
});

it("AC-321 #811: tasks が 5 件のとき、ツリーの見出しに件数の 5 が出る", async () => {
  await opened(summary({ plan: plan({ tasks: five }) }));
  expect(
    screen.getByRole("heading", { name: "タスクのツリー（5）" }),
  ).toBeInTheDocument();
  expect(screen.getByTestId("plan-task-count")).toHaveTextContent("5");
});

it("AC-322 #811: phase が next のタスクの箱に「次に走る」が出る", async () => {
  await opened();
  const box = screen
    .getAllByTestId("task-box")
    .find((b) => b.getAttribute("data-phase") === "next");
  expect(box).toHaveTextContent("次に走る");
});

it("AC-323 #811: phase が blocked のタスクの箱に「前提を待っている」が出る", async () => {
  await opened();
  const box = screen
    .getAllByTestId("task-box")
    .find((b) => b.getAttribute("data-phase") === "blocked");
  expect(box).toHaveTextContent("前提を待っている");
});

it("AC-324 #811: ツリーの箱は、リンクでない", async () => {
  await opened();
  for (const b of screen.getAllByTestId("task-box")) {
    expect(b.tagName).not.toBe("A");
    expect(b.closest("a")).toBeNull();
  }
});

it("AC-325 #811: figure があるとき、図の部品が出る（無いときは出ない）", async () => {
  const stages = (figure: string | null) => ({
    actual: null,
    expected: null,
    issues: null,
    issues_answer: null,
    non_goals: null,
    figure,
  });
  await opened(summary({ stages: stages("graph TD\n  A --> B") }));
  expect(screen.getByRole("region", { name: "図" })).toBeInTheDocument();
  cleanup();
  await opened(summary({ stages: stages(null) }));
  expect(screen.queryByRole("region", { name: "図" })).toBeNull();
});

const withTopics = (topics: PlanTopic[]) => summary({ plan: plan({ topics }) });

it("AC-326 #811: topics が 2 件のとき、2 つの論点の題名が出る", async () => {
  await opened(
    withTopics([
      topic({ title: "論点その一" }),
      topic({ title: "論点その二" }),
    ]),
  );
  const s = screen.getByTestId("plan-topics");
  expect(within(s).getByText("論点その一")).toBeInTheDocument();
  expect(within(s).getByText("論点その二")).toBeInTheDocument();
});

it("AC-327 #811: 論点の decision が出る", async () => {
  await opened(withTopics([topic({ decision: "決めた内容の文" })]));
  expect(screen.getByText("決めた内容の文")).toBeInTheDocument();
});

it("AC-328 #811: 問いの文が出る", async () => {
  await opened(withTopics([topic()]));
  expect(screen.getByText("どちらにしますか。")).toBeInTheDocument();
});

it("AC-329 #811: 問いの選択肢の、全部のラベルが出る", async () => {
  await opened(withTopics([topic()]));
  const q = screen.getByTestId("plan-question");
  expect(within(q).getByText("案A")).toBeInTheDocument();
  expect(within(q).getByText("案B")).toBeInTheDocument();
});

it("AC-330 #811: 推奨の選択肢だけに「推奨」の印が出る", async () => {
  await opened(withTopics([topic()]));
  const items = within(screen.getByTestId("plan-question")).getAllByRole(
    "listitem",
  );
  expect(items[0]).not.toHaveTextContent("推奨");
  expect(items[1]).toHaveTextContent("推奨");
});

it("AC-331 #811: 答えが記録されている問いに、答えのラベルが答えとして出る", async () => {
  const t = topic();
  const q = t.questions[0];
  if (!q) throw new Error("fixture");
  q.answer = { choice: 1, label: "案A", recommended: false, text: null };
  await opened(withTopics([t]));
  expect(
    within(screen.getByTestId("plan-question")).getByText(/答え:/),
  ).toHaveTextContent("答え: 案A");
});

it("AC-332 #811: 「問いと推奨」に、選べる入力の要素（ラジオ・チェック）は無い", async () => {
  await opened(withTopics([topic()]));
  const s = screen.getByTestId("plan-topics");
  expect(s.querySelector("input, select, textarea")).toBeNull();
  expect(within(s).queryByRole("radio")).toBeNull();
  expect(within(s).queryByRole("checkbox")).toBeNull();
});

it("AC-333 #811: 問いが 1 件以上あるとき、「推奨のままでよければ、そのまま承認できます。」が出る", async () => {
  await opened(withTopics([topic()]));
  expect(
    screen.getByText("推奨のままでよければ、そのまま承認できます。"),
  ).toBeInTheDocument();
});

it("AC-334 #811: 問いが 1 件以上あるとき、flywheel answer と --choice の案内が出る", async () => {
  await opened(withTopics([topic()]));
  const g = screen
    .getByTestId("plan-topics")
    .querySelector("[data-guide='answer-choice']");
  expect(g?.textContent).toContain("flywheel answer C-1");
  expect(g?.textContent).toContain("--choice <問いの番号>=<選択肢の番号>");
});

it("AC-335 #811: 問いが 0 件のとき、「問いはありません。」が出る（推奨の案内は出ない）", async () => {
  await opened(withTopics([]));
  const s = screen.getByTestId("plan-topics");
  expect(within(s).getByText("問いはありません。")).toBeInTheDocument();
  expect(s.textContent).not.toContain("推奨のままでよければ");
});

const approveBtn = () =>
  screen.findByRole("button", { name: "この計画を承認する" });

it("AC-336 #811: 「この計画を承認する」で、承認の口が approval.version を本文にして 1 回呼ばれる", async () => {
  const { requests } = await opened(summary(), {
    extra: { [APPROVE]: {} },
  });
  fireEvent.click(await approveBtn());
  await screen.findByText("承認しました。");
  const posts = requests.filter((r) => r.method === "POST");
  expect(posts).toHaveLength(1);
  expect(posts[0]?.url).toBe("/api/v1/workspaces/alpha/challenges/C-1/approve");
  expect(posts[0]?.body).toBe('{"version":7}');
  // CSRF の値を載せて送る（見直し前の AC-211）
  expect(posts[0]?.headers["X-Flywheel-CSRF"]).toBe("tok");
});

it("#811: 画面が送る要求は、GET と承認の口の POST だけ（見直し前の詳細のテストから移した）", async () => {
  const { requests } = await opened(summary(), {
    extra: { [APPROVE]: {} },
  });
  fireEvent.click(await approveBtn());
  await screen.findByText("承認しました。");
  expect(requests.length).toBeGreaterThan(0);
  for (const r of requests) {
    if (r.method === "GET") continue;
    expect(r.method).toBe("POST");
    expect(r.url).toMatch(/\/challenges\/C-1\/approve$/);
  }
});

it("#811: 一覧の状態の文字列が承認待ちでなくても、要約が計画の承認を返せばボタンが出る（見直し前の AC-209）", async () => {
  await opened(summary(), {
    extra: {
      "/api/v1/workspaces/alpha/challenges": {
        challenges: [
          {
            id: "C-1",
            title: "通知の再送",
            status: "in_progress",
            card: {
              context: null,
              headline: null,
              modifiers: [],
              next_human_actions: ["approve_plan"],
              state: "in_progress",
            },
          },
        ],
      },
    },
  });
  expect(await approveBtn()).toBeEnabled();
});

async function approved(o: Opts = {}) {
  await opened(summary(), { ...o, extra: { [APPROVE]: {} } });
  fireEvent.click(await approveBtn());
  await screen.findByText("承認しました。");
}

it("AC-337 #811: 承認が通ると、「承認しました。」が出る", async () => {
  await approved();
  expect(screen.getByText("承認しました。")).toBeInTheDocument();
});

it("AC-338 #811: 承認が通ると、「続きは、次のサイクルで進みます。」が出る", async () => {
  await approved();
  expect(
    screen.getByText("続きは、次のサイクルで進みます。"),
  ).toBeInTheDocument();
});

it("AC-339 #811: 承認が通ると、課題の詳細へのリンクが出る", async () => {
  await approved();
  expect(
    screen.getByRole("link", { name: "課題の詳細を開く" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-1");
});

it("AC-340 #811: 承認が通ると、「サイクルを回す」のボタンが出る（進められるものがあれば押せる）", async () => {
  await approved({ total: 2 });
  const b = screen.getByRole("button", { name: "サイクルを回す" });
  await waitFor(() => expect(b).toBeEnabled());
});

it("AC-341 #811: cycle_work の合計が 0 のとき、承認の後の「サイクルを回す」は押せない", async () => {
  await approved({ total: 0 });
  expect(screen.getByRole("button", { name: "サイクルを回す" })).toBeDisabled();
});

it("AC-342/343 #811: conflict で失敗すると、計画が変わった旨が出て、要約がもう 1 回読まれる（押し直すと新しい版を送る）", async () => {
  let reads = 0;
  let posts = 0;
  const { requests } = await open(
    () => {
      reads++;
      return json(200, summary({ version: reads === 1 ? 7 : 8 }));
    },
    {
      extra: {
        [APPROVE]: () => {
          posts++;
          return posts === 1
            ? json(409, { error: { code: "conflict", message: "版が違う" } })
            : json(200, {});
        },
      },
    },
  );
  await screen.findByTestId("plan-approval");
  expect(reads).toBe(1);
  fireEvent.click(await approveBtn());
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "計画が変わりました",
  );
  await waitFor(() => expect(reads).toBe(2));
  // 自動では再送しない
  expect(requests.filter((r) => r.method === "POST")).toHaveLength(1);
  fireEvent.click(await approveBtn());
  await screen.findByText("承認しました。");
  expect(
    requests.filter((r) => r.method === "POST").map((r) => r.body),
  ).toEqual(['{"version":7}', '{"version":8}']);
});

// 読み直しで、表示中の計画の版が変わったときは、承認のボタンの近くに知らせを出す。#798 の全体のレビュー。
const UPDATED_TEXT =
  "計画が更新されました。内容を確かめてから承認してください。";

async function openWithVersions(versions: number[], approve?: () => Response) {
  let reads = 0;
  const r = await open(
    () => {
      const v = versions[Math.min(reads, versions.length - 1)];
      reads++;
      return json(200, summary({ version: v }));
    },
    { extra: { [APPROVE]: approve ?? {} } },
  );
  await screen.findByTestId("plan-approval");
  return { ...r, reads: () => reads };
}

const changed = async () => {
  await act(async () => {
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    });
  });
};

it("#798: 読み直しで計画の版が変わると、承認のボタンの近くに知らせ（role=alert）が出る", async () => {
  const { reads } = await openWithVersions([7, 8]);
  expect(screen.queryByText(UPDATED_TEXT)).toBeNull();
  await changed();
  await waitFor(() => expect(reads()).toBe(2));
  const note = await screen.findByText(UPDATED_TEXT);
  expect(note.getAttribute("role")).toBe("alert");
  expect(note.closest(".approval-control")).toBe(
    (await approveBtn()).closest(".approval-control"),
  );
});

it("#798: 読み直しても版が変わらなければ、知らせは出ない", async () => {
  const { reads } = await openWithVersions([7, 7]);
  await changed();
  await waitFor(() => expect(reads()).toBe(2));
  await new Promise((r) => setTimeout(r, 50));
  expect(screen.queryByText(UPDATED_TEXT)).toBeNull();
});

it("#798: 知らせが出た後に押すと、新しい版で承認が送られ、知らせは消える", async () => {
  const { requests, reads } = await openWithVersions([7, 8], () =>
    json(200, {}),
  );
  await changed();
  await waitFor(() => expect(reads()).toBe(2));
  await screen.findByText(UPDATED_TEXT);
  // 押すまでは出したまま（さらに版が変わらない読み直しがあっても消えない）
  await changed();
  await waitFor(() => expect(reads()).toBe(3));
  expect(screen.getByText(UPDATED_TEXT)).toBeInTheDocument();
  fireEvent.click(await approveBtn());
  await screen.findByText("承認しました。");
  expect(
    requests.filter((r) => r.method === "POST").map((r) => r.body),
  ).toEqual(['{"version":8}']);
});

it("AC-344 #811: 承認の口が conflict 以外のコードで失敗すると、エラーの文が出る", async () => {
  await opened(summary(), {
    extra: {
      [APPROVE]: () =>
        json(500, { error: { code: "internal", message: "内部の失敗" } }),
    },
  });
  fireEvent.click(await approveBtn());
  expect(await screen.findByRole("alert")).toHaveTextContent("内部の失敗");
  expect(screen.queryByText("承認しました。")).toBeNull();
});

it("AC-345 #811: 課題の詳細の「計画の本文」へのリンクが出る", async () => {
  await opened();
  expect(
    screen.getByRole("link", { name: "計画の本文を読む" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-1/plan");
});

it("AC-346 #811: 差し戻しのボタンは無い", async () => {
  await opened();
  expect(screen.queryByRole("button", { name: /差し戻/ })).toBeNull();
  expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
    "この計画を承認する",
  ]);
});

it("AC-347 #811: 差し戻しの端末のコマンド（flywheel reject）の案内が出る", async () => {
  await opened();
  expect(
    screen.getByTestId("plan-approval").querySelector("[data-guide='reject']")
      ?.textContent,
  ).toContain("flywheel reject C-1");
});

it("AC-348 #811: kind が plan でない課題では、承認待ちでない旨と課題の詳細へのリンクが出る", async () => {
  await open(summary({ kind: "completion" }));
  await screen.findByText("この課題は、計画の承認待ちではありません。");
  expect(
    screen.getByRole("link", { name: "課題の詳細を開く" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-1");
  expect(
    screen.queryByRole("button", { name: "この計画を承認する" }),
  ).toBeNull();
});

it("AC-349 #811: 要約の API が invalid_transition を返すと、承認待ちでない旨が出る", async () => {
  await open(() =>
    json(409, {
      error: { code: "invalid_transition", message: "承認待ちではない" },
    }),
  );
  await screen.findByText("この課題は、計画の承認待ちではありません。");
});

const legacy = () =>
  summary({
    plan: plan({
      tasks: [],
      summary: null,
      body: "## 過去の節\n\n本文の **太字**",
    }),
  });

it("AC-350 #811: 過去の形の計画では、計画の本文（plan.body）が Markdown として整って出る", async () => {
  await opened(legacy());
  const body = screen.getByTestId("plan-legacy-body");
  expect(
    within(body).getByRole("heading", { name: "過去の節" }),
  ).toBeInTheDocument();
  expect(body.querySelector("strong")?.textContent).toBe("太字");
  expect(screen.queryByTestId("plan-summary")).toBeNull();
  expect(screen.queryByTestId("task-tree")).toBeNull();
});

it("AC-351 #811: 過去の形の計画にも、「この計画を承認する」のボタンが出る", async () => {
  await opened(legacy());
  expect(await approveBtn()).toBeInTheDocument();
});

it("AC-412 #811: 論点の decision の **太字** が、強調の要素になる", async () => {
  await opened(withTopics([topic({ decision: "これは **大事** です" })]));
  const strong = screen.getByTestId("plan-topic").querySelector("strong");
  expect(strong?.textContent).toBe("大事");
});

it.each([
  ["403 forbidden_origin", 403, "forbidden_origin"],
  ["403 だけ", 403, "something"],
  ["forbidden_origin だけ", 400, "forbidden_origin"],
])(
  "#811: 403・forbidden_origin（%s）では再読み込みを促し、再送しない",
  async (_n, status, code) => {
    const { requests } = await opened(summary(), {
      extra: {
        [APPROVE]: () => json(status, { error: { code, message: "拒否" } }),
      },
    });
    fireEvent.click(await approveBtn());
    await screen.findByText(/ページを再読み込みして/);
    await new Promise((r) => setTimeout(r, 50));
    expect(requests.filter((r) => r.method === "POST")).toHaveLength(1);
  },
);

it("#811: 問いが 0 件でも、論点の題名と decision は出る", async () => {
  await opened(withTopics([topic({ title: "問い無しの論点", questions: [] })]));
  const s = screen.getByTestId("plan-topics");
  expect(within(s).getByText("問い無しの論点")).toBeInTheDocument();
  expect(within(s).getByText("問いはありません。")).toBeInTheDocument();
});

it("#811: 過去の形の計画でも、完了したら見えるものとやらないことが出る", async () => {
  const s = legacy();
  s.approval.stages = {
    ...s.approval.stages,
    expected: [{ kind: "ui", scene: "旧の場面", before: "前", after: "後" }],
    non_goals: ["旧のやらないこと"],
  };
  await opened(s);
  expect(screen.getByText("旧のやらないこと")).toBeInTheDocument();
  expect(
    screen.getByRole("region", { name: "完了したら見えるもの" }),
  ).toHaveTextContent("旧の場面");
});

// 見直し前の承認の要約のテスト（parts/approval-summary.test.tsx）が守っていた動きを、新しい画面で守る。

it("#811: 過去の形の計画では、段階の欄の論点と、その回答が出る（見直し前の AC-386）", async () => {
  const s = legacy();
  s.approval.stages = {
    ...s.approval.stages,
    issues: [
      {
        question: "どちらにする？",
        recommended: 2,
        options: [
          { label: "案A", example: null },
          { label: "案B", example: null },
        ],
      },
    ],
    issues_answer: "案Bで決定",
  };
  await opened(s);
  expect(screen.getByText("どちらにする？")).toBeInTheDocument();
  expect(screen.getByText("案Bで決定")).toBeInTheDocument();
});

it("#811: 段階の欄が全部 null の計画では、完了したら見えるもの・やらないことの見出しを出さない", async () => {
  await opened();
  expect(
    screen.queryByRole("region", { name: "完了したら見えるもの" }),
  ).toBeNull();
  expect(screen.queryByRole("region", { name: "やらないこと" })).toBeNull();
  expect(screen.queryByText("論点")).toBeNull();
});

// 過去の形の計画の本文は、加工せずに Markdown として出す。節と引用の文を落とさないこと・画面の側で節を
// 足さないことを固定する（#469）。
const PLAN_HEAD =
  "# 計画\n\n## 対象\n\n- リポジトリ: flywheel\n- 操作: impl\n\n## 完了条件\n\n完了条件の文\n";
const NO_ISSUES_SECTION =
  "\n## 論点を不要とした根拠\n\n1. DECISION-ONE 方式は A に決まっている\n   - 引用元: 上流のコメント\n   - 引用:\n     > QUOTE-ONE A 方式で進める\n";

it("#811: 過去の形の計画の本文に、論点を不要とした根拠の節があれば、その節と引用の文が出る（#469）", async () => {
  await opened(
    summary({
      plan: plan({
        tasks: [],
        summary: null,
        body: PLAN_HEAD + NO_ISSUES_SECTION,
      }),
    }),
  );
  const body = screen.getByTestId("plan-legacy-body");
  expect(
    within(body).getByRole("heading", { name: "論点を不要とした根拠" }),
  ).toBeInTheDocument();
  expect(body).toHaveTextContent("DECISION-ONE 方式は A に決まっている");
  expect(body).toHaveTextContent("引用元: 上流のコメント");
  expect(body.querySelector("blockquote")).toHaveTextContent(
    "QUOTE-ONE A 方式で進める",
  );
});

it("#811: 過去の形の計画の本文に、論点を不要とした根拠の節が無ければ、その節を出さない（#469）", async () => {
  await opened(
    summary({ plan: plan({ tasks: [], summary: null, body: PLAN_HEAD }) }),
  );
  const body = screen.getByTestId("plan-legacy-body");
  expect(
    within(body).getByRole("heading", { name: "完了条件" }),
  ).toBeInTheDocument();
  expect(body.textContent).not.toContain("論点を不要とした根拠");
  expect(body.textContent).not.toContain("QUOTE-ONE");
});
