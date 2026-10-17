import type { CycleStatus } from "../api/types.gen.ts";

// state の値 → 表示（M4H34: stale は「更新なし」。止まったとは断定しない）。
const STATE_TEXT: Record<string, string> = {
  running: "実行中",
  idle: "停止中",
  stale: "⚠ 更新なし",
};

const RESULT_TEXT: Record<string, string> = {
  completed: "完了",
  aborted: "中断（異常終了）",
  interrupted: "中断（プロセス消失）",
};

// 列の見出しの 1 行。サイクルの状態と、最後に終わった周の時刻・結果を、server が返した値のまま出す。
export function CycleHeader({ cycle }: { cycle: CycleStatus }) {
  const stateText = STATE_TEXT[cycle.state] ?? cycle.state;
  return (
    <div
      className="agent-column-cycle-status"
      data-cycle-status={cycle.state}
      data-testid="cycle-header"
    >
      <span className="agent-column-cycle-status-dot" aria-hidden="true" />
      <span
        className="cycle-state"
        title={
          cycle.state === "stale"
            ? "しきい値を超えて記録がありません。停止したとは限らず、長い委譲でも出ます"
            : undefined
        }
      >
        {stateText}
      </span>
      {cycle.last_ended_at === null ? (
        <span className="cycle-last">周はまだありません</span>
      ) : (
        <span className="cycle-last">
          前回 <time dateTime={cycle.last_ended_at}>{cycle.last_ended_at}</time>
          {cycle.last_result !== null &&
            ` ${RESULT_TEXT[cycle.last_result] ?? cycle.last_result}`}
        </span>
      )}
    </div>
  );
}
