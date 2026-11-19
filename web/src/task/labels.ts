// タスクの詳細の実行の記録に出す語。対応の無いコードは生のコードをそのまま出す。
export const RUN_KIND_LABELS: Record<string, string> = {
  judgment: "判断",
  delegate: "委譲",
  contract: "委譲（接続ツール）",
  predict: "予測",
};

export const RUN_RESULT_LABELS: Record<string, string> = {
  succeeded: "成功",
  launch_failed: "起動できなかった",
  timed_out: "時間切れ",
  malformed: "出力が壊れていた",
  budget_exhausted: "予算を使い切った",
  errored: "失敗",
  invalid_output: "出力が不正",
  interrupted: "中断",
};

export const RUN_RUNNING_TEXT = "実行中";

// 委譲の実行（報告の API を呼ぶ対象）の種類。
export const DELEGATION_KINDS = ["delegate", "contract"];

// 時刻は、ブラウザのいまの時間帯で「月/日 時:分」に整える（正確な値は time の dateTime に持つ）。
export function shortTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
