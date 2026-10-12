import { useState } from "react";
import { DetailScreen } from "./detail.tsx";
import { useLive } from "./live/useLive.ts";
import { OpenDetailContext, ScreenNavContext } from "./nav.tsx";
import { screens } from "./screens.tsx";

export function App() {
  const live = useLive();
  const [current, setCurrent] = useState(screens[0]?.id);
  const [detail, setDetail] = useState<{ ws: string; id: string } | null>(null);
  const screen = screens.find((s) => s.id === current) ?? screens[0];

  return (
    <main className="app-layout">
      <div className="view-bar">
        <h1>flywheel</h1>
        <nav>
          {screens.map((s) => (
            <button
              key={s.id}
              type="button"
              className="filter-chip"
              aria-pressed={s.id === screen?.id}
              onClick={() => {
                setCurrent(s.id);
                setDetail(null);
              }}
            >
              {s.title}
            </button>
          ))}
        </nav>
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
          screen?.id === "board" && !detail
            ? "view-body view-body-board"
            : "view-body"
        }
      >
        <ScreenNavContext.Provider
          value={(id) => {
            setCurrent(id);
            setDetail(null);
          }}
        >
          <OpenDetailContext.Provider value={(ws, id) => setDetail({ ws, id })}>
            {detail ? (
              <DetailScreen
                workspace={detail.ws}
                id={detail.id}
                workspaces={live.workspaces}
                refreshKey={live.lastUpdated}
                onClose={() => setDetail(null)}
              />
            ) : (
              screen?.render(live)
            )}
          </OpenDetailContext.Provider>
        </ScreenNavContext.Provider>
      </div>
    </main>
  );
}
