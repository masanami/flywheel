import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client.ts";
import type { Ingestable } from "../api/types.gen.ts";

export type IngestableResult =
  | { state: "loading" }
  // 取り込めるものを読む問い合わせそのものの失敗（応答の ingestable.error とは別）。
  | { state: "failed"; message: string }
  | { state: "ready"; ingestable: Ingestable };

// 取り込めるもの（上流の読み取りを伴う）を、この画面を開いたときと reload を呼んだときだけ読む。
// 変化の通し番号や 60 秒ごとの読み直しでは読まない。結果はこの画面の中だけで持ち、全エージェントの状況へ混ぜない。
export function useIngestable(agentName: string): {
  result: IngestableResult;
  reload: () => void;
} {
  const [n, setN] = useState(0);
  const [held, setHeld] = useState<{
    target: string;
    result: IngestableResult;
  } | null>(null);
  const target = `${agentName}#${n}`;

  // biome-ignore lint/correctness/useExhaustiveDependencies: 読み直しの合図は target だけ
  useEffect(() => {
    let cancelled = false;
    api.statusIngestable(agentName).then(
      (s) => {
        if (cancelled) return;
        setHeld({
          target,
          result: {
            state: "ready",
            ingestable: s.ingestable ?? { error: null, items: [] },
          },
        });
      },
      (e) => {
        if (cancelled) return;
        setHeld({
          target,
          result: {
            state: "failed",
            message: e instanceof Error ? e.message : String(e),
          },
        });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [target]);

  const reload = useCallback(() => setN((x) => x + 1), []);
  // 読んでいる最中（対象の印が違う間）は読み込み中として返す。
  const result: IngestableResult =
    held && held.target === target ? held.result : { state: "loading" };
  return { result, reload };
}
