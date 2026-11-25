// 対話の欄の文言（受入基準のとおり逐語）。
export const CHAT_EMPTY_TEXT = "オーケストレーターに、相談や指示を書けます。";
export const CHAT_REPLYING_TEXT = "返事を待っています…";
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

// 道具の一覧の行の状態。「実行中」は running の行だけに出る。
export const CHAT_ACTIVITY_STATE_TEXTS: Record<string, string> = {
  running: "実行中",
  ok: "成功",
  error: "失敗",
};

// 答えを待っていない確認の結果（問いに答えたときは、問いと答えを出すので、ここには無い）。
export const CHAT_PROMPT_RESULT_TEXTS: Record<string, string> = {
  allowed: "許可しました",
  denied: "断りました",
  expired: "答えられませんでした",
};
export const CHAT_PROMPT_NOT_PENDING_TEXT =
  "この確認は、もう答えを待っていません。";

export const CHAT_SESSIONS_EMPTY_TEXT = "会話はまだありません。";
export const CHAT_TRANSCRIPT_GONE_TEXT = "全文はもう無い";
export const CHAT_RESUME_UNAVAILABLE_TEXT =
  "この会話は全文のファイルが無いので、続けられません。";
export const CHAT_TRANSCRIPT_UNAVAILABLE_TEXT =
  "全文を読めません。Claude Code が古い会話を消した可能性があります。発言と返事は、欄の記録で読めます。";
export const CHAT_TRANSCRIPT_TRUNCATED_TEXT = "長いので、ここで切りました。";
export const CHAT_TRANSCRIPT_KIND_TEXTS: Record<string, string> = {
  owner: "オーナー",
  assistant: "返事",
  tool_use: "道具の使用",
  tool_result: "道具の結果",
};
