import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import {
  FakeEventSource,
  emptyStatus,
  installFakes,
} from "../testing/fakes.ts";

// 画面の枠組み（左の列・上の帯・ハッシュでの行き先）の受入基準を、1 つずつ直接確かめる。Issue #803。

interface Agent {
  name: string;
  state?: string;
  error?: string | null;
  cycle?: string;
  needsHuman?: Record<string, unknown>;
}

const nhBase = {
  budget_exhausted: [],
  challenges: [],
  discrepancies: [],
  operations: [],
  slots: [],
  triage: [],
};

function bodiesOf(agents: Agent[]): Record<string, unknown> {
  const b: Record<string, unknown> = {
    "/api/v1/workspaces": {
      workspaces: agents.map((a) => ({
        error: a.error ?? null,
        name: a.name,
        path: `/w/${a.name}`,
        state: a.state ?? "ok",
      })),
    },
    "/api/v1/needs-human": {
      unreadable_workspaces: [],
      workspaces: agents
        .filter((a) => (a.state ?? "ok") === "ok")
        .map((a) => ({
          name: a.name,
          needs_human: { ...nhBase, ...(a.needsHuman ?? {}) },
        })),
    },
    "/api/v1/questions": { questions: [], unreadable_workspaces: [] },
  };
  for (const a of agents) {
    if ((a.state ?? "ok") !== "ok") continue;
    const p = `/api/v1/workspaces/${a.name}`;
    b[`${p}/status`] = emptyStatus;
    b[`${p}/challenges`] = { challenges: [] };
    b[`${p}/cycle`] = {
      last_ended_at: null,
      last_result: null,
      last_started_at: null,
      state: a.cycle ?? "idle",
    };
  }
  return b;
}

async function open(agents: Agent[], hash = "") {
  window.location.hash = hash;
  installFakes(bodiesOf(agents));
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
}

const rail = () => screen.getByRole("navigation", { name: "エージェント" });
const topNav = () => screen.getByRole("navigation", { name: "画面" });

beforeEach(() => {
  window.location.hash = "";
});
afterEach(() => vi.unstubAllGlobals());

it("AC-77 #803: エージェントが 1 つも無いとき「エージェントがありません。」が出る", async () => {
  await open([]);
  expect(
    await screen.findByText("エージェントがありません。"),
  ).toBeInTheDocument();
});

it("AC-78 #803: 3 つのエージェントの名前が、一覧の API の順に左の列へ出る", async () => {
  await open([{ name: "zeta" }, { name: "alpha" }, { name: "mid" }]);
  await within(rail()).findByText("zeta");
  const names = within(rail())
    .getAllByRole("link")
    .map((a) => a.querySelector(".agent-name")?.textContent);
  expect(names).toEqual(["すべて", "zeta", "alpha", "mid"]);
});

it("AC-79 #803: 名前が tom のエージェントの頭文字は「T」と出る", async () => {
  await open([{ name: "tom" }]);
  const link = await within(rail()).findByRole("link", { name: /tom/ });
  expect(link.querySelector(".agent-initial")?.textContent).toBe("T");
});

const planAndAnswer = {
  challenges: [
    { id: "C-1", status: "awaiting_plan_approval" },
    { id: "C-2", status: "awaiting_human" },
  ],
};

it("AC-80 #803: あなたを待っているものが 2 件のエージェントには数字の 2 が出る", async () => {
  await open([
    {
      name: "alpha",
      needsHuman: {
        challenges: [
          { id: "C-1", status: "awaiting_plan_approval" },
          { id: "C-2", status: "awaiting_completion_approval" },
        ],
      },
    },
  ]);
  const link = await within(rail()).findByRole("link", { name: /alpha/ });
  await waitFor(() =>
    expect(within(link).getByTestId("waiting-count")).toHaveTextContent("2"),
  );
});

it("AC-81 #803: 0 件のエージェントには数字が出ない", async () => {
  await open([{ name: "alpha" }, { name: "beta", needsHuman: planAndAnswer }]);
  await waitFor(() =>
    expect(screen.getAllByTestId("waiting-count")).toHaveLength(1),
  );
  const link = within(rail()).getByRole("link", { name: /alpha/ });
  expect(within(link).queryByTestId("waiting-count")).toBeNull();
});

