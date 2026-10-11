import { createContext } from "react";

// 課題の詳細を開く口と、いま描いているワークスペースの名前。画面の部品が詳細へのボタンを出すのに使う。
export const OpenDetailContext = createContext<
  ((workspace: string, id: string) => void) | null
>(null);
export const WorkspaceNameContext = createContext<string | null>(null);

// 最後に読み込みに成功した時刻。カードのモーダルが詳細を読み直す合図に使う。
export const RefreshKeyContext = createContext<unknown>(null);
