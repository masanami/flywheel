import type { ChallengeCard, StatusNeedsHuman } from "../api/types.gen.ts";
import type { CardExtra } from "../api/write.ts";
import { ClampedText } from "../cards.tsx";
import {
  ACTION_LABELS,
  DISCREPANCY_LABELS,
  MODIFIER_LABELS,
  NO_ACTION_LABEL,
  NO_PRIORITY_LABEL,
  STATE_LABELS,
  label,
} from "../labels.ts";

// 論点: 次に人間がすることごとに、その中身（問い・理由・残りの予算・食い違い）を添える。
export function pointsOf(
  c: ChallengeCard,
  nh: StatusNeedsHuman | undefined,
  question: string | undefined,
): string[] {
  const out: string[] = [];
  for (const a of c.card.next_human_actions) {
    const head = label(ACTION_LABELS, a);
    if (a === "answer_question" && question) out.push(`${head}: ${question}`);
    else if (a === "triage") {
      const t = nh?.triage.find((x) => x.challenge_id === c.id);
      out.push(t ? `${head}: ${t.reason}` : head);
    } else if (a === "increase_budget") {
      const b = nh?.budget_exhausted.find((x) => x.challenge_id === c.id);
      out.push(
        b
          ? `${head}（実装の残り $${b.impl_remaining_usd.toFixed(2)} / レビューの残り $${b.review_remaining_usd.toFixed(2)}）`
          : head,
      );
    } else if (a === "review_discrepancy") {
      const d = nh?.discrepancies.find((x) => x.challenge_id === c.id);
      out.push(
        d
          ? `${head}: ${d.kinds.map((k) => label(DISCREPANCY_LABELS, k)).join("、")}`
          : head,
      );
    } else out.push(head);
  }
  return out;
}

// S4 の課題カード 1 枚（一覧の面）。面に出すのは「一言で」（無ければタイトル）と識別子だけ。
// 位置づけ・論点・完了したら見えるもの・やらないこと・図などの分量の多い情報は詳細のモーダルに置く
// （ProposalDetailFields）。カード全体のクリック（とキーボード）で詳細を開く。
// 長い値は既定で切り詰め、「全文を開く」で広げる（M4P10）。外部由来の文字列はプレーンテキストで出す。
export function ProposalCard({
  card,
  onOpen,
}: {
  card: ChallengeCard;
  onOpen: () => void;
}) {
  const c = card.card;
  const heading = c.headline ?? card.title;
  return (
    <article
      className="proposal-card"
      data-testid={`proposal-card-${card.id}`}
      data-waiting={c.next_human_actions.length > 0 || undefined}
    >
      <header className="proposal-head">
        <h3 className="proposal-title">
          <ClampedText text={heading} />
        </h3>
        {/* 伸ばした疑似要素でカード全体を押せるようにする。中の「全文を開く」は前面に出す */}
        <button
          type="button"
          className="proposal-open"
          aria-label={`詳細を開く: ${heading}`}
          onClick={onOpen}
        />
        <div className="proposal-meta">
          <code>{card.id}</code>
          <span>{card.priority ?? NO_PRIORITY_LABEL}</span>
          <span data-state={c.state}>{label(STATE_LABELS, c.state)}</span>
          {c.modifiers.map((m) => (
            <span key={m} data-modifier={m}>
              {label(MODIFIER_LABELS, m)}
            </span>
          ))}
        </div>
      </header>
    </article>
  );
}

// 詳細のモーダルに足す S4 の欄。位置づけ・いまの状態・次に人間がすることは、モーダルの
// 4 項目（CardFields）が出すので、ここは論点・完了したら見えるもの・やらないこと・図と、
// 位置づけが未記入のときの印を出す。
export function ProposalDetailFields({
  card,
  extra,
  points,
}: {
  card: ChallengeCard;
  extra: CardExtra | null;
  points: string[];
}) {
  const c = card.card;
  const empty = (s: string) => <span className="proposal-empty">{s}</span>;
  return (
    <dl className="proposal-fields" data-testid="proposal-detail-fields">
      {c.context ? null : (
        <div>
          <dt>位置づけ</dt>
          <dd>{empty("（未記入。J1 が書くか、人が編集で直す）")}</dd>
        </div>
      )}
      <div>
        <dt>論点（次に人間がすること）</dt>
        <dd>
          {points.length === 0 ? (
            NO_ACTION_LABEL
          ) : (
            <ul>
              {points.map((p) => (
                <li key={p}>
                  <ClampedText text={p} />
                </li>
              ))}
            </ul>
          )}
        </dd>
      </div>
      <div>
        <dt>完了したら見えるもの</dt>
        <dd className="proposal-compare">
          <div data-kind="expected">
            <strong>予想</strong>
            {extra?.expected ? (
              <ClampedText text={extra.expected} />
            ) : (
              empty("（未記入）")
            )}
          </div>
          <div data-kind="actual">
            <strong>実物</strong>
            {extra?.actual ? (
              <ClampedText text={extra.actual} />
            ) : (
              empty("（完了後に入る）")
            )}
          </div>
        </dd>
      </div>
      <div>
        <dt>やらないこと</dt>
        <dd>
          {extra && extra.not_doing.length > 0 ? (
            <ul>
              {extra.not_doing.map((n) => (
                <li key={n}>
                  <ClampedText text={n} />
                </li>
              ))}
            </ul>
          ) : (
            empty("（なし）")
          )}
        </dd>
      </div>
      <div>
        <dt>図</dt>
        <dd>
          {extra?.diagram ? (
            <pre className="proposal-diagram">{extra.diagram}</pre>
          ) : (
            empty("（図なし）")
          )}
        </dd>
      </div>
    </dl>
  );
}
