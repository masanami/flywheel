import { useEffect, useRef, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import { RELOAD_TEXT } from "../labels.ts";

export const STOP_REQUESTED_TEXT =
  "止める指示を出しました。いまの段が終わると止まります";

// 「サイクルを止める」のボタン。走っていて、止める指示がまだ出ていないときだけ、呼ぶ側が置く。
// 止めるのはサイクルだけで、委譲先の実行は取り消さない。成立したら onDone で画面を読み直す。自動の再送はしない。
export function CycleStopButton({
  workspace,
  onDone,
}: {
  workspace: string;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const stop = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.cycleStop(workspace);
      if (alive.current) onDone();
    } catch (e) {
      if (!alive.current) return;
      if (
        e instanceof ApiError &&
        (e.status === 403 || e.code === "forbidden_origin")
      ) {
        setError(RELOAD_TEXT);
      } else {
        const m = e instanceof Error ? e.message : String(e);
        setError(`サイクルを止められませんでした: ${m}`);
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  return (
    <span className="cycle-stop-part">
      <button
        type="button"
        className="cycle-stop"
        disabled={busy}
        onClick={stop}
      >
        サイクルを止める
      </button>
      {error ? (
        <output className="cycle-notice" role="alert">
          {error}
        </output>
      ) : null}
    </span>
  );
}
