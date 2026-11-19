import { useEffect, useRef, useState } from "react";
import type { LiveState } from "../live/useLive.ts";

export type ScreenData<T> =
  | { state: "loading" }
  // error は例外そのまま（ApiError の code で not_found などを見分けられる）。
  | { state: "error"; error: unknown }
  | { state: "ready"; data: T };

// 画面ごとの読み込みの形。その画面を開いている間だけ読み、そのエージェントの変化の通し番号が進んだら読み直す。
// ほかのエージェントの合図では読み直さず、画面が裏に回っている間は読み直さない（表に戻ったときに 1 回だけ読む）。
export function useScreenData<T>(
  live: Pick<LiveState, "revision">,
  workspace: string,
  load: () => Promise<T>,
  deps: readonly unknown[] = [],
): { result: ScreenData<T>; reload: () => void } {
  const rev = live.revision[workspace] ?? 0;
  // 結果は、読んだ対象（エージェントと deps）の印を付けて持つ。描画中の対象と違えば、読み込み中として返す
  // （対象が変わった直後の描画で、前の対象のデータを見せない）。通し番号・手動の読み直しでは、いまの値を残す。
  const target = JSON.stringify([workspace, ...deps]);
  const [held, setHeld] = useState<{
    target: string;
    result: ScreenData<T>;
  } | null>(null);
  const [manual, setManual] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  const stale = useRef(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: 読み直しの合図は target・rev・manual だけ
  useEffect(() => {
    let cancelled = false;
    const run = () => {
      loadRef.current().then(
        (data) => {
          if (!cancelled) setHeld({ target, result: { state: "ready", data } });
        },
        (e) => {
          if (!cancelled)
            setHeld({ target, result: { state: "error", error: e } });
        },
      );
    };
    if (document.visibilityState === "hidden") {
      stale.current = true;
    } else {
      stale.current = false;
      run();
    }
    const onVisible = () => {
      if (document.visibilityState !== "hidden" && stale.current) {
        stale.current = false;
        run();
      }
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      cancelled = true;
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [target, rev, manual]);

  const result: ScreenData<T> =
    held && held.target === target ? held.result : { state: "loading" };
  return { result, reload: () => setManual((n) => n + 1) };
}
