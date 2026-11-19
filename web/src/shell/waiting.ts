import type { StatusNeedsHuman } from "../api/types.gen.ts";

// あなたを待っているものの種類。表示の順もこの並び。後の画面（いま進められること・すべて）が共通に使う。
export const WAITING_KINDS = [
  "plan_approval",
  "completion_approval",
  "answer",
  "operation",
  "discrepancy",
  "budget",
  "triage",
  "slot",
] as const;
export type WaitingKind = (typeof WAITING_KINDS)[number];

export const WAITING_LABELS: Record<WaitingKind, string> = {
  plan_approval: "計画の承認",
  completion_approval: "完了の承認",
  answer: "回答",
  operation: "不可逆操作の承認",
  discrepancy: "食い違い",
  budget: "予算の増額",
  triage: "トリアージ",
  slot: "スロットの片付け",
};

export type WaitingCounts = Record<WaitingKind, number>;

// 件数の数え方は、見直し前の人間待ちの帯の区分と同じ。承認待ちと回答は課題の状態で分け、
// 食い違いは同じ課題に要素が複数あっても 1 件（循環だけの要素は数えない）、ほかは要素の数。
export function waitingCounts(nh: StatusNeedsHuman): WaitingCounts {
  const inState = (s: string) =>
    nh.challenges.filter((c) => c.status === s).length;
  return {
    plan_approval: inState("awaiting_plan_approval"),
    completion_approval: inState("awaiting_completion_approval"),
    answer: inState("awaiting_human"),
    operation: nh.operations.length,
    discrepancy: new Set(
      nh.discrepancies
        .filter((d) => d.cycle_only !== true)
        .map((d) => d.challenge_id),
    ).size,
    budget: nh.budget_exhausted.length,
    triage: nh.triage.length,
    slot: nh.slots.length,
  };
}

export function waitingTotal(c: WaitingCounts): number {
  return WAITING_KINDS.reduce((a, k) => a + c[k], 0);
}
