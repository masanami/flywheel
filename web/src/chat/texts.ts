// 対話の欄の文言（受入基準のとおり逐語）。
export const CHAT_EMPTY_TEXT = "オーケストレーターに、相談や指示を書けます。";
export const CHAT_REPLYING_TEXT = "返事を待っています…";
export const CHAT_FULL_TOOLS_TEXT = "すべての道具を許可しています";
export const CHAT_ENGINE_MISSING_TEXT = "実行エンジンが見つかりません。";

// 返事の終わり方（state）ごとに欄へ出す文。done は出さない。
export const CHAT_STATE_TEXTS: Record<string, string> = {
  replying: CHAT_REPLYING_TEXT,
  stopped: "止めました。",
  limit:
    "上限に達したので、ここで止まりました。続きは、もう一度送ってください。",
  rate_limited: "利用上限に達しました。時間を置いて、もう一度送ってください。",
  session_lost:
    "前の会話を続けられませんでした。新しい会話として、もう一度送ってください。",
  failed: "返事を作れませんでした。もう一度送れます。",
};

// 費用は小数 2 桁（$0.42）。
export const chatCost = (n: number): string => `$${n.toFixed(2)}`;

// 会話が無いときに、欄へ出す知らせの上限。
export const CHAT_NOTICE_LIMIT = 3;
