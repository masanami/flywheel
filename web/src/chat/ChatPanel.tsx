import { useEffect, useRef, useState } from "react";
import { NoticeItem } from "../NoticeItem.tsx";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { ChatMessage, ChatPostRequest } from "../api/types.gen.ts";
import { LOADING_TEXT } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import { type NoticeLine, noticeLine } from "../notices.ts";
import { useScreenData } from "../shell/useScreenData.ts";
import { useNotices } from "../useNotices.ts";
import {
  CHAT_EMPTY_TEXT,
  CHAT_ENGINE_MISSING_TEXT,
  CHAT_FULL_TOOLS_TEXT,
  CHAT_NOTICE_LIMIT,
  CHAT_REPLYING_TEXT,
  CHAT_STATE_TEXTS,
  chatCost,
} from "./texts.ts";

type Entry =
  | { kind: "message"; message: ChatMessage }
  | { kind: "notice"; line: NoticeLine };

// 欄に出す知らせ。会話が開いている（started_at がある）ときは、会話の始まりより後のものだけ。
// 会話が無いときは、直近の CHAT_NOTICE_LIMIT 件まで。どちらも時刻の古い順に返す。
export function noticesForPanel(
  lines: readonly NoticeLine[],
  startedAt: string | null,
): NoticeLine[] {
  const t = (l: NoticeLine) => Date.parse(l.at);
  const byTime = [...lines].sort((a, b) => t(a) - t(b));
  if (startedAt !== null) {
    const start = Date.parse(startedAt);
    return byTime.filter((l) => t(l) > start);
  }
  return byTime.slice(-CHAT_NOTICE_LIMIT);
}

// 発言（id の順）の間へ、知らせを時刻の順に差し込む。知らせは発言の表に書かず、画面の側で並べるだけ。
function interleave(
  messages: readonly ChatMessage[],
  notices: readonly NoticeLine[],
): Entry[] {
  const out: Entry[] = [];
  let i = 0;
  for (const m of messages) {
    while (
      i < notices.length &&
      Date.parse((notices[i] as NoticeLine).at) < Date.parse(m.created_at)
    ) {
      out.push({ kind: "notice", line: notices[i++] as NoticeLine });
    }
    out.push({ kind: "message", message: m });
  }
  while (i < notices.length) {
    out.push({ kind: "notice", line: notices[i++] as NoticeLine });
  }
  return out;
}

// いま見ているもの（開いている課題かタスク）。どちらも開いていなければ呼び出し側が null にする。
export interface Viewing {
  challengeId: string;
  taskId: string | null;
  title: string | null;
}

// 送るときの viewing。無いものは省く。
export function postViewing(v: Viewing | null): ChatPostRequest["viewing"] {
  if (v === null) return undefined;
  return {
    challenge_id: v.challengeId,
    ...(v.taskId !== null ? { task_id: v.taskId } : {}),
  };
}

function errorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 503) {
    return CHAT_ENGINE_MISSING_TEXT;
  }
  return e instanceof Error ? e.message : String(e);
}

function MessageView({ m }: { m: ChatMessage }) {
  const note =
    m.role === "orchestrator" ? CHAT_STATE_TEXTS[m.state] : undefined;
  return (
    <li
      className={`chat-message chat-${m.role}`}
      data-testid={`chat-message-${m.role}`}
    >
      {m.role === "owner" ? (
        <p className="chat-owner-body">{m.body}</p>
      ) : (
        <>
          {m.body !== "" ? <Markdown text={m.body} /> : null}
          {note !== undefined ? (
            <p
              className="chat-state"
              data-state={m.state}
              data-testid="chat-state"
            >
              {note}
            </p>
          ) : null}
          {m.activity.length > 0 ? (
            <details className="chat-activity" data-testid="chat-activity">
              <summary>実行したコマンド</summary>
              <ul>
                {m.activity.map((a, i) => (
                  // 同じコマンドが続きうるので、並びの位置を key にする（並びは増えるだけ）
                  // biome-ignore lint/suspicious/noArrayIndexKey: 上の理由
                  <li key={i}>
                    <code>{a.command}</code> {a.ok ? "（成功）" : "（失敗）"}
                  </li>
                ))}
              </ul>
            </details>
          ) : null}
        </>
      )}
    </li>
  );
}

