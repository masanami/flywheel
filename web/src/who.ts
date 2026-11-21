import { WHO_ME_RELAYED, WHO_ME_TTY, WHO_ME_UI } from "./labels.ts";

// 「誰が」を、記録の経路と本人確認の方式から決める（経緯の行と、タスクの詳細の回答で同じ決め方）。
// 方式が relayed なら「あなた（対話）」、経路が ui なら「あなた（画面）」、経路が cli で方式が tty_confirm なら
// 「あなた（ターミナル）」。どれにも当たらなければ、経路の値をそのまま返す。
export function whoOf(channel: string, verification: string | null): string {
  if (verification === "relayed") return WHO_ME_RELAYED;
  if (channel === "ui") return WHO_ME_UI;
  if (channel === "cli" && verification === "tty_confirm") return WHO_ME_TTY;
  return channel;
}
