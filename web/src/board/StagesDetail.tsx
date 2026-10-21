import type {
  StageExpected,
  StageIssue,
  StagePair,
  Stages,
} from "../api/types.gen.ts";
import { ClampedText } from "./ClampedText.tsx";
import { ACTUAL_MISSING_MARK, EXPECTED_MISSING_MARK } from "./stage-marks.ts";

// 論点（問い・選択肢・推奨の印）。読み取り専用。
export function IssuesView({ issues }: { issues: StageIssue[] }) {
  return (
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
  );
}

export function IssuesAnswerView({ answer }: { answer: string }) {
  return (
    <section aria-label="論点の決定" data-stage="issues-answer">
      <h3>論点の決定</h3>
      <ClampedText text={answer} />
    </section>
  );
}

// 予想（完了したら見えるもの）の一覧。計画の承認の要約で使う。
export function ExpectedView({ expected }: { expected: StageExpected[] }) {
  return (
    <section aria-label="予想" data-stage="expected">
      <h3>予想</h3>
      <ol>
        {expected.map((e, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
          <li key={i} data-field="expected">
            予想（{e.scene}）: <ClampedText text={e.after} />
          </li>
        ))}
      </ol>
    </section>
  );
}

// 予想と実物の対。core の Pairs の結果をそのまま 1 行ずつ出す（UI では位置で対を組まない）。
// 片側が null の行には印を出す。
export function PairsView({ pairs }: { pairs: StagePair[] }) {
  return (
    <section aria-label="予想と実物" data-stage="pairs">
      <h3>予想と実物</h3>
      <ol>
        {pairs.map((p, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
          <li key={i} data-pair={i}>
            {p.expected ? (
              <div data-field="expected">
                予想（{p.expected.scene}）:{" "}
                <ClampedText text={p.expected.after} />
              </div>
            ) : (
              <div data-field="expected" data-mark="expected-missing">
                {EXPECTED_MISSING_MARK}
              </div>
            )}
            {p.actual ? (
              <div data-field="actual">
                実物（{p.actual.scene}）: <ClampedText text={p.actual.after} />
              </div>
            ) : (
              <div data-field="actual" data-mark="actual-missing">
                {ACTUAL_MISSING_MARK}
              </div>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}

export function NonGoalsView({ nonGoals }: { nonGoals: string[] }) {
  return (
    <section aria-label="やらないこと" data-stage="non-goals">
      <h3>やらないこと</h3>
      <ul>
        {nonGoals.map((g, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
          <li key={i}>
            <ClampedText text={g} />
          </li>
        ))}
      </ul>
    </section>
  );
}

// 段階の欄（論点・論点の決定・予想と実物・やらないこと）。すべて null なら何も出さない（欄の見出しも出さない）。
// recommended は選択肢の 1 から数えた位置。figure はここでは描かない（判定にも入れない。描くのは FigureView）。
// 読み取り専用。選択肢を選ぶボタンも入力欄も置かない（回答は `flywheel answer`）。値はプレーンテキストで出す。
export function StagesDetail({
  stages,
  pairs,
}: {
  stages: Stages;
  pairs: StagePair[];
}) {
  const { issues, issues_answer, non_goals } = stages;
  if (
    issues === null &&
    issues_answer === null &&
    pairs.length === 0 &&
    non_goals === null
  ) {
    return null;
  }
  return (
    <section className="card-detail-stages" data-testid="stages-detail">
      {issues !== null && <IssuesView issues={issues} />}
      {issues_answer !== null && <IssuesAnswerView answer={issues_answer} />}
      {pairs.length > 0 && <PairsView pairs={pairs} />}
      {non_goals !== null && <NonGoalsView nonGoals={non_goals} />}
    </section>
  );
}
