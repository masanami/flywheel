import { useEffect, useState } from "react";
import { api } from "../api/client.ts";
import type { CycleInfo } from "../api/proposed.ts";
import type { Workspace } from "../api/types.gen.ts";

const CYCLE_TEXT: Record<CycleInfo["cycle_state"], string> = {
  idle: "停止中",
  running: "サイクル実行中",
  // 旧 board（claude-flywheel-board の Issue #154）と同じ文言。止まったとは断定しない
  stale: "⚠ 更新なし",
};

// 案③: 旧 board の列の見出しにあった、サイクルの状態・優先度方針のモードの置き場所の案。
// 列の見出しの直前に 1 行で置く。差し込みの表示は引き継がない（flywheel#207 の決定 6）。
export function CycleHeaders({
  workspaces,
  refreshKey,
}: {
  workspaces: Workspace[];
  refreshKey: unknown;
}) {
  const [info, setInfo] = useState<Record<string, CycleInfo>>({});
  const [error, setError] = useState<string | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey は再取得の合図
  useEffect(() => {
    let live = true;
    Promise.all(
      workspaces
        .filter((w) => w.state === "ok")
        .map(async (w) => [w.name, await api.cycleInfo(w.name)] as const),
    ).then(
      (entries) => {
        if (!live) return;
        setInfo(Object.fromEntries(entries));
        setError(null);
      },
      (e) => live && setError(String(e)),
    );
    return () => {
      live = false;
    };
  }, [workspaces, refreshKey]);
  if (error) return <p role="alert">サイクル情報の取得に失敗: {error}</p>;
  return (
    <section className="cycle-headers" aria-label="列の見出しの案">
      {Object.entries(info).map(([name, i]) => (
        <div key={name} className="cycle-header" data-cycle={i.cycle_state}>
          <b>{name}</b>
          <span
            className="cycle-state"
            title={
              i.cycle_state === "stale"
                ? "しきい値を超えて記録がありません。停止したとは限らず、長い委譲でも出ます"
                : undefined
            }
          >
            {CYCLE_TEXT[i.cycle_state]}
          </span>
          <span>{i.policy_mode}</span>
        </div>
      ))}
    </section>
  );
}
