import { type FieldScope, cardFieldItems } from "./card-fields.ts";
import type { Challenge } from "./types.ts";

// カードの 4 項目（一言で・位置づけ・いまの状態・次に人間がすること）。値が無い項目は出さない。
// 一覧の面（scope="face"）には「一言で」だけを出し、詳細（scope="all"）に 4 項目をそろえる。
export function CardFields({
  challenge,
  className,
  testId,
  scope = "all",
}: {
  challenge: Challenge;
  className: string;
  testId: string;
  scope?: FieldScope;
}) {
  const items = cardFieldItems(challenge, scope);
  if (items.length === 0) {
    return null;
  }
  return (
    <dl className={className} data-testid={testId}>
      {items.map(([label, value]) => (
        <div key={label} className={`${className}-item`}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
