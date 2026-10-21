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

// 予想の例 1 つ。種類・場面・変更前・変更後を出す（仕様の表の各例と同じ。端末の要約も変更前を出す）。
function ExpectedBody({ e }: { e: StageExpected }) {
  return (
    <>
      予想（{e.kind}・{e.scene}）:
      <div data-field="before">
        変更前: <ClampedText text={e.before} />
      </div>
      <div data-field="after">
        変更後: <ClampedText text={e.after} />
      </div>
    </>
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
            <ExpectedBody e={e} />
          </li>
        ))}
      </ol>
    </section>
  );
}

// 予想と実物の対。core の Pairs の結果をそのまま 1 行ずつ出す（UI では位置で対を組まない）。
// 片側が null の行には印を出す。quietWhenNoActual のときは、実物がどの行にも無い間
// （計画の承認の時点。実物は完了確認で入る）だけ実物の欄に印を出さない（欠けているのではなく、まだ無いだけ）。
// 完了の承認の要約は指定せず、常に印を出す。
export function PairsView({
  pairs,
  quietWhenNoActual = false,
}: {
  pairs: StagePair[];
  quietWhenNoActual?: boolean;
}) {
  const showActualMark =
    !quietWhenNoActual || pairs.some((p) => p.actual !== null);
  return (
    <section aria-label="予想と実物" data-stage="pairs">
      <h3>予想と実物</h3>
      <ol>
        {pairs.map((p, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧
          <li key={i} data-pair={i}>
            {p.expected ? (
              <div data-field="expected">
                <ExpectedBody e={p.expected} />
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
            ) : showActualMark ? (
              <div data-field="actual" data-mark="actual-missing">
                {ACTUAL_MISSING_MARK}
              </div>
            ) : null}
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
      {pairs.length > 0 && <PairsView pairs={pairs} quietWhenNoActual />}
      {non_goals !== null && <NonGoalsView nonGoals={non_goals} />}
    </section>
  );
}
