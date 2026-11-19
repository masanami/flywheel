import { createContext } from "react";

// 最後に読み込みに成功した時刻。課題の詳細が詳細を読み直す合図に使う。
export const RefreshKeyContext = createContext<unknown>(null);
