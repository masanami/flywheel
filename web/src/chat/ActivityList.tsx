import type { ChatActivity } from "../api/types.gen.ts";
import { CHAT_ACTIVITY_STATE_TEXTS } from "./texts.ts";

// 返事の下の道具の一覧（たためる）。1 行に、道具の名前・対象の 1 行・状態。対象は core が切った 1 行をそのまま出す。
export function ActivityList({
  activity,
}: {
  activity: readonly ChatActivity[];
}) {
  if (activity.length === 0) return null;
  return (
    <details className="chat-activity" data-testid="chat-activity">
      <summary>使った道具（{activity.length} 件）</summary>
      <ul>
        {activity.map((a, i) => (
          // 過去の行は識別が空なので、並びの位置を key にする（並びは増えるだけ）
          // biome-ignore lint/suspicious/noArrayIndexKey: 上の理由
          <li key={i} data-state={a.state}>
            <b>{a.tool}</b> <code>{a.target}</code>{" "}
            <span>{CHAT_ACTIVITY_STATE_TEXTS[a.state] ?? a.state}</span>
          </li>
        ))}
      </ul>
    </details>
  );
}
