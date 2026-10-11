import { useState } from "react";
import { useLive } from "./live/useLive.ts";
import { screens } from "./screens.tsx";

export function App() {
  const live = useLive();
  const [current, setCurrent] = useState(screens[0]?.id);
  const screen = screens.find((s) => s.id === current) ?? screens[0];

  return (
    <main>
      <h1>flywheel</h1>
      <output>
        {live.connected ? "接続中" : "切断中（再接続を試みています）"}
        {" ・ 最終更新: "}
        {live.lastUpdated ? live.lastUpdated.toLocaleString() : "未取得"}
      </output>
      {live.loadError ? (
        <p role="alert">読み込みに失敗: {live.loadError}</p>
      ) : null}
      {live.fleetError ? (
        <p role="alert">集約の読み込みに失敗: {live.fleetError}</p>
      ) : null}
      <nav>
        {screens.map((s) => (
          <button
            key={s.id}
            type="button"
            aria-pressed={s.id === screen?.id}
            onClick={() => setCurrent(s.id)}
          >
            {s.title}
          </button>
        ))}
      </nav>
      {screen?.render(live)}
    </main>
  );
}
