import { createContext, useContext } from "react";

// 対話の欄と、画面の中（課題の詳細・タスクの詳細）のつなぎ。
// 枠の外で描かれた画面（単体の試験）では、何もしない既定の値になる。
export interface ChatContextValue {
  // 欄を開き、入力の場所へフォーカスを移す（「この課題について相談する」）。
  openAndFocus: () => void;
  // タスクの詳細が、開いているタスクの題名を欄へ知らせる（課題は一覧の題名を枠が引く）。
  reportTask: (taskId: string | null, title: string | null) => void;
}

export const ChatContext = createContext<ChatContextValue>({
  openAndFocus: () => {},
  reportTask: () => {},
});

export const useChat = (): ChatContextValue => useContext(ChatContext);
