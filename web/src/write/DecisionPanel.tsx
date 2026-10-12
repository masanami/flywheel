import { useState } from "react";
import type {
  ChallengeCard,
  Hold,
  StatusNeedsHuman,
} from "../api/types.gen.ts";

type HoldQuestion = Pick<Hold, "question">;
import { ClampedText } from "../cards.tsx";
import { DecisionAction } from "./DecisionAction.tsx";

function Row({
  c,
  children,
}: {
  c: ChallengeCard;
  children: React.ReactNode;
}) {
  return (
    <li className="decision-row" data-testid={`decision-${c.id}`}>
      <div>
        <code>{c.id}</code> <ClampedText text={c.title} />
      </div>
      {children}
    </li>
  );
}

function RejectRow({ workspace, c, onDone }: Props & { c: ChallengeCard }) {
  const [reason, setReason] = useState("");
  return (
    <Row c={c}>
      <label className="field">
        <span className="field-label">差し戻しの理由</span>
        <textarea
          rows={2}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
      </label>
      <DecisionAction
        workspace={workspace}
        label="差し戻す…"
        request={{ action: "reject", challenge_id: c.id, reason }}
        onDone={onDone}
      />
    </Row>
  );
}

function AnswerRow({
  workspace,
  c,
  hold,
  onDone,
}: Props & { c: ChallengeCard; hold: HoldQuestion | undefined }) {
  const [answer, setAnswer] = useState("");
  return (
    <Row c={c}>
      <p className="hold-question">
        問い:{" "}
        <ClampedText text={hold?.question ?? "（問いを取得できません）"} />
      </p>
      <label className="field">
        <span className="field-label">回答</span>
        <textarea
          rows={2}
          value={answer}
          onChange={(e) => setAnswer(e.target.value)}
        />
      </label>
      <DecisionAction
        workspace={workspace}
        label="回答する…"
        request={{ action: "answer", challenge_id: c.id, answer }}
        onDone={onDone}
      />
    </Row>
  );
}

function BudgetRow({
  workspace,
  c,
  remaining,
  onDone,
}: Props & { c: ChallengeCard; remaining: string }) {
  const [amount, setAmount] = useState("5");
  return (
    <Row c={c}>
      <p className="form-meta">{remaining}</p>
      <label className="field">
        <span className="field-label">増額する額（ドル）</span>
        <input
          type="number"
          min="0"
          step="0.5"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
      </label>
      <DecisionAction
        workspace={workspace}
        label="増額する…"
        request={{
          action: "increase_budget",
          challenge_id: c.id,
          amount_usd: Number(amount),
        }}
        onDone={onDone}
      />
    </Row>
  );
}

interface Props {
  workspace: string;
  onDone: (message: string) => void;
}

// 差し戻し（承認待ちの課題）・保留への回答（人間対応待ち）・予算の増額（使い切り）。1 件ずつ。
export function DecisionPanel({
  workspace,
  cards,
  needsHuman,
  holds,
  onDone,
}: Props & {
  cards: ChallengeCard[];
  needsHuman: StatusNeedsHuman | undefined;
  // 課題 ID → 未回答の保留
  holds: Record<string, HoldQuestion>;
}) {
  const rejectable = cards.filter(
    (c) =>
      c.status === "awaiting_plan_approval" ||
      c.status === "awaiting_completion_approval",
  );
  const askable = cards.filter((c) => c.status === "awaiting_human");
  const budget = needsHuman?.budget_exhausted ?? [];
  const byId = new Map(cards.map((c) => [c.id, c]));
  return (
    <div className="decision-panel">
      <section aria-label="差し戻し">
        <h3>差し戻し（{rejectable.length}）</h3>
        {rejectable.length === 0 ? <p>なし</p> : null}
        <ul>
          {rejectable.map((c) => (
            <RejectRow key={c.id} c={c} workspace={workspace} onDone={onDone} />
          ))}
        </ul>
      </section>
      <section aria-label="保留への回答">
        <h3>保留への回答（{askable.length}）</h3>
        {askable.length === 0 ? <p>なし</p> : null}
        <ul>
          {askable.map((c) => (
            <AnswerRow
              key={c.id}
              c={c}
              hold={holds[c.id]}
              workspace={workspace}
              onDone={onDone}
            />
          ))}
        </ul>
      </section>
      <section aria-label="予算の増額">
        <h3>予算の増額（{budget.length}）</h3>
        {budget.length === 0 ? <p>なし</p> : null}
        <ul>
          {budget.map((b) => {
            const c = byId.get(b.challenge_id);
            return c ? (
              <BudgetRow
                key={b.challenge_id}
                c={c}
                workspace={workspace}
                onDone={onDone}
                remaining={`実装の残り $${b.impl_remaining_usd.toFixed(2)} / レビューの残り $${b.review_remaining_usd.toFixed(2)}`}
              />
            ) : null;
          })}
        </ul>
      </section>
    </div>
  );
}
