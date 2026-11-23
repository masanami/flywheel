import type { SupervisorAction } from "../api/types.gen.ts";
import {
  SUPERVISOR_HAND_LABELS,
  SUPERVISOR_NO_HAND_TEXT,
  SUPERVISOR_REJECT_REASON_LABELS,
  SUPERVISOR_RESULT_LABELS,
  label,
} from "../labels.ts";

// スーパーバイザーの手の種類の語。手が決まらなかった（null）ときは、その旨の文。
export const handWord = (a: SupervisorAction): string =>
  a.hand === null
    ? SUPERVISOR_NO_HAND_TEXT
    : label(SUPERVISOR_HAND_LABELS, a.hand);

// 結果の語。断られた手は、断られた理由を添える。
export const resultWord = (a: SupervisorAction): string => {
  const result = label(SUPERVISOR_RESULT_LABELS, a.result);
  return a.result === "rejected" && a.reject_reason !== null
    ? `${result}（${label(SUPERVISOR_REJECT_REASON_LABELS, a.reject_reason)}）`
    : result;
};

// 手の中身: 調べたこと・添え書き・条件・答えた問い。課題の詳細の記録とタスクの詳細の記録が共有する。
export function SupervisorHandBody({ action }: { action: SupervisorAction }) {
  return (
    <>
      {action.findings !== "" ? (
        <p className="sv-hand-findings" data-testid="sv-hand-findings">
          調べたこと: {action.findings}
        </p>
      ) : null}
      {action.note !== "" ? (
        <p className="sv-hand-note" data-testid="sv-hand-note">
          添え書き: {action.note}
        </p>
      ) : null}
      {action.conditions.length > 0 ? (
        <ul className="sv-hand-conditions" data-testid="sv-hand-conditions">
          {action.conditions.map((c) => (
            <li key={c.number}>
              条件 {c.number}: {c.text}
            </li>
          ))}
        </ul>
      ) : null}
      {action.questions.length > 0 ? (
        <ul className="sv-hand-questions" data-testid="sv-hand-questions">
          {action.questions.map((q, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: 同じ問いが並びうる
            <li key={i}>
              問い: {q.question} ／ 答え: {q.answer}
            </li>
          ))}
        </ul>
      ) : null}
    </>
  );
}
