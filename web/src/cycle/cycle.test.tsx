import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  CYCLE_RESULTS,
  CYCLE_RESULT_LABELS,
  CYCLE_STATES,
  CYCLE_STATE_SHORT_LABELS,
  CYCLE_WORK_STAGES,
  CYCLE_WORK_STAGE_LABELS,
  cycleStateText,
} from "../labels.ts";
import { NO_CYCLE_WORK_TEXT } from "../shell/CycleWorkPart.tsx";
import { type Agent, openAgents } from "../testing/agents.tsx";
import { cycleResultText } from "./CycleScreen.tsx";

// サイクルの画面の受入基準を 1 つずつ直接確かめる。Issue #806。

const STARTED = "2026-10-17T00:50:00Z";
const ENDED = "2026-10-17T01:00:00Z";
const agent: Agent = {
  name: "alpha",
  cycle: {
    state: "idle",
    last_started_at: STARTED,
    last_ended_at: ENDED,
    last_result: "completed",
  },
  cycleWork: { total: 3, stages: { classify: 1, plan: 2 } },
};

beforeEach(() => {
  window.location.hash = "";
});
afterEach(() => vi.unstubAllGlobals());

const body = () => document.querySelector(".shell-body") as HTMLElement;

it("AC-376 #806: サイクルの画面に、見出し「サイクル」が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  expect(
    await screen.findByRole("heading", { name: "サイクル" }),
  ).toBeInTheDocument();
});

it("AC-377 #806: 最後のサイクルの始まった時刻が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  const el = await screen.findByTestId("cycle-last-started");
  expect(el).toHaveTextContent(new Date(STARTED).toLocaleString());
  expect(el.querySelector("time")?.getAttribute("datetime")).toBe(STARTED);
});

it("AC-378 #806: 最後のサイクルの終わった時刻が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  const el = await screen.findByTestId("cycle-last-ended");
  expect(el).toHaveTextContent(new Date(ENDED).toLocaleString());
});

it("AC-379 #806: 最後のサイクルの結果が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  expect(await screen.findByTestId("cycle-last-result")).toHaveTextContent(
    "最後まで終わった",
  );
});

it("#806: まだ走ったことがなければ、始まり・終わり・結果は「まだありません」", async () => {
  await openAgents(
    [{ name: "alpha", cycle: { state: "idle" } }],
    "#/a/alpha/cycle",
  );
  for (const id of [
    "cycle-last-started",
    "cycle-last-ended",
    "cycle-last-result",
  ]) {
    expect(await screen.findByTestId(id)).toHaveTextContent("まだありません");
  }
});

it("#806: 状態（実行中・走っていない・止まっているかもしれない）が出る", async () => {
  await openAgents(
    [{ name: "alpha", cycle: { state: "stale" } }],
    "#/a/alpha/cycle",
  );
  expect(await screen.findByTestId("cycle-screen-state")).toHaveTextContent(
    "止まっているかもしれない",
  );
});

it("AC-380 #806: サイクルを止めるボタンは無い（ボタンは「サイクルを回す」だけ）", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  await screen.findByTestId("cycle-work");
  const names = within(body())
    .queryAllByRole("button")
    .map((b) => b.textContent);
  expect(names).toEqual(["サイクルを回す"]);
  expect(within(body()).queryByText(/止める|止めて|停止/)).toBeNull();
});

it("AC-181 #995・AC-381 #806: サイクルを回すと進むものの合計と「サイクルを回す」のボタンが出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  const part = await screen.findByTestId("cycle-work");
  await waitFor(() =>
    expect(within(part).getByTestId("cycle-work-total")).toHaveTextContent("3"),
  );
  expect(
    within(part).getByRole("button", { name: "サイクルを回す" }),
  ).toBeEnabled();
  // 段ごとの件数
  expect(part.querySelector('[data-stage="plan"]')).toHaveTextContent("計画 2");
  // 0 件の段は出さない
  expect(part.querySelector('[data-stage="release"]')).toBeNull();
});

it("#806: 走っているときは「サイクルを回す」を押せない", async () => {
  await openAgents(
    [{ ...agent, cycle: { ...agent.cycle, state: "running" } }],
    "#/a/alpha/cycle",
  );
  const part = await screen.findByTestId("cycle-work");
  await waitFor(() =>
    expect(
      within(part).getByRole("button", { name: "サイクルを回す" }),
    ).toBeDisabled(),
  );
});

