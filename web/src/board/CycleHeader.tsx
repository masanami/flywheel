import { useEffect, useRef, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/proposed.ts";
import type { CycleStatus } from "../api/types.gen.ts";

const LOCKED_NOTE = "サイクルが既に動いているため、1 周を起動しませんでした。";
const FAILED_NOTE = "1 周を始められませんでした: ";

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
export function CycleHeader({
  cycle,
  workspace,
}: {
  cycle: CycleStatus;
  workspace?: string;
}) {
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{
    kind: "started" | "locked" | "failed";
    text: string;
  } | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // 自動の再送はしない。周が始まった後の失敗は GET …/cycle の last_result で見る。
  const start = async () => {
    if (workspace === undefined) return;
    setBusy(true);
    setNotice(null);
    try {
      await api.startCycle(workspace);
      if (alive.current)
        setNotice({ kind: "started", text: "1 周を起動しました。" });
    } catch (e) {
      if (!alive.current) return;
      if (e instanceof ApiError && e.code === "locked") {
        setNotice({ kind: "locked", text: LOCKED_NOTE });
      } else {
        const m = e instanceof Error ? e.message : String(e);
        setNotice({ kind: "failed", text: FAILED_NOTE + m });
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  };

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
      {workspace !== undefined && (
        <button
          type="button"
          className="cycle-start"
          data-testid="cycle-start"
          disabled={busy}
          onClick={start}
        >
          1 周を起動
        </button>
      )}
      {notice && (
        <div
          className="cycle-notice"
          data-testid="cycle-notice"
          data-kind={notice.kind}
          role={notice.kind === "failed" ? "alert" : "status"}
        >
          {notice.text}
        </div>
      )}
    </div>
  );
}
