import type { Stages } from "../api/types.gen.ts";
import { ClampedText } from "./ClampedText.tsx";

// 段階の欄（論点・論点の決定・予想と実物・やらないこと）。すべて null なら何も出さない（欄の見出しも出さない）。
// recommended は選択肢の 1 から数えた位置。figure はここでは描かない（判定にも入れない）。
// 読み取り専用。選択肢を選ぶボタンも入力欄も置かない（回答は `flywheel answer`）。値はプレーンテキストで出す。
export function StagesDetail({ stages }: { stages: Stages }) {
  const { issues, issues_answer, expected, actual, non_goals } = stages;
  if (
    issues === null &&
    issues_answer === null &&
    expected === null &&
    actual === null &&
    non_goals === null
  ) {
    return null;
  }
  const pairCount = Math.max(expected?.length ?? 0, actual?.length ?? 0);
  return (
    <section className="card-detail-stages" data-testid="stages-detail">
      {issues !== null && (
        <section aria-label="論点" data-stage="issues">
          <h3>論点</h3>
          <ol>
            {issues.map((issue, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
              <li key={i}>
                <div data-field="question">
                  <ClampedText text={issue.question} />
                </div>
                <ul>
                  {issue.options.map((o, j) => (
                    // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
                    <li key={j} data-recommended={j + 1 === issue.recommended}>
                      <ClampedText text={o.label} />
                      {j + 1 === issue.recommended ? "（推奨）" : ""}
                      {o.example !== null ? (
                        <div>
                          例: <ClampedText text={o.example} />
                        </div>
                      ) : null}
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ol>
        </section>
      )}
      {issues_answer !== null && (
        <section aria-label="論点の決定" data-stage="issues-answer">
          <h3>論点の決定</h3>
          <ClampedText text={issues_answer} />
        </section>
      )}
      {pairCount > 0 && (
        <section aria-label="予想と実物" data-stage="pairs">
          <h3>予想と実物</h3>
          <ol>
            {Array.from({ length: pairCount }, (_, i) => {
              const e = expected?.[i];
              const a = actual?.[i];
              return (
                // biome-ignore lint/suspicious/noArrayIndexKey: 位置で対にする固定の一覧
                <li key={i} data-pair={i}>
                  {e ? (
                    <div data-field="expected">
                      予想（{e.scene}）: <ClampedText text={e.after} />
                    </div>
                  ) : null}
                  {a ? (
                    <div data-field="actual">
                      実物（{a.scene}）: <ClampedText text={a.after} />
                    </div>
                  ) : null}
                </li>
              );
            })}
          </ol>
        </section>
      )}
      {non_goals !== null && (
        <section aria-label="やらないこと" data-stage="non-goals">
          <h3>やらないこと</h3>
          <ul>
            {non_goals.map((g, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
              <li key={i}>
                <ClampedText text={g} />
              </li>
            ))}
          </ul>
        </section>
      )}
    </section>
  );
}
