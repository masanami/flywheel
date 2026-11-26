import type { SpecDraft, SpecDraftQuestion } from "../api/types.gen.ts";
import { guidesForContext } from "../guidance.ts";
import { PlanQuestionView } from "../parts/PlanQuestionView.tsx";

// 保留の理由の値（core の閉集合のうち、案内を出し分ける値）。
const HOLD_REASON_QUESTION = "spec_question";

export const SPEC_ANSWER_GUIDE_TEXT =
  "推奨と違う答えにしたいときは、Claude Code との対話で伝えるか、ターミナルで次のコマンドを実行します。";

// 下書きづくりの保留の所に足す中身: 下書きの場所・残った問い（表示だけ）・推奨と違う答えの案内。
// どの保留に出すかは呼び出し側が決める（draft.hold_id と同じ ID の保留）。選ぶのは、保留の選択肢のボタンだけ。
export function SpecDraftHold({
  draft,
  challengeId,
  workspacePath,
}: {
  draft: SpecDraft;
  challengeId: string;
  workspacePath: string;
}) {
  const guide =
    draft.hold_reason === HOLD_REASON_QUESTION
      ? guidesForContext(HOLD_REASON_QUESTION, workspacePath).find(
          (g) => g.id === "answer-spec-choice",
        )
      : undefined;
  return (
    <>
      <p>下書き: {draft.path}</p>
      {draft.questions.length > 0 && (
        <div data-testid="spec-questions">
          {draft.questions.map((s, i) => (
            <SpecQuestion
              // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
              key={i}
              number={i + 1}
              spec={s}
            />
          ))}
        </div>
      )}
      {guide && (
        <div data-testid="spec-answer-guide">
          <p>{SPEC_ANSWER_GUIDE_TEXT}</p>
          <pre data-guide={guide.id}>
            <code>{guide.command.replaceAll("<C-ID>", () => challengeId)}</code>
          </pre>
        </div>
      )}
    </>
  );
}

// 下書きの問いを、計画の論点の問いの部品の形にする。番号は記録の並びの順（保留の問いの文の Q1 からと同じ）。
function SpecQuestion({
  number,
  spec,
}: {
  number: number;
  spec: SpecDraftQuestion;
}) {
  return (
    <PlanQuestionView
      q={{
        answer: null,
        number,
        options: spec.options.map((o) => ({ example: null, label: o.label })),
        question: spec.question,
        recommended: spec.recommended,
      }}
      recommendedMark="（推奨）"
      reason={spec.reason}
    />
  );
}
