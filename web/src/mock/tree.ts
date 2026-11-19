// 課題のツリーの API の偽の応答（エージェント alpha の課題の画面用）。区分・進み・費用・件数は、本物では core が決める値で、
// ここでは画面が出すだけであることを確かめられるよう、値を直に書く。開発時の mock モードだけが読み込む。

import type {
  TreeChallenge,
  TreeResponse,
  TreeTask,
} from "../api/types.gen.ts";

const base = (
  id: string,
  title: string,
  status: string,
  statusLabel: string,
  p: Partial<TreeChallenge>,
): TreeChallenge => ({
  budget_usd: null,
  children: [],
  cost_usd: 0,
  dependencies: [],
  followups_open: 0,
  id,
  next_human_actions: [],
  parent: null,
  status,
  status_label: statusLabel,
  tasks: [],
  tasks_done: 0,
  tasks_total: 0,
  title,
  updated_at: "2026-11-01T00:00:00.000Z",
  uses_integration: null,
  ...p,
});

const task = (
  key: string,
  title: string,
  phase: string,
  dependsOn: string[],
  id: string | null,
  cost = 0,
): TreeTask => ({
  cost_usd: cost,
  depends_on: dependsOn,
  id,
  key,
  phase,
  status: null,
  title,
});

// 列が画面の幅より多いツリー（横スクロールの試験用）。8 列の直列。
const chain: TreeTask[] = Array.from({ length: 8 }, (_, i) =>
  task(
    `s${i + 1}`,
    `手順 ${i + 1} を進める`,
    i < 3 ? "done" : i === 3 ? "running" : "blocked",
    i === 0 ? [] : [`s${i}`],
    `T-${40 + i}`,
  ),
);

export function alphaTree(): TreeResponse {
  const challenges: TreeChallenge[] = [
    base(
      "C-3",
      "通知メールの再送制御",
      "awaiting_plan_approval",
      "計画承認待ち",
      {
        budget_usd: 12,
        next_human_actions: ["approve_plan"],
        tasks_total: 4,
        tasks: [
          task("a", "誤りの種類を洗う", "next", [], null),
          task("b", "知らせ方を決める", "blocked", ["a"], null),
          task("c", "実装", "blocked", ["b"], null),
          task("d", "確かめ", "blocked", ["c"], null),
        ],
      },
    ),
    base("C-11", "決済画面の改修", "in_progress", "着手中", {
      budget_usd: 30,
      cost_usd: 11.2,
      dependencies: [
        {
          challenge_id: "C-5",
          satisfied: false,
          status: "in_progress",
          status_label: "着手中",
          title: "ビルドの高速化",
        },
        {
          challenge_id: "C-12",
          satisfied: true,
          status: "done",
          status_label: "完了",
          title: "古い設定の削除",
        },
      ],
      followups_open: 3,
      tasks_done: 2,
      tasks_total: 5,
      uses_integration: true,
      tasks: [
        task("spec", "仕様", "done", [], "T-1", 2.1),
        task("design", "設計", "done", ["spec"], "T-2", 3.4),
        task("input", "入力を受ける", "running", ["design"], "T-3", 4.0),
        task("keep", "同じ成果物で続ける", "waiting", ["design"], "T-4", 1.7),
        task(
          "join",
          "結合の確かめ",
          "blocked",
          ["input", "keep", "gone"],
          "T-5",
        ),
      ],
    }),
    base(
      "C-28",
      "請求データの夜間集計を新しい基盤へ載せ替える",
      "in_progress",
      "着手中",
      {
        budget_usd: 50,
        cost_usd: 18,
        next_human_actions: ["approve_completion"],
        tasks_done: 3,
        tasks_total: 8,
        uses_integration: false,
        tasks: chain,
      },
    ),
    base(
      "C-7",
      "料金表の計算を直す",
      "awaiting_completion_approval",
      "完了確認待ち",
      {
        cost_usd: 5.5,
        next_human_actions: ["approve_completion"],
        tasks_done: 1,
        tasks_total: 2,
        tasks: [
          task("calc", "計算を直す", "done", [], "T-20", 3),
          task("drop", "古い表の撤去", "cancelled", ["calc"], "T-21"),
        ],
      },
    ),
    base("C-20", "返金 API の上限の確認", "in_progress", "着手中", {}),
  ];
  const recent_done: TreeChallenge[] = [
    base("C-12", "古い設定の削除", "done", "完了", {
      budget_usd: 8,
      cost_usd: 6.3,
      followups_open: 2,
      tasks_done: 2,
      tasks_total: 2,
      tasks: [
        task("x", "設定の洗い出し", "done", [], "T-30", 3),
        task("y", "削除", "done", ["x"], "T-31", 3.3),
      ],
    }),
    base("C-22", "ログ出力の整理", "done", "完了", {
      cost_usd: 1,
      tasks_done: 1,
      tasks_total: 1,
      tasks: [task("z", "整理", "done", [], "T-32", 1)],
    }),
  ];
  return { challenges, recent_done };
}