it("AC-82 #803: 承認待ち 1・未回答の問い 1・不可逆操作の承認 1 のとき件数は 3", async () => {
  await open([
    {
      name: "alpha",
      needsHuman: {
        ...planAndAnswer,
        operations: [{ challenge_id: "C-3", id: "OP-1" }],
      },
    },
  ]);
  const link = await within(rail()).findByRole("link", { name: /alpha/ });
  await waitFor(() =>
    expect(within(link).getByTestId("waiting-count")).toHaveTextContent("3"),
  );
});

it("AC-83 #803: 同じ課題に食い違いの要素が 2 つあっても 1 件と数える", async () => {
  await open([
    {
      name: "alpha",
      needsHuman: {
        discrepancies: [
          { challenge_id: "C-1", kinds: ["upstream_closed"] },
          { challenge_id: "C-1", kinds: ["out_of_policy"] },
        ],
      },
    },
  ]);
  const link = await within(rail()).findByRole("link", { name: /alpha/ });
  await waitFor(() =>
    expect(within(link).getByTestId("waiting-count")).toHaveTextContent("1"),
  );
});

it("AC-84 #803: エージェントが 2 つ以上のとき、一番上に「すべて」が出る", async () => {
  await open([{ name: "alpha" }, { name: "beta" }]);
  await within(rail()).findByText("alpha");
  expect(within(rail()).getAllByRole("link")[0]).toHaveTextContent("すべて");
});

it("AC-85 #803: エージェントが 1 つだけのとき「すべて」は出ない", async () => {
  await open([{ name: "alpha" }]);
  await within(rail()).findByText("alpha");
  expect(within(rail()).queryByText("すべて")).toBeNull();
});

it("AC-86 #803: 選んでいるエージェントの要素に aria-current が付く", async () => {
  await open([{ name: "alpha" }, { name: "beta" }], "#/a/beta/now");
  const beta = await within(rail()).findByRole("link", { name: /beta/ });
  expect(beta).toHaveAttribute("aria-current", "page");
  expect(
    within(rail()).getByRole("link", { name: /alpha/ }),
  ).not.toHaveAttribute("aria-current");
});

const unreadable: Agent = {
  name: "gamma",
  state: "store_not_found",
  error: "ストアが見つかりません",
};

it("AC-87 #803: state が ok でないエージェントは、読めない印つきで左の列に出る", async () => {
  await open([{ name: "alpha" }, unreadable]);
  const g = await within(rail()).findByRole("link", { name: /gamma/ });
  expect(g).toHaveAttribute("data-unreadable", "true");
  expect(within(g).getByText("（読めません）")).toBeInTheDocument();
});

it("AC-88 #803: 読めないエージェントを選ぶと、本文に error の文が出る", async () => {
  await open([{ name: "alpha" }, unreadable]);
  fireEvent.click(await within(rail()).findByRole("link", { name: /gamma/ }));
  expect(await screen.findByTestId("agent-error")).toHaveTextContent(
    "ストアが見つかりません",
  );
});

it("AC-89 #803: 上の帯に 4 つの項目がこの順に出る", async () => {
  await open([{ name: "alpha" }]);
  await within(topNav()).findByText("課題");
  expect(
    within(topNav())
      .getAllByRole("link")
      .map((a) => a.textContent),
  ).toEqual(["いま進められること", "課題", "フォローアップ", "サイクル"]);
});

it("AC-90 #803: 上の帯に、選んでいるエージェントの名前が出る", async () => {
  await open([{ name: "alpha" }, { name: "beta" }], "#/a/beta/now");
  expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent(
    "beta",
  );
});

it.each([
  ["AC-94", "running", "サイクル: 走っている"],
  ["AC-95", "idle", "サイクル: 走っていない"],
  ["AC-96", "stale", "サイクル: 止まっているかもしれない"],
])(
  "%s #803: サイクルの state が %s のとき「%s」が出る",
  async (_ac, state, text) => {
    await open([{ name: "alpha", cycle: state }]);
    expect(await screen.findByText(text)).toBeInTheDocument();
  },
);

it("AC-97 #803: 接続が切れているとき「切断中」が出る（つながると消える）", async () => {
  await open([{ name: "alpha" }]);
  await waitFor(() => expect(screen.queryByText("切断中")).toBeNull());
  act(() => FakeEventSource.latest.fail());
  expect(await screen.findByText("切断中")).toBeInTheDocument();
});

it("AC-98 #803: 「すべて」を選んでいるとき、上の帯に 4 つの項目は出ない", async () => {
  await open([{ name: "alpha" }, { name: "beta" }], "#/all");
  await screen.findByRole("heading", { name: "すべてのエージェント" });
  expect(screen.queryByRole("navigation", { name: "画面" })).toBeNull();
});

