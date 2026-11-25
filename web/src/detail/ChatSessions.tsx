import type { ChallengeChatSession } from "../api/types.gen.ts";
import { chatCost } from "../chat/texts.ts";
import { hrefOf } from "../shell/route.ts";
import { shortTime } from "../when.ts";

// 課題に紐づいた会話の一覧。費用は、この課題の仕様づくりに数えた額。1 件も無ければ区画ごと出さない。
export function ChatSessions({
  agentName,
  sessions,
}: {
  agentName: string;
  sessions: readonly ChallengeChatSession[];
}) {
  if (sessions.length === 0) return null;
  return (
    <section className="detail-chat" data-testid="detail-chat-sessions">
      <h3>紐づいた会話</h3>
      <ul>
        {sessions.map((s) => (
          <li key={s.id}>
            <time className="now-hint" dateTime={s.started_at}>
              {shortTime(s.started_at)}
            </time>{" "}
            <span>{s.first_message}</span>{" "}
            <span>この課題に数えた費用 {chatCost(s.cost_usd)}</span>{" "}
            <a
              href={hrefOf({
                kind: "transcript",
                name: agentName,
                sessionId: s.id,
              })}
            >
              全文
            </a>
          </li>
        ))}
      </ul>
    </section>
  );
}
