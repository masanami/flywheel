import type { Screen } from "../screens.tsx";
import { GRAPH_TITLE, GraphScreen } from "./graph/GraphScreen.tsx";

// 開発時の mock モードで足す画面。部品は本物で、データの出どころ（api）だけが偽の API。
export const proposalScreens: Screen[] = [
  {
    // flywheel#176: ワークスペースを選ぶと課題の関係を 1 ページのグラフで示し、区分で色分けする案。
    // 2026-10-13 の決定 1 で、UX 案①③のボード（人間の操作待ちの帯と列の見出しの案つき）を
    // 同じ画面の「列のボード」の表示へ移した（既定はグラフ）。
    id: "graph-proposal",
    title: GRAPH_TITLE,
    render: (live) => <GraphScreen live={live} />,
  },
];
