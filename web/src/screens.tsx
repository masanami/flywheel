import type { ReactNode } from "react";
import { RelationScreen } from "./graph/RelationScreen.tsx";
import type { LiveState } from "./live/useLive.ts";

export interface Screen {
  id: string;
  title: string;
  render: (live: LiveState) => ReactNode;
}

// 本番の UI はボードの 1 画面だけを持ち、タブを描かない（M4H23）。その中に「グラフ」と「ボード」の
// 表示の切り替えを置く（既定はグラフ）。詳細はボードの上のモーダルで開く。
export const screens: Screen[] = [
  {
    id: "board",
    title: "ボード",
    render: (live) => <RelationScreen live={live} />,
  },
];

// 開発時の mock モードだけが、画面をここへ足せる（main.tsx）。足した画面があるときだけタブを描く。
export function registerScreens(extra: Screen[]): void {
  for (const s of extra) {
    if (!screens.some((x) => x.id === s.id)) screens.push(s);
  }
}
