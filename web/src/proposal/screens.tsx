import { Board } from "../board/Board.tsx";
import type { LiveState } from "../live/useLive.ts";
import type { Screen } from "../screens.tsx";
import { CycleHeaders } from "./CycleHeaders.tsx";
import { NeedsHumanStrip } from "./NeedsHumanStrip.tsx";

// UX 案①③: 人間の操作待ちの帯と列の見出しの案つきのボード
function BoardProposalScreen({ live }: { live: LiveState }) {
  return (
    <section aria-label="ボード（案）" className="board-screen">
      <NeedsHumanStrip live={live} />
      <CycleHeaders
        workspaces={live.workspaces}
        refreshKey={live.lastUpdated}
      />
      <Board live={live} />
    </section>
  );
}

// 開発時の mock モードで足す画面。部品は本物で、データの出どころ（api）だけが偽の API。
export const proposalScreens: Screen[] = [
  {
    id: "board-proposal",
    title: "ボード（案）",
    render: (live) => <BoardProposalScreen live={live} />,
  },
];
