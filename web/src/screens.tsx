import type { ReactNode } from "react";
import { Board } from "./board/Board.tsx";
import type { LiveState } from "./live/useLive.ts";

export interface Screen {
  id: string;
  title: string;
  render: (live: LiveState) => ReactNode;
}

// 本番の UI はボードの 1 画面だけを持ち、タブを描かない（M4H23）。詳細はボードの上のモーダルで開く。
export const screens: Screen[] = [
  {
    id: "board",
    title: "ボード",
    render: (live) => (
      <section aria-label="ボード" className="board-screen">
        <Board live={live} />
      </section>
    ),
  },
];

// 開発時の mock モードだけが、UX 案の画面をここへ足す（main.tsx）。足した画面があるときだけタブを描く。
export function registerScreens(extra: Screen[]): void {
  for (const s of extra) {
    if (!screens.some((x) => x.id === s.id)) screens.push(s);
  }
}
