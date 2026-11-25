import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { ChatTranscriptEntry } from "../api/types.gen.ts";
import { LOADING_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import { NotFound } from "../shell/NotFound.tsx";
import { hrefOf } from "../shell/route.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { shortTime } from "../when.ts";
import { errorText } from "./errorText.ts";
import {
  CHAT_TRANSCRIPT_KIND_TEXTS,
  CHAT_TRANSCRIPT_TRUNCATED_TEXT,
  CHAT_TRANSCRIPT_UNAVAILABLE_TEXT,
} from "./texts.ts";

// 会話 1 つの全文の画面（読み取りだけ）。中身は読むたびに Claude Code のセッションのファイルから作られるので、
// 無くなっていれば読めない。
export function TranscriptScreen({
  live,
  agentName,
  sessionId,
}: {
  live: LiveState;
  agentName: string;
  sessionId: string;
}) {
  const { result } = useScreenData(
    live,
    agentName,
    () => api.chatTranscript(agentName, sessionId),
    [sessionId],
  );
  const code =
    result.state === "error" && result.error instanceof ApiError
      ? result.error.code
      : null;
  if (code === "not_found") {
    return (
      <NotFound
        homeHref={hrefOf({ kind: "agent", name: agentName, screen: "now" })}
      />
    );
  }
  return (
    <article className="transcript" data-testid="chat-transcript">
      <h2>会話の全文</h2>
      {result.state === "loading" ? <p>{LOADING_TEXT}</p> : null}
      {result.state === "error" ? (
        code === "transcript_unavailable" ? (
          <p role="alert" data-testid="chat-transcript-unavailable">
            {CHAT_TRANSCRIPT_UNAVAILABLE_TEXT}
          </p>
        ) : (
          <p role="alert">{errorText(result.error)}</p>
        )
      ) : null}
      {result.state === "ready" ? (
        <ol className="transcript-entries">
          {result.data.entries.map((e, i) => (
            // 項目は識別を持たず、並びは末尾に増えるだけなので、位置を key にする
            // biome-ignore lint/suspicious/noArrayIndexKey: 上の理由
            <Entry key={i} entry={e} />
          ))}
        </ol>
      ) : null}
    </article>
  );
}

function Entry({ entry: e }: { entry: ChatTranscriptEntry }) {
  const kind = CHAT_TRANSCRIPT_KIND_TEXTS[e.kind] ?? e.kind;
  return (
    <li data-testid="transcript-entry" data-kind={e.kind}>
      <div className="now-hint">
        <b>{e.tool !== null ? `${kind}: ${e.tool}` : kind}</b>{" "}
        {e.at !== null ? <time dateTime={e.at}>{shortTime(e.at)}</time> : null}
      </div>
      {e.kind === "assistant" ? (
        <Markdown text={e.text} />
      ) : (
        <pre className="transcript-text">{e.text}</pre>
      )}
      {e.truncated ? (
        <p className="now-hint">{CHAT_TRANSCRIPT_TRUNCATED_TEXT}</p>
      ) : null}
    </li>
  );
}
