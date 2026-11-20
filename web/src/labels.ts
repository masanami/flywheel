// core が返す閉集合のコードを表示の文言に写す対応表。状態は UI で導かず、文言だけをここで持つ。

import {
  EXECUTION_RESULT_CODES,
  EXECUTION_RESULT_WORDS,
  HOLD_KIND_CODES,
} from "./api/types.gen.ts";
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
  merged_unapproved: "承認なしで反映された",
  dependency_cycle: "依存が循環している",
};

type ExecutionResultCode = (typeof EXECUTION_RESULT_CODES)[number];

// 反映の試行の結果（Operation.last_execution_result。core の閉集合 EXECUTION_RESULT_CODES）の語。
// 語の正本は Go の表（internal/view の ExecutionResultLabel）で、生成物の EXECUTION_RESULT_WORDS を読む
// （添字は EXECUTION_RESULT_CODES と同じ）。ここに語を直書きしない。
export const EXECUTION_RESULT_LABELS = Object.fromEntries(
  EXECUTION_RESULT_CODES.map((code, i) => [code, EXECUTION_RESULT_WORDS[i]]),
) as Record<ExecutionResultCode, string>;

// カードの面に出す区分の印（M4H23）。API の値から UI が導く（marks.ts は S2c の仕上げで外した。値の表だけ残してある）。
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

// 前提の課題の充足の表示。値は API の satisfied をそのまま写す（画面では導かない）。
export const DEPENDENCY_SATISFIED_LABEL = "満たされている";
export const DEPENDENCY_UNMET_LABEL = "未完了";

export const NO_ACTION_LABEL = "なし（エージェントが進める）";
export const NO_PRIORITY_LABEL = "優先度なし";

// 対応の無いコードは生のコードをそのまま出す（欠けても画面は落とさない。対応の網羅はテストが守る）。
export function label<K extends string>(
  table: Record<K, string>,
  code: string,
): string {
  // 継承されたキー（constructor・toString など）は表の値ではないので、自前のキーだけを引く。
  const v = Object.hasOwn(table, code)
    ? (table as Record<string, string>)[code]
    : undefined;
  return v ?? code;
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

// 読み込みの最中に出す文（枠と各画面で同じ）。
export const LOADING_TEXT = "読み込んでいます";

// 書き込みの口が 403・forbidden_origin で拒否されたときの案内（計画の承認と「サイクルを回す」で同じ文）。
export const RELOAD_TEXT =
  "操作を受け付けられませんでした。ページを再読み込みしてからやり直してください（自動では再送しません）。";

// サイクルの画面・「すべて」の区分に出す、状態だけの語（上の帯の語から「サイクル: 」を除いたもの）。
export const CYCLE_STATE_SHORT_LABELS: Record<CycleState, string> = {
  running: "走っている",
  idle: "走っていない",
  stale: "止まっているかもしれない",
};

export function cycleStateText(state: string): string {
  return (CYCLE_STATES as readonly string[]).includes(state)
    ? CYCLE_STATE_SHORT_LABELS[state as CycleState]
    : state;
}

// 最後のサイクルの結果（サイクルの API の last_result。core の閉集合）。
export const CYCLE_RESULTS = ["completed", "aborted", "interrupted"] as const;
export type CycleResult = (typeof CYCLE_RESULTS)[number];

export const CYCLE_RESULT_LABELS: Record<CycleResult, string> = {
  completed: "最後まで終わった",
  aborted: "途中でやめた",
  interrupted: "途中で止まった",
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

// 種類が親が直接行うの保留が開いているときの表示（オーナーが答える問いではない）。
export const PARENT_DIRECT_HOLD_NOTE =
  "親のエージェントが直接行う保留（オーナーが答える問いではありません）";

// 経緯の「何が起きたか」。画面が知っている作業ログの action の値ごとの文言。知らない値は値のまま出す。
export const HISTORY_ACTION_LABELS: Record<string, string> = {
  plan: "計画を作った",
  approve: "承認した",
  "operation:approve": "不可逆操作を承認した",
  "operation:reject": "不可逆操作を差し戻した",
  approve_hold_release: "完了を承認した（反映は保留のまま）",
  reject: "差し戻した",
  hold: "保留にして、問いを上げた",
  answer: "問いに答えた",
  answer_plan: "計画の問いに答えた",
  submit: "完了の検証へ出した",
  verify_met: "完了条件を満たしていると確かめた",
  verify_not_met: "完了条件を満たしていないと確かめた",
  verify_uncertain: "完了条件を満たしているか判断できなかった",
  create: "課題を作った",
  "task:create": "タスクを足した",
  "followup:create": "フォローアップを足した",
  task_status: "タスクの状態が変わった",
  task_verify: "タスクを検証した",
  classify: "分類した",
  withdraw: "取り下げた",
  dep_add: "前提を足した",
  dep_rm: "前提を外した",
  parent_set: "親を付けた",
  parent_clear: "親を外した",
  learn: "学びを記録した",
  followup: "フォローアップを足した",
  followup_done: "フォローアップを済みにした",
  followup_drop: "フォローアップを捨てた",
  followup_promote: "フォローアップを課題に上げた",
  op_execute: "不可逆操作を実行した",
  op_close: "不可逆操作を閉じた",
  op_add: "不可逆操作を足した",
  op_approve: "不可逆操作を承認した",
  op_reject: "不可逆操作を差し戻した",
  complete_direct: "オーケストレーターが直接行った作業を、完了として記録した",
  reopen_for_release: "反映をやり直すため、完了から戻した",
  ingest_create: "取り込み元から課題を作った",
  ingest_update: "取り込み元の変更を取り込んだ",
  bind: "取り込み元と対応付けた",
  unbind: "取り込み元との対応を外した",
  edit: "課題を書き換えた",
  improvement: "改修の提案から課題を作った",
  upstream_read: "取り込み元の更新を読んだと記録した",
  upstream_observation_change:
    "取り込み元のコメント数・更新日時の観測が変わった",
  upstream_state_change: "取り込み元の状態が変わった",
  policy_state_change: "取り込みの方針の範囲内か外かが変わった",
};

// 実行の記録の「何の実行か」。判断点は役割の言葉で呼び、番号は括弧で添える。知らない値は値のまま出す。
export const JUDGMENT_LABELS: Record<string, string> = {
  J1: "分類する判断点（J1）",
  J2: "計画を作る判断点（J2）",
  J3: "委譲の指示文を書く判断点（J3）",
  J4: "問いを振り分ける判断点（J4）",
  J5: "完了を検証する判断点（J5）",
  J6: "学びを書く判断点（J6）",
  J8: "改修を提案する判断点（J8）",
};
export const RUN_KIND_LABELS: Record<string, string> = {
  judgment: "判断点",
  delegate: "委譲",
  contract: "委譲",
  predict: "衝突の予測",
};
export const RUN_RESULT_LABELS: Record<string, string> = {
  succeeded: "成功",
  errored: "失敗",
  launch_failed: "起動に失敗",
  timed_out: "時間切れ",
  malformed: "出力が読めなかった",
  invalid_output: "出力が不正だった",
  budget_exhausted: "予算を使い切った",
  interrupted: "中断",
};
export const RUN_RUNNING_LABEL = "実行中";
// 実行の記録に費用の値が無い（null）とき。0 とは違う。
export const NO_COST_LABEL = "費用の記録なし";

// 経緯の「誰が」
export const WHO_ME_RELAYED = "あなた（対話）";
export const WHO_ME_UI = "あなた（画面）";
export const WHO_ME_TTY = "あなた（端末）";
export const WHO_AGENT_RUN = "エージェント";
