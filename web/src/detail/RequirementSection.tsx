import type { Requirement } from "../api/types.gen.ts";
import { REQUIREMENT_ORIGIN_LABELS, STATE_LABELS, label } from "../labels.ts";
import { safeHref } from "../safeHref.ts";
import { hrefOf } from "../shell/route.ts";

// 区画「要求」: 出どころと、同じ要求の課題の一覧。出すかは呼び出し側が決める
// （core が requirement を返すのは、課題が要求の記録を指すか、取り下げた元の課題のときだけ）。
export function RequirementSection({
  requirement,
  agentName,
}: {
  requirement: Requirement;
  agentName: string;
}) {
  return (
    <section aria-label="要求" data-testid="requirement-section">
      <h3>要求</h3>
      <p>出どころ: {originOf(requirement)}</p>
      <p>同じ要求の課題</p>
      <ul>
        {requirement.challenges.map((c) => (
          <li key={c.id} data-testid="requirement-challenge">
            <a
              href={hrefOf({
                kind: "challenge",
                name: agentName,
                id: c.id,
                view: "tree",
              })}
            >
              {c.title}
            </a>{" "}
            <span className="status-pill">{label(STATE_LABELS, c.status)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

// 元の Issue は URL のリンク（http・https でない URL は文字で出す）。ほかの出どころは語で出す。
function originOf({ origin, url }: Requirement) {
  if (origin !== "issue" || url === null) {
    return label(REQUIREMENT_ORIGIN_LABELS, origin);
  }
  const href = safeHref(url);
  return (
    <>
      {label(REQUIREMENT_ORIGIN_LABELS, origin)}{" "}
      {href === null ? (
        url
      ) : (
        <a href={href} target="_blank" rel="noopener noreferrer">
          {url}
        </a>
      )}
    </>
  );
}
