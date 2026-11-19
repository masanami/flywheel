import { readFileSync } from "node:fs";
import { join } from "node:path";
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
import type {
  TreeChallenge,
  TreeDependency,
  TreeTask,
} from "../api/types.gen.ts";
import { PHASE_LABELS, TASK_PHASES } from "../labels.ts";
import { POLL_MS } from "../live/useLive.ts";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";

// 課題の画面（課題の枠・タスクのツリー・凡例・読み直し）の受入基準を 1 つずつ直接確かめる。Issue #807。

const TREE_URL = "/api/v1/workspaces/alpha/tree";

const task = (key: string, p: Partial<TreeTask> = {}): TreeTask => ({
  cost_usd: 0,
  depends_on: [],
  id: `T-${key}`,
  key,
  phase: "next",
  status: null,
  title: `タスク ${key}`,
  ...p,
});

const challenge = (
  id: string,
  p: Partial<TreeChallenge> = {},
): TreeChallenge => ({
  budget_usd: null,
  children: [],
  cost_usd: 0,
  dependencies: [],
  followups_open: 0,
  id,
  next_human_actions: [],
  parent: null,
  status: "in_progress",
  status_label: "着手中",
  tasks: [],
  tasks_done: 0,
  tasks_total: 0,
  title: `課題 ${id}`,
  updated_at: "2026-11-01T00:00:00Z",
  uses_integration: null,
  ...p,
});

interface Tree {
  challenges: TreeChallenge[];
  recent_done?: TreeChallenge[];
}

let requests: ReturnType<typeof installFakes>["requests"];

async function open(tree: Tree) {
  window.location.hash = "#/a/alpha/challenges";
  const f = installFakes({
    ...fixtures(),
    [TREE_URL]: { recent_done: [], ...tree },
    "/api/v1/workspaces/beta/tree": { challenges: [], recent_done: [] },
  });
  requests = f.requests;
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  await screen.findByRole("heading", { name: "課題" });
}

const frames = () => screen.queryAllByTestId("challenge-frame");
const treeCalls = () =>
  requests.filter((r) => r.url === TREE_URL && r.method === "GET").length;

