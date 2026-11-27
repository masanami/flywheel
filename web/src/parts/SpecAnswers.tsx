import type { SpecDraftAnswer } from "../api/types.gen.ts";
import { SPEC_ANSWERER_LABELS, label } from "../labels.ts";
import { ClampedText } from "./ClampedText.tsx";

const NOTE_LABEL = "添え書き";

// 下書きづくりの答えと添え書きの一覧（表示だけ）。1 件が 1 つの項目。
// showBy を立てると、答えた者の語を出す（添え書きには出さない）。
export function SpecAnswerList({
  answers,
  showBy = false,
}: {
  answers: SpecDraftAnswer[];
  showBy?: boolean;
}) {
  return (
    <ul className="spec-answer-list">
      {answers.map((a, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
        <li key={i}>
          {a.kind === "note" ? (
            <p>
              {NOTE_LABEL}: <ClampedText text={a.answer} />
            </p>
          ) : (
            <>
              <p>
                問い: <ClampedText text={a.question} />
              </p>
              <p>
                答え: <ClampedText text={a.answer} />
              </p>
              {showBy && <p>答えた者: {label(SPEC_ANSWERER_LABELS, a.by)}</p>}
              {a.basis.map((b, j) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
                <p key={j}>
                  根拠: {b.path}
                  {b.locator !== "" && `（${b.locator}）`}
                </p>
              ))}
            </>
          )}
        </li>
      ))}
    </ul>
  );
}
