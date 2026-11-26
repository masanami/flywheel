import type { PlanQuestion } from "../api/types.gen.ts";
import { ClampedText } from "./ClampedText.tsx";

// 問いの表示（計画の論点の問いと、下書きづくりの保留の問いで同じもの）。
// 選択肢は見せるだけ（選べない）。推奨の印と、記録されている答えを出す。
// recommendedMark は推奨の選択肢の見出しの後ろに付ける印。reason は推奨の理由（無ければ出さない）。
export function PlanQuestionView({
  q,
  recommendedMark = " 推奨",
  reason,
}: {
  q: PlanQuestion;
  recommendedMark?: string;
  reason?: string;
}) {
  const answer = q.answer;
  const answerLabel = answer ? (answer.label ?? answer.text ?? "") : null;
  return (
    <div className="plan-question" data-testid="plan-question">
      <p data-field="question">
        問い {q.number}: <ClampedText text={q.question} />
      </p>
      <ol>
        {q.options.map((o, j) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
          <li key={j} data-recommended={j + 1 === q.recommended}>
            <ClampedText text={o.label} />
            {j + 1 === q.recommended && (
              <span className="plan-recommended-mark">{recommendedMark}</span>
            )}
          </li>
        ))}
      </ol>
      {reason !== undefined && (
        <p data-field="reason">
          推奨の理由: <ClampedText text={reason} />
        </p>
      )}
      {answerLabel !== null && (
        <p data-field="answer">
          答え: <ClampedText text={answerLabel} />
        </p>
      )}
    </div>
  );
}