// 共通の枠の右の、選んだエージェントの対話の欄。たたんだ状態は枠（Shell）が持つ。
export function ChatPanel({
  live,
  agentName,
  open,
  onToggle,
  viewing,
  focusRequest,
}: {
  live: LiveState;
  agentName: string;
  open: boolean;
  onToggle: () => void;
  viewing: Viewing | null;
  // 値が変わるたびに入力の場所へフォーカスを移す（0 は未要求）。
  focusRequest: number;
}) {
  const { result, reload } = useScreenData(live, agentName, () =>
    api.chat(agentName),
  );
  const notices = useNotices(live, agentName);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const input = useRef<HTMLTextAreaElement>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: フォーカスの合図は focusRequest だけ
  useEffect(() => {
    if (focusRequest > 0 && open) input.current?.focus();
  }, [focusRequest]);

  if (!open) {
    return (
      <aside className="chat-panel chat-collapsed" data-testid="chat-panel">
        <button type="button" aria-expanded={false} onClick={onToggle}>
          対話の欄を開く
        </button>
      </aside>
    );
  }

  const chat = result.state === "ready" ? result.data : null;
  const messages = chat ? [...chat.messages].sort((a, b) => a.id - b.id) : [];
  const entries = interleave(
    messages,
    noticesForPanel(
      notices.map((n) => noticeLine(n, agentName)),
      chat?.started_at ?? null,
    ),
  );
  const replying =
    chat !== null &&
    (chat.state === "replying" || messages.some((m) => m.state === "replying"));

  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await f();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
      reload();
    }
  };

  const send = () => {
    const t = text.trim();
    if (t === "") return;
    const req: ChatPostRequest = { text: t };
    const v = postViewing(viewing);
    if (v !== undefined) req.viewing = v;
    void run(async () => {
      await api.chatSend(agentName, req);
      // 送っている間に打ち足した分は消さない
      setText((cur) => (cur.trim() === t ? "" : cur));
    });
  };

  return (
    <aside
      className="chat-panel"
      data-testid="chat-panel"
      aria-label="対話の欄"
    >
      <header className="chat-header">
        <h2>オーケストレーターとの対話（{agentName}）</h2>
        <button type="button" aria-expanded={true} onClick={onToggle}>
          たたむ
        </button>
      </header>
      {chat?.tools === "full" ? (
        <p className="chat-tools" data-testid="chat-tools">
          {CHAT_FULL_TOOLS_TEXT}
        </p>
      ) : null}
      {viewing !== null && viewing.title !== null ? (
        <p className="chat-viewing" data-testid="chat-viewing">
          いま見ているもの: {viewing.title}
        </p>
      ) : null}
      <div className="chat-meta">
        {chat ? (
          <span data-testid="chat-cost">{chatCost(chat.cost_usd)}</span>
        ) : null}
        <button
          type="button"
          disabled={busy || replying}
          onClick={() => void run(() => api.chatNew(agentName))}
        >
          新しい会話
        </button>
      </div>
      <div className="chat-log">
        {result.state === "loading" ? <p>{LOADING_TEXT}</p> : null}
        {result.state === "error" ? (
          <p data-testid="chat-load-error">{errorText(result.error)}</p>
        ) : null}
        {chat && messages.length === 0 ? <p>{CHAT_EMPTY_TEXT}</p> : null}
        {entries.length > 0 ? (
          <ol className="chat-messages">
            {entries.map((e) =>
              e.kind === "message" ? (
                <MessageView key={e.message.id} m={e.message} />
              ) : (
                <NoticeItem key={e.line.key} line={e.line} badge />
              ),
            )}
          </ol>
        ) : null}
        {replying && !messages.some((m) => m.state === "replying") ? (
          <p className="chat-state">{CHAT_REPLYING_TEXT}</p>
        ) : null}
      </div>
      {error !== null ? (
        <p role="alert" data-testid="chat-error">
          {error}
        </p>
      ) : null}
      <form
        className="chat-form"
        onSubmit={(e) => {
          e.preventDefault();
          send();
        }}
      >
        <textarea
          ref={input}
          aria-label="オーケストレーターへの文"
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={3}
        />
        <div className="chat-actions">
          {replying ? (
            <button
              type="button"
              disabled={busy}
              onClick={() => void run(() => api.chatStop(agentName))}
            >
              止める
            </button>
          ) : null}
          <button
            type="submit"
            disabled={busy || replying || text.trim() === ""}
          >
            送る
          </button>
        </div>
      </form>
    </aside>
  );
}
