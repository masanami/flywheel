import { useContext, useState } from "react";
import { Board } from "../board/Board.tsx";
import type { LiveState } from "../live/useLive.ts";
import { InitialViewContext, type RelationView } from "../nav.tsx";
import { GraphView } from "./GraphView.tsx";

export const GRAPH_TITLE = "関係グラフ";

// 本番の画面の中身。ボードの 1 画面の中に「グラフ」と「ボード」の表示の切り替えを置く（既定はグラフ）。
// グラフはワークスペースを選択欄で選んで 1 つずつ描き、右側はターミナル／対話の欄のために空けておく（欄は作らない）。
// ボードの表示は従来のボードそのもの（挙動を変えない）。
export function RelationScreen({ live }: { live: LiveState }) {
  const initial = useContext(InitialViewContext);
  const [view, setView] = useState<RelationView>(initial);
  const [chosen, setChosen] = useState<string | null>(null);
  // ワークスペースごとに開いている親（サーバには持たない）
  const [open, setOpen] = useState<Record<string, string[]>>({});
  const readable = live.workspaces.filter((w) => w.state === "ok");
  const ws =
    readable.find((w) => w.name === chosen)?.name ?? readable[0]?.name ?? null;

  const switcher = (
    <fieldset aria-label="表示" className="graph-view-switch">
      <button
        type="button"
        aria-pressed={view === "graph"}
        onClick={() => setView("graph")}
      >
        グラフ
      </button>
      <button
        type="button"
        aria-pressed={view === "board"}
        onClick={() => setView("board")}
      >
        ボード
      </button>
    </fieldset>
  );

  if (view === "board") {
    return (
      <section aria-label="ボード" className="board-screen">
        <div className="graph-topbar graph-topbar-board">{switcher}</div>
        <Board live={live} />
      </section>
    );
  }
  return (
    <section aria-label={GRAPH_TITLE} className="graph-screen">
      <div className="graph-topbar">
        {switcher}
        <label className="graph-ws-picker">
          <span>ワークスペース</span>
          <select
            value={ws ?? ""}
            disabled={readable.length === 0}
            onChange={(e) => setChosen(e.target.value)}
          >
            {readable.map((w) => (
              <option key={w.name} value={w.name}>
                {w.name}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className="graph-shell">
        <div className="graph-shell-main">
          <GraphView
            key={ws ?? ""}
            live={live}
            ws={ws}
            openParents={ws ? open[ws] : undefined}
            onOpenParents={(ids) => {
              if (ws) setOpen((o) => ({ ...o, [ws]: ids }));
            }}
          />
        </div>
        {/* 右側は、ターミナル／対話の欄のために空けておく（欄は作らない） */}
        <div className="graph-dock-space" aria-hidden="true" />
      </div>
    </section>
  );
}
