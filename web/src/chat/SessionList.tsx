import { useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { ChatSessionSummary } from "../api/types.gen.ts";
import { LOADING_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { shortTime } from "../when.ts";
import { errorText } from "./errorText.ts";
import {
  CHAT_RESUME_UNAVAILABLE_TEXT,
  CHAT_SESSIONS_EMPTY_TEXT,
  CHAT_TRANSCRIPT_GONE_TEXT,
  chatCost,
} from "./texts.ts";

// 欄の会話の一覧（閉じた会話を含む。並びはサーバが返す新しい順のまま）。開いている間だけ読む。
export function SessionList({
  live,
  agentName,
  resumeDisabled,
  onResumed,
}: {
  live: LiveState;
  agentName: string;
  resumeDisabled: boolean;
  onResumed: () => void;
}) {
  const { result, reload } = useScreenData(live, agentName, () =>
    api.chatSessions(agentName),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const resume = async (id: string) => {
    setBusy(true);
    setError(null);
    try {
      await api.chatResume(agentName, id);
      onResumed();
    } catch (e) {
      setError(
        e instanceof ApiError && e.code === "transcript_unavailable"
          ? CHAT_RESUME_UNAVAILABLE_TEXT
          : errorText(e),
      );
      reload();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="chat-log" data-testid="chat-sessions">
      {error !== null ? <p role="alert">{error}</p> : null}
      {result.state === "loading" ? <p>{LOADING_TEXT}</p> : null}
      {result.state === "error" ? <p>{errorText(result.error)}</p> : null}
      {result.state === "ready" && result.data.sessions.length === 0 ? (
        <p>{CHAT_SESSIONS_EMPTY_TEXT}</p>
      ) : null}
      {result.state === "ready" ? (
        <ul className="chat-messages">
          {result.data.sessions.map((s) => (
            <SessionRow
              key={s.id}
              agentName={agentName}
              session={s}
              disabled={busy || resumeDisabled}
              onResume={() => void resume(s.id)}
            />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function SessionRow({
  agentName,
  session: s,
  disabled,
  onResume,
}: {
  agentName: string;
  session: ChatSessionSummary;
  disabled: boolean;
  onResume: () => void;
}) {
  return (
    <li className="chat-session" data-testid="chat-session">
      <div className="now-hint">
        <time dateTime={s.started_at}>{shortTime(s.started_at)}</time>{" "}
        <span>{chatCost(s.cost_usd)}</span>
        {s.open ? <span> いまの会話</span> : null}
      </div>
      <div>{s.first_message}</div>
      {s.challenges.length > 0 ? (
        <ul className="chat-session-challenges">
          {s.challenges.map((c) => (
            <li key={c.challenge_id}>
              <a
                href={hrefOf({
                  kind: "challenge",
                  name: agentName,
                  id: c.challenge_id,
                  view: "tree",
                })}
              >
                {c.title}
              </a>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="chat-session-actions">
        {s.transcript_available ? (
          <>
            <a
              href={hrefOf({
                kind: "transcript",
                name: agentName,
                sessionId: s.id,
              })}
            >
              全文
            </a>
            {s.open ? null : (
              <button type="button" disabled={disabled} onClick={onResume}>
                続ける
              </button>
            )}
          </>
        ) : (
          <span>{CHAT_TRANSCRIPT_GONE_TEXT}</span>
        )}
      </div>
    </li>
  );
}
