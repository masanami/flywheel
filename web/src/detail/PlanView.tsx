import { useState } from "react";
import type { ShowCardResponse } from "../api/types.gen.ts";
import { ClampedText } from "../board/ClampedText.tsx";
import { FigureView } from "../board/FigureView.tsx";
import { StagesDetail } from "../board/StagesDetail.tsx";
import { Markdown } from "../markdown/Markdown.tsx";

// 計画の本文は長いので、畳むときの行数を多めにする。
const PLAN_LINES = 12;

function Body({ text, lines }: { text: string; lines?: number }) {
  return (
    <ClampedText resetKey={text} lines={lines}>
      <Markdown text={text} />
    </ClampedText>
  );
}

// 「計画の本文」の中身。計画の本文（最新の版が開いた直後。版を選べる）・説明・完了条件・段階の欄・図。
// 計画が無い課題でも、説明と完了条件は出す。
export function PlanView({ show }: { show: ShowCardResponse }) {
  const plans = [...show.plans].sort((a, b) => a.version - b.version);
  const latest = plans.at(-1)?.version ?? null;
  const [picked, setPicked] = useState<number | null>(null);
  const version =
    picked !== null && plans.some((p) => p.version === picked)
      ? picked
      : latest;
  const plan = plans.find((p) => p.version === version);
  const { description, done_criteria } = show.challenge;
  return (
    <div className="detail-plan" data-testid="detail-plan-view">
      {plan ? (
        <section aria-label="計画の本文" data-testid="plan-body">
          <h3>計画の本文</h3>
          {plans.length > 1 ? (
            <div className="plan-versions" aria-label="計画の版">
              {plans.map((p) => (
                <button
                  key={p.version}
                  type="button"
                  aria-pressed={p.version === version}
                  onClick={() => setPicked(p.version)}
                >
                  v{p.version}
                </button>
              ))}
            </div>
          ) : null}
          <p className="plan-meta">
            v{plan.version}（{plan.created_at}）
          </p>
          <Body text={plan.body} lines={PLAN_LINES} />
        </section>
      ) : null}
      <section aria-label="説明" data-testid="detail-description">
        <h3>説明</h3>
        {description === "" ? <p>なし</p> : <Body text={description} />}
      </section>
      <section aria-label="完了条件" data-testid="detail-done-criteria">
        <h3>完了条件</h3>
        {done_criteria === "" ? <p>なし</p> : <Body text={done_criteria} />}
      </section>
      <StagesDetail stages={show.stages} pairs={show.pairs} />
      <FigureView figure={show.stages.figure} />
    </div>
  );
}