it("#806: サイクルの状態・結果・段の全部の値に文言がある", () => {
  for (const s of CYCLE_STATES) {
    expect(CYCLE_STATE_SHORT_LABELS[s]).toBeTruthy();
    expect(cycleStateText(s)).toBe(CYCLE_STATE_SHORT_LABELS[s]);
  }
  for (const r of CYCLE_RESULTS) {
    expect(cycleResultText(r)).toBe(CYCLE_RESULT_LABELS[r]);
  }
  expect(CYCLE_WORK_STAGES.map((s) => CYCLE_WORK_STAGE_LABELS[s])).toEqual([
    "反映",
    "分類",
    "計画",
    "委譲",
    "完了の検証",
    "学び",
    "改修の提案",
    "片付け",
    "スーパーバイザー待ち",
  ]);
});

it("#806: サイクルを回すと進むものの区分は、「いま進められること」と同じ部品で、同じ中身が出る", async () => {
  await openAgents([agent], "#/a/alpha/cycle");
  const onCycle = (await screen.findByTestId("cycle-work")).textContent;
  expect(onCycle).toContain("サイクルを回すと進むもの（3 件）");
  window.location.hash = "#/a/alpha/now";
  await screen.findByRole("heading", { name: "いま進められること" });
  expect((await screen.findByTestId("cycle-work")).textContent).toBe(onCycle);
});

it("#806: 進められるものが 0 件のとき、始めない旨が出て、「サイクルを回す」は押せない", async () => {
  await openAgents([{ ...agent, cycleWork: { total: 0 } }], "#/a/alpha/cycle");
  const part = await screen.findByTestId("cycle-work");
  await within(part).findByText(NO_CYCLE_WORK_TEXT);
  expect(
    within(part).getByRole("button", { name: "サイクルを回す" }),
  ).toBeDisabled();
});

it("#806: 止まっているかもしれないときは、ターミナルのコマンドの案内が出て、「サイクルを回す」は押せない", async () => {
  await openAgents(
    [{ ...agent, cycle: { ...agent.cycle, state: "stale" } }],
    "#/a/alpha/cycle",
  );
  const part = await screen.findByTestId("cycle-work");
  await within(part).findByText("flywheel cycle --workspace '/w/alpha'");
  expect(
    within(part).getByRole("button", { name: "サイクルを回す" }),
  ).toBeDisabled();
});

it("#798: エージェントを切り替えても、前のエージェントの「サイクルを始めました。」は残らない", async () => {
  await openAgents([agent, { ...agent, name: "beta" }], "#/a/alpha/cycle", {
    "POST /api/v1/workspaces/alpha/cycle": { started: true },
    "POST /api/v1/workspaces/beta/cycle": { started: true },
  });
  const part = await screen.findByTestId("cycle-work");
  await waitFor(() =>
    expect(
      within(part).getByRole("button", { name: "サイクルを回す" }),
    ).toBeEnabled(),
  );
  fireEvent.click(within(part).getByRole("button", { name: "サイクルを回す" }));
  expect(await screen.findByText("サイクルを始めました。")).toBeInTheDocument();
  act(() => {
    window.location.hash = "#/a/beta/cycle";
  });
  await waitFor(() =>
    expect(screen.getByTestId("cycle-work")).toBeInTheDocument(),
  );
  expect(screen.queryByText("サイクルを始めました。")).toBeNull();
});

it("#798: 止まっているかもしれないときの案内は、ターミナルで flywheel cycle を実行すると、止まったサイクルを片付けてから始める旨を伝える", async () => {
  await openAgents(
    [{ ...agent, cycle: { ...agent.cycle, state: "stale" } }],
    "#/a/alpha/cycle",
  );
  const part = await screen.findByTestId("cycle-work");
  const code = await within(part).findByText(
    "flywheel cycle --workspace '/w/alpha'",
  );
  const note = code.closest("p");
  expect(note).toHaveTextContent("止まったサイクルを片付けてから始めます");
  expect(note).toHaveTextContent("ターミナルで");
  expect(note).toHaveTextContent("この画面からは始められません");
});
