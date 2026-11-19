import { useEffect, useRef, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";

// 「サイクルを回す」のボタン。「いま進められること」・サイクルの画面・計画の承認の後で共通に使う。
// 押せるのは、サイクルを回すと進むものが 1 件以上あり、サイクルの状態が走っていない（idle）ときだけ。
export function canStartCycle(total: number, cycleState: string): boolean {
  return total >= 1 && cycleState === "idle";
}

export const CYCLE_STARTED_NOTE = "サイクルを始めました。";
export const CYCLE_NOT_STARTED_NOTE =
  "進められるものが無かったので、始めませんでした。";
export const CYCLE_LOCKED_NOTE =
  "ほかのサイクルが走っているので、始められませんでした。";

export function CycleStartButton({
  workspace,
  total,
  cycleState,
}: {
  workspace: string;
  total: number;
  cycleState: string;
}) {
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{ failed: boolean; text: string } | null>(
    null,
  );
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // 自動の再送はしない。
  const start = async () => {
    setBusy(true);
    setNote(null);
    try {
      const res = await api.startCycle(workspace);
      if (alive.current)
        setNote({
          failed: false,
          text: res.started ? CYCLE_STARTED_NOTE : CYCLE_NOT_STARTED_NOTE,
        });
    } catch (e) {
      if (!alive.current) return;
      if (e instanceof ApiError && e.code === "locked") {
        setNote({ failed: false, text: CYCLE_LOCKED_NOTE });
      } else {
        const m = e instanceof Error ? e.message : String(e);
        setNote({ failed: true, text: `サイクルを始められませんでした: ${m}` });
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  return (
    <span className="cycle-start-part">
      <button
        type="button"
        className="cycle-start"
        disabled={busy || !canStartCycle(total, cycleState)}
        onClick={start}
      >
        サイクルを回す
      </button>
      {note ? (
        <output
          className="cycle-notice"
          role={note.failed ? "alert" : "status"}
        >
          {note.text}
        </output>
      ) : null}
    </span>
  );
}
