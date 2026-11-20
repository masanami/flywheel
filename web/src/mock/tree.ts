// 課題のツリーの API の偽の応答（エージェント alpha の課題の画面用）。区分・進み・費用・件数は、本物では core が決める値で、
// ここでは画面が出すだけであることを確かめられるよう、値を直に書く。開発時の mock モードだけが読み込む。

import type {
  Followup,
  SourceBinding,
  SourceChild,
  Task,
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
    i < 3 ? "done" : i === 3 ? "running" : i === 4 ? "waiting" : "blocked",
    i === 0 ? [] : [`s${i}`],
    `T-${40 + i}`,
    i < 5 ? 1.5 : 0,
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

// 課題の詳細（課題の詳細の API）で、alpha の課題に足す値。C-28 は、取り込み元の子・フォローアップ・
// 前提にされている課題を持つ（幅 360px の画面で折り返しや横の溢れを確かめるため、長い題名・長い URL も含む）。
type Extras = {
  dependents?: string[];
  followups?: Followup[];
  source_binding?: SourceBinding | null;
  source_children?: SourceChild[];
  tasks?: Task[];
};

const followup = (id: string, status: string, title: string): Followup => ({
  challenge_id: "C-28",
  created_at: "2026-11-01T00:00:00.000Z",
  detail: "",
  id,
  note: null,
  promoted_challenge_id: null,
  reason: null,
  source: "learn",
  status,
  title,
  updated_at: "2026-11-01T00:00:00.000Z",
});

export function alphaDetailExtras(id: string): Extras {
  if (id !== "C-28") return {};
  return {
    dependents: ["C-20"],
    followups: [
      followup("F-1", "open", "集計の遅い夜のログを整理する"),
      followup("F-2", "open", "旧基盤の撤去の手順を書き出す"),
      followup("F-3", "done", "テーブル名の揺れを直す"),
    ],
    source_binding: {
      comments_count: 0,
      created_at: "2026-10-01T00:00:00.000Z",
      external_key: "example/billing#28",
      fingerprint: "x",
      policy_state: "in_policy",
      read_comments_count: 0,
      read_upstream_updated_at: null,
      source_id: "example",
      updated_at: "2026-10-01T00:00:00.000Z",
      upstream_state: "open",
      upstream_updated_at: null,
      url: "https://example.com/billing/issues/28",
    },
    source_children: [
      {
        external_key: "example/billing#29",
        parent_external_key: "example/billing#28",
        title: "集計クエリを新しい基盤向けに書き直す",
        upstream_state: "open",
        url: "https://example.com/billing/issues/29/with/a/very/long/path/that/should/wrap/instead/of/overflowing/the/page",
      },
      {
        external_key: "example/billing#30",
        parent_external_key: "example/billing#28",
        title: "旧基盤の夜間ジョブを止める",
        upstream_state: "closed",
        url: "https://example.com/billing/issues/30",
      },
    ],
    tasks: Array.from({ length: 8 }, (_, i): Task => {
      const tid = `T-${40 + i}`;
      return {
        budget_impl_usd: 0,
        budget_review_usd: 0,
        body:
          i === 3
            ? "旧基盤の集計クエリを、新基盤の表へ書き出す形に直す。\n\n- 書き出し先の表を決める\n- 列の名前を旧基盤とそろえる"
            : `手順 ${i + 1} の作業。`,
        depends_on: i === 0 ? [] : [`T-${39 + i}`],
        done_criteria:
          i === 3
            ? "- 件数が旧基盤と一致する\n- 集計が 3 日続けて成功する"
            : "- 手順が済んでいる",
        id: tid,
        non_goals:
          i === 3 ? ["旧基盤のジョブは止めない", "画面は変えない"] : [],
        operation: "",
        plan_version: 1,
        repo: "",
        source_key: i === 0 ? "example/billing#29" : null,
        spent_usd: 0,
        status: i < 3 ? "done" : i < 5 ? "in_progress" : "pending",
        title: `手順 ${i + 1} を進める`,
      };
    }),
  };
}
