import { useState } from "react";
import { CardDetailModal } from "./board/CardDetailModal.tsx";
import { toAgentBoard } from "./board/adapt.ts";
import { useLive } from "./live/useLive.ts";
import {
  OpenDetailContext,
  RefreshKeyContext,
  ScreenNavContext,
} from "./nav.tsx";
import { screens } from "./screens.tsx";

export function App() {
  const live = useLive();
  const [current, setCurrent] = useState(screens[0]?.id);
  // 開発時の mock の案の画面が課題の詳細を開く口。ボードのカードは自分でモーダルを開く。
  const [detail, setDetail] = useState<{ ws: string; id: string } | null>(null);
  const screen = screens.find((s) => s.id === current) ?? screens[0];

  const detailWs = detail
    ? live.workspaces.find((w) => w.name === detail.ws)
    : undefined;
  const detailChallenge =
    detail && detailWs
      ? toAgentBoard(detailWs, live.data[detailWs.name]).challenges.find(
          (c) => c.id === detail.id,
        )
      : undefined;

  return (
    <main className="app-layout">
      <div className="view-bar">
        <h1>flywheel</h1>
        {screens.length > 1 ? (
          <nav>
            {screens.map((s) => (
              <button
                key={s.id}
                type="button"
                className="filter-chip"
                aria-pressed={s.id === screen?.id}
                onClick={() => setCurrent(s.id)}
              >
                {s.title}
              </button>
            ))}
          </nav>
        ) : null}
        <output>
          {live.connected ? "接続中" : "切断中（再接続を試みています）"}
          {" ・ 最終更新: "}
          {live.lastUpdated ? live.lastUpdated.toLocaleString() : "未取得"}
        </output>
      </div>
      {live.loadError ? (
        <p role="alert">読み込みに失敗: {live.loadError}</p>
      ) : null}
      {live.fleetError ? (
        <p role="alert">集約の読み込みに失敗: {live.fleetError}</p>
      ) : null}
      <div
        className={
          screen?.id === "board" ? "view-body view-body-board" : "view-body"
        }
      >
        <ScreenNavContext.Provider value={setCurrent}>
          <OpenDetailContext.Provider value={(ws, id) => setDetail({ ws, id })}>
            {screen?.render(live)}
          </OpenDetailContext.Provider>
        </ScreenNavContext.Provider>
      </div>
      {detail && detailWs && detailChallenge ? (
        <RefreshKeyContext.Provider value={live.lastUpdated}>
          <CardDetailModal
            challenge={detailChallenge}
            agentName={detailWs.name}
            workspacePath={detailWs.path}
            onClose={() => setDetail(null)}
          />
        </RefreshKeyContext.Provider>
      ) : null}
    </main>
  );
}
