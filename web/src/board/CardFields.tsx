import { cardFieldItems } from "./card-fields.ts";
import type { Challenge } from "./types.ts";

// カードの面の 4 項目（一言で・位置づけ・いまの状態・次に人間がすること）。値が無い項目は出さない。
export function CardFields({
  challenge,
  className,
  testId,
}: {
  challenge: Challenge;
  className: string;
  testId: string;
}) {
  const items = cardFieldItems(challenge);
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
