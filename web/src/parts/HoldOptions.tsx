import { useCallback, useState } from "react";
import { api } from "../api/client.ts";
import { ApiError } from "../api/error.ts";
import type { Hold, HoldOption } from "../api/types.gen.ts";
import { Markdown } from "../markdown/Markdown.tsx";

export const CONVERSATION_ONLY_NOTE = "対話で伝えてください";
export const HOLD_CHANGED_NOTE =
  "問いか選択肢が変わっていました。読み直した問いを確かめて、もう一度選んでください。";
export const CONFIRM_CANCEL = "やめる";
export const CONFIRM_SEND = "この内容で答える";

// ボタンで答えられる形か。選択肢があり、照合の値を受け取っているときだけ。
// 「ボタンで選べるか」の判定は core が返す option.button の値（画面では判定しない）。
export const hasOptions = (h: Pick<Hold, "options" | "check">): boolean =>
  h.options.length > 0 && (h.check ?? "") !== "";

export interface HoldAnswerState {
  busy: boolean;
  notice: string | null;
  error: string | null;
  answer: (holdId: string, option: string, check: string) => Promise<void>;
}

// 回答の口を呼ぶ。応答が返るまで busy（二度押しを防ぐ）。409 は問いが変わった旨を出して読み直す。
// 成立しても失敗しても、課題の詳細を読み直す（onChanged）。
export function useHoldAnswer(
  agentName: string,
  challengeId: string,
  onChanged: () => void,
): HoldAnswerState {
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const answer = useCallback(
    async (holdId: string, option: string, check: string) => {
      setBusy(true);
      setNotice(null);
      setError(null);
      try {
        await api.answerHold(agentName, challengeId, holdId, { option, check });
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) {
          setNotice(HOLD_CHANGED_NOTE);
        } else {
          setError(e instanceof Error ? e.message : String(e));
        }
      } finally {
        setBusy(false);
      }
      onChanged();
    },
    [agentName, challengeId, onChanged],
  );
  return { busy, notice, error, answer };
}

export function HoldAnswerNotices({ state }: { state: HoldAnswerState }) {
  return (
    <>
      {state.notice !== null ? (
        <p role="alert" data-testid="hold-changed">
          {state.notice}
        </p>
      ) : null}
      {state.error !== null ? (
        <p role="alert" data-testid="hold-answer-error">
          {state.error}
        </p>
      ) : null}
    </>
  );
}

// 保留の選択肢。ボタンで選べるものはボタン、選べないものは見出しと説明に案内を添える。
// 押すと確かめを 1 回はさみ、確かめた後にだけ回答の口へ送る（保留の ID・選択肢の識別・読んだときの照合の値）。
// 親は key に保留の ID と照合の値を含める（問いが変わったら確かめの途中を捨てる）。
export function HoldOptions({
  hold,
  state,
}: {
  hold: Pick<Hold, "hold_id" | "options" | "check">;
  state: HoldAnswerState;
}) {
  const [chosen, setChosen] = useState<HoldOption | null>(null);
  const check = hold.check ?? "";
  return (
    <div data-testid="hold-options" data-hold={hold.hold_id}>
      <ul className="hold-options">
        {hold.options.map((o) => (
          <li key={o.id} data-option={o.id}>
            <b>{o.label}</b>
            {/* 説明が見出しと同じ文のときは、同じ語を 2 回並べない。 */}
            {o.description !== "" && o.description !== o.label ? (
              <Markdown text={o.description} />
            ) : null}
            {o.button !== true ? (
              <p data-testid="hold-option-conversation">
                {CONVERSATION_ONLY_NOTE}
              </p>
            ) : (
              <button
                type="button"
                data-testid="hold-option-button"
                disabled={state.busy}
                onClick={() => setChosen(o)}
              >
                {o.label}
              </button>
            )}
          </li>
        ))}
      </ul>
      {chosen !== null ? (
        <div
          role="alertdialog"
          aria-label="回答の確かめ"
          data-testid="hold-confirm"
        >
          <p>「{chosen.label}」で答えます。よろしいですか？</p>
          <button
            type="button"
            data-testid="hold-confirm-send"
            disabled={state.busy}
            onClick={() => {
              const o = chosen;
              setChosen(null);
              void state.answer(hold.hold_id, o.id, check);
            }}
          >
            {CONFIRM_SEND}
          </button>{" "}
          <button
            type="button"
            data-testid="hold-confirm-cancel"
            disabled={state.busy}
            onClick={() => setChosen(null)}
          >
            {CONFIRM_CANCEL}
          </button>
        </div>
      ) : null}
    </div>
  );
}

// スーパーバイザーが上げた問いの、調べたことと推奨。関わっていない問い（どちらも空）では何も出さない。
export function SupervisorNote({
  findings,
  recommendation,
}: {
  findings: string | null;
  recommendation: string | null;
}) {
  if (!findings && !recommendation) return null;
  return (
    <div data-testid="hold-supervisor">
      {findings ? (
        <section aria-label="スーパーバイザーが調べたこと">
          <h4>スーパーバイザーが調べたこと</h4>
          <Markdown text={findings} />
        </section>
      ) : null}
      {recommendation ? (
        <section aria-label="推奨">
          <h4>推奨</h4>
          <Markdown text={recommendation} />
        </section>
      ) : null}
    </div>
  );
}
