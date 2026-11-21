import { screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { type Agent, openAgents } from "../testing/agents.tsx";
import { waitingItems } from "./items.ts";

// 「すべて」の画面の受入基準を 1 つずつ直接確かめる。Issue #806。

const alpha: Agent = {
  name: "alpha",
  cycle: { state: "running" },
  cards: [{ id: "C-9", title: "不可逆の課題" }],
  needsHuman: {
    challenges: [
      { id: "C-1", status: "awaiting_plan_approval", title: "計画を待つ課題" },
      { id: "C-2", status: "awaiting_human", title: "答えを待つ課題" },
    ],
    operations: [{ challenge_id: "C-9", id: "O-1" }],
  },
};
const beta: Agent = {
  name: "beta",
  cycle: { state: "idle" },
  needsHuman: {
    challenges: [
      {
        id: "C-3",
        status: "awaiting_completion_approval",
        title: "完了を待つ課題",
      },
    ],
  },
};
const gamma: Agent = {
  name: "gamma",
  state: "store_not_found",
  error: "ストアが見つかりません",
};

beforeEach(() => {
  window.location.hash = "";
});
afterEach(() => vi.unstubAllGlobals());

const rows = async () =>
  within(await screen.findByTestId("all-waiting")).getAllByTestId(
    "all-waiting-row",
  );

it("AC-382 #806: 「すべて」の画面に、見出し「すべてのエージェント」が出る", async () => {
  await openAgents([alpha, beta], "#/all");
  expect(
    await screen.findByRole("heading", { name: "すべてのエージェント" }),
  ).toBeInTheDocument();
});

it("AC-383 #806: 「あなたを待っている」に、2 つのエージェントの待っているものが名前つきで出る", async () => {
  await openAgents([alpha, beta], "#/all");
  await screen.findByTestId("all-waiting");
  await waitFor(async () => expect(await rows()).toHaveLength(4));
  const r = await rows();
  expect(r.map((x) => x.querySelector(".all-agent-name")?.textContent)).toEqual(
    ["alpha", "alpha", "alpha", "beta"],
  );
  expect(r[0]).toHaveTextContent("計画の承認");
  expect(r[0]).toHaveTextContent("計画を待つ課題");
  expect(r[3]).toHaveTextContent("完了の承認");
  // 不可逆操作の承認の題名は課題の一覧から引く
  // 並びは種類の順（承認 → 回答）。同じ種類の中は API の並び。
  expect(r[1]).toHaveTextContent("不可逆操作の承認");
  expect(r[1]).toHaveTextContent("不可逆の課題");
  expect(
    screen.getByRole("heading", { name: "あなたを待っている（4 件）" }),
  ).toBeInTheDocument();
});

it("AC-384 #806: 行のリンクは、そのエージェントの画面（#/a/<名前>/…）を指す", async () => {
  await openAgents([alpha, beta], "#/all");
  await waitFor(async () => expect(await rows()).toHaveLength(4));
  const hrefs = (await rows()).map((r) =>
    within(r).getByRole("link").getAttribute("href"),
  );
  expect(hrefs).toEqual([
    "#/a/alpha/challenges/C-1/approve-plan",
    "#/a/alpha/challenges/C-9",
    "#/a/alpha/challenges/C-2",
    "#/a/beta/challenges/C-3/approve-completion",
  ]);
  for (const h of hrefs) expect(h).toMatch(/^#\/a\/(alpha|beta)\//);
});

it("AC-385 #806: 「エージェント」の区分に、エージェントごとのサイクルの状態と、待っているものの件数が出る", async () => {
  await openAgents([alpha, beta], "#/all");
  await waitFor(async () =>
    expect(
      within(
        (await screen.findAllByTestId("all-agent-row"))[0] as HTMLElement,
      ).getByTestId("all-agent-waiting").textContent,
    ).toContain("3"),
  );
  const [a, b] = screen.getAllByTestId("all-agent-row");
  expect(a).toHaveTextContent("alpha");
  expect(a).toHaveTextContent("サイクル: 実行中");
  expect(a).toHaveTextContent("あなたを待っている 3 件");
  expect(b).toHaveTextContent("beta");
  expect(b).toHaveTextContent("サイクル: 走っていない");
  expect(b).toHaveTextContent("あなたを待っている 1 件");
});

it("AC-386 #806: 「エージェント」の区分の名前は、そのエージェントの「いま進められること」を指す", async () => {
  await openAgents([alpha, beta], "#/all");
  const list = await screen.findByTestId("all-agents");
  const link = within(list).getByRole("link", { name: "beta" });
  expect(link.getAttribute("href")).toBe("#/a/beta/now");
  link.click();
  expect(
    await screen.findByRole("heading", { name: "いま進められること" }),
  ).toBeInTheDocument();
  expect(window.location.hash).toBe("#/a/beta/now");
});

it("AC-387 #806: 読めないエージェントは、「エージェント」の区分の行に、読めない旨と理由が出る", async () => {
  await openAgents([alpha, gamma], "#/all");
  const list = await screen.findByTestId("all-agents");
  const row = within(list)
    .getAllByTestId("all-agent-row")
    .find((r) => r.textContent?.includes("gamma"));
  expect(row).toHaveTextContent("読めません: ストアが見つかりません");
  expect(row).not.toHaveTextContent("サイクル:");
  expect(row).not.toHaveTextContent("あなたを待っている");
});

it("#806: あなたを待っているものが無いときは、その旨を出す", async () => {
  await openAgents([{ name: "alpha" }, { name: "beta" }], "#/all");
  expect(
    await screen.findByText("あなたを待っているものはありません。"),
  ).toBeInTheDocument();
});

it("#806: waitingItems は、食い違いを課題ごとに 1 件にし、循環だけの要素を数えない", () => {
  const items = waitingItems(
    "a",
    {
      budget_exhausted: [],
      challenges: [],
      discrepancies: [
        { challenge_id: "C-1", kinds: ["upstream_closed"] },
        { challenge_id: "C-1", kinds: ["upstream_missing"] },
        { challenge_id: "C-2", kinds: ["dependency_cycle"], cycle_only: true },
      ],
      operations: [],
      slots: [{ path: "/s/1", repo: "r", run_id: null, slot_id: "S-1" }],
      triage: [],
    },
    () => undefined,
  );
  expect(items.map((i) => [i.kind, i.title])).toEqual([
    ["discrepancy", "C-1"],
    ["slot", "S-1（r）"],
  ]);
});

it("#806: 回答の行は問いの文を出し、保留がタスクに付いていればタスクの詳細を指す", async () => {
  await openAgents([alpha], "#/all", {
    "/api/v1/questions": {
      unreadable_workspaces: [],
      questions: [
        {
          challenge_id: "C-2",
          from_status: "in_progress",
          question: "価格は税込みですか",
          raised_at: "2026-10-17T00:00:00Z",
          run_id: null,
          task_id: "T-5",
          title: "答えを待つ課題",
          workspace: "alpha",
        },
      ],
    },
  });
  await waitFor(async () => expect(await rows()).toHaveLength(3));
  const r = (await rows())[2] as HTMLElement;
  expect(r).toHaveTextContent("価格は税込みですか");
  expect(within(r).getByRole("link").getAttribute("href")).toBe(
    "#/a/alpha/challenges/C-2/tasks/T-5",
  );
});