beforeEach(() => {
  window.location.hash = "";
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("AC-179 #807: 課題の画面に、見出し「課題」が出る", async () => {
  await open({ challenges: [] });
  expect(screen.getByRole("heading", { name: "課題" })).toBeInTheDocument();
});

it("AC-180 #807: 開いている課題ごとの枠が、API の challenges の順に出る", async () => {
  await open({
    challenges: [challenge("C-9"), challenge("C-2"), challenge("C-5")],
  });
  await waitFor(() => expect(frames()).toHaveLength(3));
  expect(
    frames().map((f) => within(f).getAllByRole("link")[0]?.textContent),
  ).toEqual(["課題 C-9", "課題 C-2", "課題 C-5"]);
});

it("AC-181 #807: 枠の題名は、その課題の詳細へのリンクである", async () => {
  await open({ challenges: [challenge("C-9")] });
  const link = await screen.findByRole("link", { name: "課題 C-9" });
  expect(link).toHaveAttribute("href", "#/a/alpha/challenges/C-9");
});

it("AC-182 #807: 枠に、その課題の status_label が出る", async () => {
  await open({
    challenges: [challenge("C-1", { status_label: "検証の途中" })],
  });
  const f = (await screen.findAllByTestId("challenge-frame"))[0] as HTMLElement;
  expect(within(f).getByText("検証の途中")).toBeInTheDocument();
});

it("AC-183 #807: tasks が 3 件の課題の枠の中に、タスクの箱が 3 つ出る", async () => {
  await open({
    challenges: [
      challenge("C-1", { tasks: [task("a"), task("b"), task("c")] }),
    ],
  });
  const f = (await screen.findAllByTestId("challenge-frame"))[0] as HTMLElement;
  expect(within(f).getAllByTestId("task-box")).toHaveLength(3);
});

it("AC-184 #807: approve_plan がある課題の枠に「計画の承認」の語が出る", async () => {
  await open({
    challenges: [challenge("C-1", { next_human_actions: ["approve_plan"] })],
  });
  const f = (await screen.findAllByTestId("challenge-frame"))[0] as HTMLElement;
  expect(within(f).getByText("計画の承認")).toBeInTheDocument();
});

it("AC-185 #807: approve_plan がある課題の枠に、計画の承認の画面へのリンクが出る", async () => {
  await open({
    challenges: [challenge("C-1", { next_human_actions: ["approve_plan"] })],
  });
  const f = (await screen.findAllByTestId("challenge-frame"))[0] as HTMLElement;
  expect(
    within(f).getByRole("link", { name: "計画の承認の画面へ" }),
  ).toHaveAttribute("href", "#/a/alpha/challenges/C-1/approve-plan");
});

it("AC-186 #807: approve_completion がある課題の枠に「完了の承認」の語が出る", async () => {
  await open({
    challenges: [
      challenge("C-1", { next_human_actions: ["approve_completion"] }),
    ],
  });
  const f = (await screen.findAllByTestId("challenge-frame"))[0] as HTMLElement;
  expect(within(f).getByText("完了の承認")).toBeInTheDocument();
  expect(within(f).queryByText("計画の承認")).toBeNull();
});

it("AC-187 #807: tasks_done が 2、tasks_total が 5 の枠に、進みの 2 と 5 が出る", async () => {
  await open({
    challenges: [challenge("C-1", { tasks_done: 2, tasks_total: 5 })],
  });
  expect(await screen.findByTestId("tasks-done")).toHaveTextContent("2");
  expect(screen.getByTestId("tasks-total")).toHaveTextContent("5");
});

it("AC-188 #807: 枠に、その課題の cost_usd が出る", async () => {
  await open({ challenges: [challenge("C-1", { cost_usd: 11.2 })] });
  expect(await screen.findByTestId("cost")).toHaveTextContent("11.2");
});

it("AC-189 #807: budget_usd が 30 の枠に、予算の上限の 30 が出る", async () => {
  await open({ challenges: [challenge("C-1", { budget_usd: 30 })] });
  expect(await screen.findByTestId("budget")).toHaveTextContent("30");
});

it("AC-190 #807: budget_usd が null の枠に、予算の上限は出ない", async () => {
  await open({ challenges: [challenge("C-1", { budget_usd: null })] });
  await screen.findByTestId("cost");
  expect(screen.queryByTestId("budget")).toBeNull();
});

it("AC-191 #807: 枠に、その課題の followups_open が出る", async () => {
  await open({ challenges: [challenge("C-1", { followups_open: 7 })] });
  expect(await screen.findByTestId("followups-open")).toHaveTextContent("7");
});

it("AC-192 #807: uses_integration が真の枠に、統合先の「使う」が出る", async () => {
  await open({ challenges: [challenge("C-1", { uses_integration: true })] });
  expect(await screen.findByTestId("integration")).toHaveTextContent("使う");
});

it("AC-193 #807: uses_integration が null の枠に、統合先は出ない", async () => {
  await open({ challenges: [challenge("C-1", { uses_integration: null })] });
  await screen.findByTestId("cost");
  expect(screen.queryByTestId("integration")).toBeNull();
  expect(screen.queryByText(/統合先/)).toBeNull();
});

const dep = (id: string, satisfied: boolean): TreeDependency => ({
  challenge_id: id,
  satisfied,
  status: satisfied ? "done" : "in_progress",
  status_label: satisfied ? "完了だよ" : "進行だよ",
  title: `前提 ${id}`,
});

it("AC-194 #807: dependencies がある枠に「前提:」と前提の課題の題名が出る", async () => {
  await open({
    challenges: [challenge("C-1", { dependencies: [dep("C-7", true)] })],
  });
  const d = await screen.findByTestId("dependencies");
  expect(d).toHaveTextContent("前提:");
  expect(within(d).getByText("前提 C-7")).toBeInTheDocument();
});

it("AC-195 #807: dependencies がある枠に前提の課題の status_label が出る", async () => {
  await open({
    challenges: [challenge("C-1", { dependencies: [dep("C-7", false)] })],
  });
  const d = await screen.findByTestId("dependencies");
  expect(within(d).getByText("進行だよ")).toBeInTheDocument();
});

it("AC-196 #807: satisfied が偽の前提には「未完了」が添えて出る", async () => {
  await open({
    challenges: [challenge("C-1", { dependencies: [dep("C-7", false)] })],
  });
  const d = await screen.findByTestId("dependencies");
  expect(within(d).getByText("未完了")).toBeInTheDocument();
});

it("AC-197 #807: satisfied が真の前提には「未完了」は出ない", async () => {
  await open({
    challenges: [challenge("C-1", { dependencies: [dep("C-7", true)] })],
  });
  const d = await screen.findByTestId("dependencies");
  expect(within(d).queryByText("未完了")).toBeNull();
});

it("AC-198 #807: tasks が空の課題の枠に「タスクはまだありません。」が出る", async () => {
  await open({ challenges: [challenge("C-1", { tasks: [] })] });
  expect(
    await screen.findByText("タスクはまだありません。"),
  ).toBeInTheDocument();
});

it("AC-199 #807: challenges が空のとき「開いている課題はありません。」と「いま進められること」へのリンクが出る", async () => {
  await open({ challenges: [] });
  expect(
    await screen.findByText(/開いている課題はありません。/),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("link", { name: "「いま進められること」へ" }),
  ).toHaveAttribute("href", "#/a/alpha/now");
});

it("AC-200 #807: recent_done が 2 件のとき「完了した課題を開く（最近の 2 件）」が出る", async () => {
  await open({
    challenges: [],
    recent_done: [challenge("C-8"), challenge("C-9")],
  });
  expect(
    await screen.findByRole("button", {
      name: "完了した課題を開く（最近の 2 件）",
    }),
  ).toBeInTheDocument();
});

it("AC-201 #807: 「完了した課題を開く」を押すまで、完了した課題の枠は出ない", async () => {
  await open({ challenges: [], recent_done: [challenge("C-8")] });
  await screen.findByRole("button", { name: /完了した課題を開く/ });
  expect(frames()).toHaveLength(0);
  expect(screen.queryByText("課題 C-8")).toBeNull();
});

it("AC-202 #807: 「完了した課題を開く」を押すと、完了した課題の枠が出る", async () => {
  await open({ challenges: [], recent_done: [challenge("C-8")] });
  fireEvent.click(
    await screen.findByRole("button", { name: /完了した課題を開く/ }),
  );
  expect(await screen.findByText("課題 C-8")).toBeInTheDocument();
  expect(frames()).toHaveLength(1);
});

it("AC-203 #807: recent_done が空のとき「完了した課題を開く」は出ない", async () => {
  await open({ challenges: [challenge("C-1")], recent_done: [] });
  await screen.findByText("課題 C-1");
  expect(screen.queryByText(/完了した課題を開く/)).toBeNull();
});

it("AC-204 #807: 凡例の 5 つの語と、線の説明が出る", async () => {
  await open({ challenges: [] });
  const legend = await screen.findByTestId("legend");
  for (const w of [
    "走っている",
    "あなたを待っている",
    "次に走る（前提は済み）",
    "前提を待っている",
    "済み",
    "線は前提です（左が終わると、右が走れます）。",
  ]) {
    expect(within(legend).getByText(w)).toBeInTheDocument();
  }
});

// ---- タスクのツリー ----

const boxOf = (title: string) =>
  screen.getByText(title).closest("[data-testid=task-box]") as HTMLElement;

async function openTasks(tasks: TreeTask[]) {
  await open({ challenges: [challenge("C-1", { tasks })] });
  await screen.findAllByTestId("task-box");
}

it("AC-205 #807: 前提の無いタスクの箱は、1 列目に出る", async () => {
  await openTasks([task("a"), task("b")]);
  expect(boxOf("タスク a")).toHaveAttribute("data-col", "1");
  expect(boxOf("タスク b")).toHaveAttribute("data-col", "1");
});

it("AC-206 #807: A が B の前提、B が C の前提のとき、C の箱は 3 列目に出る", async () => {
  await openTasks([
    task("c", { depends_on: ["b"] }),
    task("b", { depends_on: ["a"] }),
    task("a"),
  ]);
  expect(boxOf("タスク a")).toHaveAttribute("data-col", "1");
  expect(boxOf("タスク b")).toHaveAttribute("data-col", "2");
  expect(boxOf("タスク c")).toHaveAttribute("data-col", "3");
});

it("AC-207 #807: 1 列目と 2 列目の両方を前提に持つタスクの箱は、3 列目に出る", async () => {
  await openTasks([
    task("a"),
    task("b", { depends_on: ["a"] }),
    task("c", { depends_on: ["a", "b"] }),
  ]);
  expect(boxOf("タスク c")).toHaveAttribute("data-col", "3");
});

it("AC-208 #807: ツリーの線の数は、depends_on の要素の数の合計と同じである", async () => {
  await openTasks([
    task("a"),
    task("b", { depends_on: ["a"] }),
    task("c", { depends_on: ["a", "b"] }),
    task("d", { depends_on: ["c"] }),
  ]);
  expect(
    screen.getByTestId("task-tree").querySelectorAll("path[data-edge]"),
  ).toHaveLength(4);
});

it("AC-209 #807: 箱に、タスクの題名が出る", async () => {
  await openTasks([task("a", { title: "誤りを洗う" })]);
  expect(boxOf("誤りを洗う")).toBeInTheDocument();
});

for (const p of TASK_PHASES) {
  const ac = {
    running: 210,
    waiting: 211,
    next: 212,
    blocked: 213,
    done: 214,
    cancelled: 215,
  }[p];
  it(`AC-${ac} #807: phase が ${p} の箱に、「${PHASE_LABELS[p]}」が出る`, async () => {
    await openTasks([task("a", { phase: p })]);
    expect(
      within(boxOf("タスク a")).getByText(PHASE_LABELS[p]),
    ).toBeInTheDocument();
  });
}

it("AC-216 #807: 箱は、属性 data-phase に、そのタスクの phase の値を持つ", async () => {
  await openTasks(
    TASK_PHASES.map((p) => task(p, { phase: p, title: `箱 ${p}` })),
  );
  for (const p of TASK_PHASES) {
    expect(boxOf(`箱 ${p}`)).toHaveAttribute("data-phase", p);
  }
});

// 見た目の定義（styles.css）を読み、6 つの値の背景の色が配色ごとに互いに異なることと、箱がその色を使うことを確かめる。
it("AC-217 #807: 6 つの区分の箱の背景の色は、明るい配色でも暗い配色でも互いに異なる", () => {
  const css = readFileSync(
    join(import.meta.dirname, "..", "styles.css"),
    "utf8",
  );
  const darkAt = css.indexOf("@media (prefers-color-scheme: dark)");
  expect(darkAt).toBeGreaterThan(0);
  const colors = (src: string) =>
    TASK_PHASES.map((p) => {
      const m = src.match(new RegExp(`--phase-${p}-bg:\\s*([^;]+);`));
      expect(m, `--phase-${p}-bg`).not.toBeNull();
      return (m?.[1] ?? "").trim().toLowerCase();
    });
  for (const src of [css.slice(0, darkAt), css.slice(darkAt)]) {
    const c = colors(src);
    expect(new Set(c).size).toBe(TASK_PHASES.length);
  }
  for (const p of TASK_PHASES) {
    expect(css).toMatch(
      new RegExp(
        `\\.task-box\\[data-phase="${p}"\\]\\s*\\{[^}]*background:\\s*var\\(--phase-${p}-bg\\)`,
      ),
    );
  }
});

it("AC-218 #807: depends_on に tasks に無い key があるタスクは、その前提が無いものとして列が決まり、線も引かない", async () => {
  await openTasks([
    task("a", { depends_on: ["nowhere"] }),
    task("b", { depends_on: ["a", "ghost"] }),
  ]);
  expect(boxOf("タスク a")).toHaveAttribute("data-col", "1");
  expect(boxOf("タスク b")).toHaveAttribute("data-col", "2");
  expect(
    screen.getByTestId("task-tree").querySelectorAll("path[data-edge]"),
  ).toHaveLength(1);
});

it("AC-219 #807: id があるタスクの箱は、そのタスクの詳細へのリンクである", async () => {
  await openTasks([task("a", { id: "T-77" })]);
  const box = boxOf("タスク a");
  expect(box.tagName).toBe("A");
  expect(box).toHaveAttribute("href", "#/a/alpha/challenges/C-1/tasks/T-77");
});

it("AC-220 #807: id が null のタスクの箱は、リンクでない", async () => {
  await openTasks([task("a", { id: null })]);
  const box = boxOf("タスク a");
  expect(box.tagName).not.toBe("A");
  expect(box.hasAttribute("href")).toBe(false);
});

// ---- 読み直し ----

it("AC-111 #807: そのエージェントの変化の合図を受けると、課題のツリーの API がもう 1 回呼ばれる", async () => {
  await open({ challenges: [challenge("C-1")] });
  await waitFor(() => expect(treeCalls()).toBe(1));
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(treeCalls()).toBe(2));
});

it("AC-112 #807: ほかのエージェントの変化の合図を受けても、課題のツリーの API は呼ばれない", async () => {
  await open({ challenges: [challenge("C-1")] });
  await waitFor(() => expect(treeCalls()).toBe(1));
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "beta",
    }),
  );
  await waitFor(() =>
    expect(
      requests.some((r) => r.url === "/api/v1/workspaces/beta/status"),
    ).toBe(true),
  );
  await act(async () => {
    await new Promise((r) => setTimeout(r, 50));
  });
  expect(treeCalls()).toBe(1);
});

