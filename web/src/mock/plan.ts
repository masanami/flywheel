import type { ApprovalPlan } from "../api/types.gen.ts";

// 計画の承認の画面の偽のデータ。タスクを持つ形の計画（キーは「エージェント/課題」）。
// ここに無い課題は、過去の形（本文だけ）の計画を返す。
export const RICH_PLANS: Record<string, Omit<ApprovalPlan, "version">> = {
  "alpha/C-3": {
    body: "1. 送信に冪等キーを足す\n2. キューの再試行で同じキーを使う",
    budget_usd: 12,
    summary:
      "再送のたびに**同じ通知が重ならない**ようにする。まず誤りの種類を洗い、知らせ方を決めてから実装する。",
    uses_integration: true,
    tasks: [
      {
        depends_on: [],
        key: "a",
        phase: "next",
        source: null,
        title: "誤りの種類を洗う",
      },
      {
        depends_on: ["a"],
        key: "b",
        phase: "blocked",
        source: null,
        title: "知らせ方を決める",
      },
      {
        depends_on: ["b"],
        key: "c",
        phase: "blocked",
        source: null,
        title: "実装",
      },
      {
        depends_on: ["c"],
        key: "d",
        phase: "blocked",
        source: null,
        title: "確かめ",
      },
    ],
    topics: [
      {
        title: "再送の間隔",
        decision: "間隔は **指数的に延ばす**。上限は 1 時間にする。",
        questions: [
          {
            number: 1,
            question: "再送の回数に上限を付けますか。",
            recommended: 1,
            options: [
              { label: "付ける（5 回まで）", example: null },
              { label: "付けない", example: null },
            ],
            answer: null,
          },
          {
            number: 2,
            question: "失敗が続いたとき、あなたに知らせますか。",
            recommended: 2,
            options: [
              { label: "毎回知らせる", example: null },
              { label: "3 回続いたら知らせる", example: null },
            ],
            answer: {
              choice: 2,
              label: "3 回続いたら知らせる",
              recommended: true,
              text: null,
            },
          },
        ],
      },
      {
        title: "重複の判定",
        decision: "通知の **冪等キー** で判定する。",
        questions: [],
      },
    ],
  },
};
