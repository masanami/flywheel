import { createContext } from "react";

// 課題の詳細を開く口。開発時の mock の案の画面が使う。
export const OpenDetailContext = createContext<
  ((workspace: string, id: string) => void) | null
>(null);

// 最後に読み込みに成功した時刻。カードのモーダルが詳細を読み直す合図に使う。
export const RefreshKeyContext = createContext<unknown>(null);

// 画面の切り替え口。ある画面から別の画面（id は screens の id）へ移るのに使う。
export const ScreenNavContext = createContext<((id: string) => void) | null>(
  null,
);