it("AC-114 #807: 課題の画面を開いたまま 60 秒たつと、課題のツリーの API がもう 1 回呼ばれる", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  await open({ challenges: [challenge("C-1")] });
  await waitFor(() => expect(treeCalls()).toBe(1));
  act(() => {
    vi.advanceTimersByTime(POLL_MS);
  });
  await waitFor(() => expect(treeCalls()).toBe(2));
});

it("#807: 課題の画面を開く前は、課題のツリーの API を読まない", async () => {
  window.location.hash = "#/a/alpha/now";
  const f = installFakes({
    ...fixtures(),
    [TREE_URL]: { challenges: [], recent_done: [] },
  });
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  await screen.findByRole("heading", { name: "いま進められること" });
  expect(f.requests.some((r) => r.url === TREE_URL)).toBe(false);
});

it("#807: depends_on が循環していても、ツリーは崩れず箱が出る", async () => {
  await openTasks([
    task("a", { depends_on: ["b"] }),
    task("b", { depends_on: ["a"] }),
  ]);
  const canvas = screen.getByTestId("task-tree")
    .firstElementChild as HTMLElement;
  expect(canvas.style.height).not.toBe("");
  expect(Number.parseFloat(canvas.style.height)).toBeGreaterThan(0);
  expect(screen.getAllByTestId("task-box")).toHaveLength(2);
});

it("#807: approve_plan 以外の待ちの種類も、「いま進められること」と同じ語で枠に出る", async () => {
  await open({
    challenges: [
      challenge("C-1", {
        next_human_actions: ["answer_question", "increase_budget"],
      }),
    ],
  });
  const f = (await screen.findAllByTestId("challenge-frame"))[0] as HTMLElement;
  expect(within(f).getByText("回答")).toBeInTheDocument();
  expect(within(f).getByText("予算の増額")).toBeInTheDocument();
});
