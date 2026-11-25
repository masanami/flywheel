import { useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type {
  ChatPrompt,
  ChatPromptAnswerRequest,
  ChatQuestion,
} from "../api/types.gen.ts";
import { errorText } from "./errorText.ts";
import {
  CHAT_PROMPT_NOT_PENDING_TEXT,
  CHAT_PROMPT_RESULT_TEXTS,
} from "./texts.ts";

type Send = (req: ChatPromptAnswerRequest) => void;

// 返事の中の確認 1 つ（許可の確認か問い）。答えを待つ間だけボタンを出し、ほかは答えの結果だけを出す。
// 送っている間の状態は確認ごとに持ち、欄の「止める」を塞がない。
export function PromptView({
  agentName,
  prompt,
  onAnswered,
}: {
  agentName: string;
  prompt: ChatPrompt;
  // 答えが通っても断られても呼ぶ（欄を読み直す）。
  onAnswered: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const send: Send = (req) => {
    setBusy(true);
    setError(null);
    api
      .chatAnswer(agentName, prompt.id, req)
      .then(
        // 通ったら、読み直した結果に変わるまでボタンは押せないままにする
        () => {},
        (e) => {
          setError(
            e instanceof ApiError && e.status === 409
              ? CHAT_PROMPT_NOT_PENDING_TEXT
              : errorText(e),
          );
          setBusy(false);
        },
      )
      .finally(onAnswered);
  };

  return (
    <div
      className="chat-prompt"
      data-testid="chat-prompt"
      data-prompt-id={prompt.id}
      data-kind={prompt.kind}
      data-state={prompt.state}
    >
      {prompt.state !== "pending" ? (
        <PromptResult prompt={prompt} />
      ) : prompt.kind === "question" ? (
        <QuestionForm questions={prompt.questions} busy={busy} send={send} />
      ) : (
        <PermissionForm prompt={prompt} busy={busy} send={send} />
      )}
      {error !== null ? <p role="alert">{error}</p> : null}
    </div>
  );
}

function PromptResult({ prompt }: { prompt: ChatPrompt }) {
  const answers = prompt.answer?.answers;
  if (prompt.state === "answered" && answers) {
    // 問いの並びの順に出す（答えの対応の並びは決まらない）
    return (
      <dl>
        {prompt.questions.map((q) => (
          <div key={q.question}>
            <dt>{q.question}</dt>
            <dd>回答: {answers[q.question]}</dd>
          </div>
        ))}
      </dl>
    );
  }
  const message = prompt.answer?.message;
  return (
    <p>
      {prompt.tool}: {CHAT_PROMPT_RESULT_TEXTS[prompt.state] ?? prompt.state}
      {message ? `（伝えた文: ${message}）` : null}
    </p>
  );
}

// 許可の確認。何を許すかを見て決められるように、対象は全文を切らずに出す。
// 答えはその 1 回だけの許可か、断るかの 2 つだけ。
function PermissionForm({
  prompt,
  busy,
  send,
}: {
  prompt: ChatPrompt;
  busy: boolean;
  send: Send;
}) {
  const [reason, setReason] = useState("");
  return (
    <>
      <p>
        許可の確認: <b>{prompt.tool}</b>
      </p>
      <pre className="chat-prompt-target" data-testid="chat-prompt-target">
        {prompt.target}
      </pre>
      <div className="chat-prompt-actions">
        <button
          type="button"
          disabled={busy}
          onClick={() => send({ decision: "allow" })}
        >
          許可する
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            const message = reason.trim();
            send(
              message === ""
                ? { decision: "deny" }
                : { decision: "deny", message },
            );
          }}
        >
          断る
        </button>
      </div>
      <label className="chat-prompt-text">
        断る理由（任意）
        <input
          type="text"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
      </label>
    </>
  );
}

// 問い 1 つに書きかけの答え。選んだ見出しと、選択肢に無い答えの文。
interface Draft {
  picked: string[];
  other: string;
}
const EMPTY: Draft = { picked: [], other: "" };

// 送る答えの文。複数を選んだ答えは、選択肢の順の見出し（と、書いた文）を「, 」でつなぐ。
function answerOf(q: ChatQuestion, d: Draft): string {
  const labels = q.options
    .map((o) => o.label)
    .filter((l) => d.picked.includes(l));
  const other = d.other.trim();
  return [...labels, ...(other !== "" ? [other] : [])].join(", ");
}

// 問い。答えは全部の問いの分をまとめて 1 回で送る。問いが 1 つで 1 つ選ぶものは、選択肢のボタンがそのまま送る。
function QuestionForm({
  questions,
  busy,
  send,
}: {
  questions: readonly ChatQuestion[];
  busy: boolean;
  send: Send;
}) {
  const [drafts, setDrafts] = useState<Record<number, Draft>>({});
  const immediate = questions.length === 1 && !questions[0]?.multi_select;
  const draftOf = (i: number) => drafts[i] ?? EMPTY;
  const put = (i: number, d: Draft) => setDrafts((cur) => ({ ...cur, [i]: d }));
  const complete = questions.every((q, i) => answerOf(q, draftOf(i)) !== "");

  return (
    <>
      {questions.map((q, i) => {
        const d = draftOf(i);
        return (
          <fieldset key={q.question} disabled={busy}>
            <legend>{q.question}</legend>
            <ul className="chat-prompt-options">
              {q.options.map((o) => (
                <li key={o.label}>
                  {q.multi_select ? (
                    <label>
                      <input
                        type="checkbox"
                        checked={d.picked.includes(o.label)}
                        onChange={(e) =>
                          put(i, {
                            ...d,
                            picked: e.target.checked
                              ? [...d.picked, o.label]
                              : d.picked.filter((l) => l !== o.label),
                          })
                        }
                      />
                      {o.label}
                    </label>
                  ) : (
                    <button
                      type="button"
                      aria-pressed={
                        immediate ? undefined : d.picked.includes(o.label)
                      }
                      onClick={() =>
                        immediate
                          ? send({ answers: { [q.question]: o.label } })
                          : put(i, { picked: [o.label], other: "" })
                      }
                    >
                      {o.label}
                    </button>
                  )}
                  {o.description !== "" ? (
                    <span className="chat-prompt-description">
                      {" "}
                      {o.description}
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
            <label className="chat-prompt-text">
              選択肢に無い答え
              <input
                type="text"
                aria-label={`選択肢に無い答え: ${q.question}`}
                value={d.other}
                onChange={(e) =>
                  put(i, {
                    // 1 つ選ぶ問いは、文を書いたら選んだ見出しを外す
                    picked: q.multi_select ? d.picked : [],
                    other: e.target.value,
                  })
                }
              />
            </label>
          </fieldset>
        );
      })}
      <button
        type="button"
        disabled={busy || !complete}
        onClick={() =>
          send({
            answers: Object.fromEntries(
              questions.map((q, i) => [q.question, answerOf(q, draftOf(i))]),
            ),
          })
        }
      >
        送る
      </button>
    </>
  );
}
