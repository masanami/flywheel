// core が返す閉集合のコードを表示の文言に写す対応表。状態は UI で導かず、文言だけをここで持つ。

export const CARD_STATES = [
  "unclassified",
  "classified",
  "awaiting_plan_approval",
  "in_progress",
  "verifying",
  "awaiting_completion_approval",
  "done",
  "awaiting_human",
  "withdrawn",
] as const;
export type CardState = (typeof CARD_STATES)[number];

export const CARD_MODIFIERS = [
  "running",
  "unresponsive",
  "waiting_ci",
  "budget_exhausted",
  "discrepancy",
] as const;
export type CardModifier = (typeof CARD_MODIFIERS)[number];

export const CARD_ACTIONS = [
  "approve_plan",
  "approve_completion",
  "answer_question",
  "triage",
  "increase_budget",
  "approve_operation",
  "review_discrepancy",
  "clear_slot",
] as const;
export type CardAction = (typeof CARD_ACTIONS)[number];

export const DISCREPANCY_KINDS = [
  "upstream_closed",
  "upstream_missing",
  "out_of_policy",
  "upstream_commented",
  "upstream_updated",
  "merged_unapproved",
  "dependency_cycle",
] as const;
export type DiscrepancyKind = (typeof DISCREPANCY_KINDS)[number];

export const STATE_LABELS: Record<CardState, string> = {
  unclassified: "未分類",
  classified: "分類済",
  awaiting_plan_approval: "計画承認待ち",
  in_progress: "着手中",
  verifying: "検証中",
  awaiting_completion_approval: "完了確認待ち",
  done: "完了",
  awaiting_human: "人間対応待ち",
  withdrawn: "取り下げ",
};

export const MODIFIER_LABELS: Record<CardModifier, string> = {
  running: "実行中",
  unresponsive: "更新なし（実行中だが一定時間更新がない）",
  waiting_ci: "自動の確認待ち",
  budget_exhausted: "予算を使い切った",
  discrepancy: "食い違い",
};

export const ACTION_LABELS: Record<CardAction, string> = {
  approve_plan: "計画を承認する",
  approve_completion: "完了を確認する",
  answer_question: "問いに答える",
  triage: "どう進めるか判断する（トリアージ）",
  increase_budget: "予算を追加する",
  approve_operation: "不可逆操作を承認する",
  review_discrepancy: "食い違いを確認する",
  clear_slot: "スロットを片づける",
};

export const DISCREPANCY_LABELS: Record<DiscrepancyKind, string> = {
  upstream_closed: "上流で閉じられた",
  upstream_missing: "上流に見つからない",
  out_of_policy: "取り込みの方針の範囲外",
  upstream_commented: "上流に新しいコメント",
  upstream_updated: "上流が更新された",
  merged_unapproved: "承認なしでマージされた",
  dependency_cycle: "依存が循環している",
};

// カードの面に出す区分の印（M4H23）。API の値から UI が導く（board/marks.ts）。
export const MARK_KINDS = [
  "needs_human",
  "external",
  "actionable",
  "running",
  "unresponsive",
  "approved_operation",
] as const;
export type MarkKind = (typeof MARK_KINDS)[number];

export const MARK_LABELS: Record<MarkKind, string> = {
  needs_human: "人間の操作待ち",
  external: "外部待ち",
  actionable: "システムが次に進める",
  running: "実行中",
  unresponsive: "更新なし",
  approved_operation: "承認済みの操作が未実行",
};

// 依存先の充足の表示。値は API の satisfied をそのまま写す（UI で導かない）。
export const DEPENDENCY_SATISFIED_LABEL = "満たされている";
export const DEPENDENCY_WAITING_LABEL = "待っている";

export const NO_ACTION_LABEL = "なし（エージェントが進める）";
export const NO_PRIORITY_LABEL = "優先度なし";

// 対応の無いコードは生のコードをそのまま出す（欠けても画面は落とさない。対応の網羅はテストが守る）。
export function label<K extends string>(
  table: Record<K, string>,
  code: string,
): string {
  return (table as Record<string, string>)[code] ?? code;
}
