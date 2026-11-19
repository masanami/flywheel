// core が返す閉集合のコードを表示の文言に写す対応表。状態は UI で導かず、文言だけをここで持つ。

import { HOLD_KIND_CODES } from "./api/types.gen.ts";
import type { CycleWorkStages } from "./api/types.gen.ts";

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

// 保留の種類。値は core の閉集合から生成した HOLD_KIND_CODES（types.gen.ts）の要素で、文字列の直書きで比べない。
export const [HOLD_KIND_HUMAN, HOLD_KIND_PARENT_DIRECT] = HOLD_KIND_CODES;

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

export const NO_ACTION_LABEL = "なし（エージェントが進める）";
export const NO_PRIORITY_LABEL = "優先度なし";

// 対応の無いコードは生のコードをそのまま出す（欠けても画面は落とさない。対応の網羅はテストが守る）。
export function label<K extends string>(
  table: Record<K, string>,
  code: string,
): string {
  return (table as Record<string, string>)[code] ?? code;
}

// タスクの表示の区分（phase）。区分は core が決め、ここは語だけを持つ。
export const TASK_PHASES = [
  "running",
  "waiting",
  "next",
  "blocked",
  "done",
  "cancelled",
] as const;
export type TaskPhase = (typeof TASK_PHASES)[number];

export const PHASE_LABELS: Record<TaskPhase, string> = {
  running: "走っている",
  waiting: "あなたを待っている",
  next: "次に走る",
  blocked: "前提を待っている",
  done: "済み",
  cancelled: "取りやめ",
};

// サイクルの状態（サイクルの API の state）。値は core が決め、ここは上の帯に出す語だけを持つ。
export const CYCLE_STATES = ["running", "idle", "stale"] as const;
export type CycleState = (typeof CYCLE_STATES)[number];

export const CYCLE_STATE_LABELS: Record<CycleState, string> = {
  running: "サイクル: 走っている",
  idle: "サイクル: 走っていない",
  stale: "サイクル: 止まっているかもしれない",
};

// サイクルを回すと進むものの段（状況の API の cycle_work.stages のキー）。表示の順もこの並び。
// 段の件数は core が数え、ここは語と並びだけを持つ（Record の型で、全部のキーに語があることを守る）。
export const CYCLE_WORK_STAGES = [
  "release",
  "classify",
  "plan",
  "run",
  "verify",
  "learn",
  "improve",
  "cleanup",
] as const satisfies readonly (keyof CycleWorkStages)[];
export type CycleWorkStage = (typeof CYCLE_WORK_STAGES)[number];

export const CYCLE_WORK_STAGE_LABELS: Record<keyof CycleWorkStages, string> = {
  release: "反映",
  classify: "分類",
  plan: "計画",
  run: "委譲",
  verify: "完了の検証",
  learn: "学び",
  improve: "改修の提案",
  cleanup: "片付け",
};

// 課題の画面の凡例。区分の語は PHASE_LABELS と同じで、「次に走る」だけ括弧の補足を添える。
export const PHASE_LEGEND_ORDER = [
  "running",
  "waiting",
  "next",
  "blocked",
  "done",
] as const satisfies readonly TaskPhase[];

export const PHASE_LEGEND_LABELS: Record<
  (typeof PHASE_LEGEND_ORDER)[number],
  string
> = {
  running: PHASE_LABELS.running,
  waiting: PHASE_LABELS.waiting,
  next: "次に走る（前提は済み）",
  blocked: PHASE_LABELS.blocked,
  done: PHASE_LABELS.done,
};

export const LEGEND_EDGE_HINT = "線は前提です（左が終わると、右が走れます）。";

// 課題の詳細の語。値は core が決め、ここは文言だけを持つ。
export const FOLLOWUP_STATUS_LABELS: Record<string, string> = {
  open: "未",
  done: "済み",
  dropped: "捨てた",
};

// 取り込み元の状態（上流の状態）。対応の無い値は生のコードをそのまま出す。
export const UPSTREAM_STATE_LABELS: Record<string, string> = {
  open: "開いている",
  closed: "閉じている",
};

// フォローアップの出どころ。対応の無い値は生のコードをそのまま出す。
export const FOLLOWUP_SOURCE_LABELS: Record<string, string> = {
  learn: "学び",
};

export const NO_WAITING_TEXT = "あなたを待っているものはありません。";
// 種類が親が直接行うの保留が開いているときの表示（オーナーが答える問いではない）。
export const PARENT_DIRECT_HOLD_NOTE =
  "親のエージェントが直接行う保留（オーナーが答える問いではありません）";
