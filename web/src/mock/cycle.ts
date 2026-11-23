// サイクルの口の偽の応答。サイクルを止める指示（POST …/cycle/stop）と、サイクルの様子に足す値（running・supervisor_actions・costs）。
// 開発時の mock モードとブラウザの試験だけが使う。

import type { ApiClient } from "../api/client.ts";
import type { CycleScreen } from "../api/types.gen.ts";
import type { MockContext } from "./context.ts";

// 止める指示を出したエージェント（ブラウザ内だけの偽の状態）。
const stopRequested = new Set<string>();

const at = (minutesAgo: number) =>
  new Date(Date.now() - minutesAgo * 60_000).toISOString();

/** サイクルの様子（CycleStatus の欄）に足す、走っているサイクルの中身と、対象のサイクルのスーパーバイザーの手・費用の内訳。 */
export function cycleScreenExtras(
  ctx: MockContext,
  name: string,
): Pick<CycleScreen, "supervisor_actions" | "costs" | "running"> {
  if (name !== "alpha") {
    return { running: null, supervisor_actions: [], costs: [] };
  }
  const state = ctx.wsOf(name).cycle.state;
  return {
    running:
      state === "running"
        ? {
            active_runs: 2,
            budget_usd: 20,
            max_parallel_runs: 3,
            rounds: 4,
            spent_usd: 6.5,
            started_at: at(12),
            stop_requested: stopRequested.has(name),
            trigger: "ui",
            watching: [
              {
                challenge_id: "C-11",
                run_started_at: at(8),
                spent_usd: 2.25,
                state: "working",
                task_id: "T-1",
                title: "入力の名前をそろえる",
              },
              {
                challenge_id: "C-12",
                run_started_at: at(5),
                spent_usd: 1.1,
                state: "verifying",
                task_id: null,
                title: "長い説明の切り詰め方を決める",
              },
            ],
          }
        : null,
    supervisor_actions: [
      {
        at: at(3),
        challenge_id: "C-11",
        challenge_title: "差し戻しの後も同じ成果物で続ける",
        findings: "検証の結果は前回と同じで、条件を変えて再確認が要る。",
        hand: "reverify",
        id: 2,
        reject_reason: null,
        result: "executed",
        task_id: "T-1",
        task_title: "入力の名前をそろえる",
      },
      {
        at: at(6),
        challenge_id: "C-12",
        challenge_title: "長い説明の切り詰め方",
        findings: "あなたがすでに回答していた。",
        hand: "answer",
        id: 1,
        reject_reason: "owner_answered",
        result: "rejected",
        task_id: null,
        task_title: null,
      },
    ],
    costs: [
      { cost_usd: 1.5, kind: "judgment", role: "J2" },
      { cost_usd: 5, kind: "delegate", role: null },
    ],
  };
}

export function cycleMock(ctx: MockContext): Pick<ApiClient, "cycleStop"> {
  return {
    async cycleStop(name) {
      await ctx.wait();
      const running = ctx.wsOf(name).cycle.state === "running";
      if (running) stopRequested.add(name);
      return { cycle_id: running ? "Y-1" : null, requested: running, running };
    },
  };
}