it("AC-99 #803: 「すべて」を選んでいるとき、上の帯にサイクルの状態は出ない", async () => {
  await open([{ name: "alpha" }, { name: "beta" }], "#/all");
  await screen.findByRole("heading", { name: "すべてのエージェント" });
  expect(screen.queryByTestId("cycle-state")).toBeNull();
});

it("AC-100 #803: ハッシュが無いとき、先頭のエージェントの「いま進められること」が出る", async () => {
  await open([{ name: "alpha" }, { name: "beta" }]);
  expect(
    await screen.findByRole("heading", { name: "いま進められること" }),
  ).toBeInTheDocument();
  expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent(
    "alpha",
  );
});

it("AC-101 #803: ハッシュが #/a/<名前>/challenges のとき課題の画面が出る", async () => {
  await open([{ name: "alpha" }], "#/a/alpha/challenges");
  expect(
    await screen.findByRole("heading", { name: "課題" }),
  ).toBeInTheDocument();
  expect(within(topNav()).getByRole("link", { name: "課題" })).toHaveAttribute(
    "aria-current",
    "page",
  );
});

it("AC-102 #803: 左の列のエージェントを押すと、ハッシュが #/a/<名前>/now になる", async () => {
  await open([{ name: "alpha" }, { name: "beta" }], "#/a/alpha/challenges");
  fireEvent.click(await within(rail()).findByRole("link", { name: /beta/ }));
  await waitFor(() => expect(window.location.hash).toBe("#/a/beta/now"));
  expect(
    await screen.findByRole("heading", { name: "いま進められること" }),
  ).toBeInTheDocument();
});

it("AC-103 #803: 上の帯の「課題」を押すと、ハッシュが #/a/<名前>/challenges になる", async () => {
  await open([{ name: "alpha" }]);
  fireEvent.click(await within(topNav()).findByRole("link", { name: "課題" }));
  await waitFor(() =>
    expect(window.location.hash).toBe("#/a/alpha/challenges"),
  );
});

it("AC-106 #803: 無いエージェントを指す URL は「見つかりません」と「いま進められること」へのリンクが出る", async () => {
  await open([{ name: "alpha" }], "#/a/nobody/now");
  expect(await screen.findByText("見つかりません。")).toBeInTheDocument();
  expect(
    screen.getByRole("link", { name: "「いま進められること」へ" }),
  ).toHaveAttribute("href", "#/a/alpha/now");
});

it("AC-109 #803: 一覧の応答が返るまで「見つかりません」は出ず「読み込んでいます」が出る", async () => {
  window.location.hash = "#/a/nobody/now";
  installFakes({
    ...bodiesOf([{ name: "alpha" }]),
    "/api/v1/workspaces": () =>
      new Response(JSON.stringify({ workspaces: [] }), { status: 200 }),
  });
  render(<App />);
  // EventSource を開く前（一覧を読む前）
  expect(screen.getByText("読み込んでいます")).toBeInTheDocument();
  expect(screen.queryByText("見つかりません。")).toBeNull();
});

it("AC-117 #803: 左の列に、エージェントを足す口は無い", async () => {
  await open([{ name: "alpha" }, { name: "beta" }]);
  await within(rail()).findByText("alpha");
  expect(within(rail()).queryByRole("button")).toBeNull();
  expect(within(rail()).queryByText(/追加|足す|新規/)).toBeNull();
});

it("AC-416 #803: 画面に「グラフ」と「ボード」を切り替える口は無い", async () => {
  await open([{ name: "alpha" }, { name: "beta" }]);
  await within(rail()).findByText("alpha");
  expect(screen.queryByRole("button", { name: /グラフ|ボード/ })).toBeNull();
  expect(screen.queryByRole("link", { name: /グラフ|ボード/ })).toBeNull();
});

it("#803: 課題の詳細は、モーダルではなく通常の本文として出す", async () => {
  window.location.hash = "#/a/alpha/challenges/C-9";
  installFakes({
    ...bodiesOf([{ name: "alpha" }]),
    "/api/v1/workspaces/alpha/challenges": { challenges: [] },
  });
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  // 一覧に無い課題は「見つかりません」
  expect(await screen.findByText("見つかりません。")).toBeInTheDocument();
  expect(document.querySelector("dialog")).toBeNull();
});
