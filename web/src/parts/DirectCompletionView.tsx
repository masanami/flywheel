import type { DirectCompletion } from "../api/types.gen.ts";

// 親が直接行った作業の成果物の参照（URL・PR・Issue コメントなど。run を介さない）をリンクとして出す。
// 参照は外部の文字列なので、http(s) の URL だけをリンクにする（javascript: などは文字のまま出す）。
export function artifactHref(ref: string): string | null {
  try {
    const u = new URL(ref);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : null;
  } catch {
    return null;
  }
}

export const DIRECT_COMPLETION_HEADING = "親が直接行った成果物";
export const DIRECT_COMPLETION_NOTE =
  "対話のセッションの親が直接行った作業です。完了の検証（自動の確認）は通っていません。成果物と完了条件を見て判断してください。";

export function ArtifactList({ artifacts }: { artifacts: string[] }) {
  return (
    <ul className="direct-completion-artifacts">
      {artifacts.map((ref, i) => {
        const href = artifactHref(ref);
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: 同じ参照が並びうる固定の一覧
          <li key={i}>
            {href ? (
              <a href={href} target="_blank" rel="noopener noreferrer">
                {ref}
              </a>
            ) : (
              <span>{ref}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

// 承認の要約では完了条件の次に、課題の詳細では記録ごとに出す。
export function DirectCompletionView({
  completion,
  showNote = false,
}: {
  completion: DirectCompletion;
  showNote?: boolean;
}) {
  return (
    <div data-testid="direct-completion">
      <h4>{DIRECT_COMPLETION_HEADING}</h4>
      {showNote ? <p>{DIRECT_COMPLETION_NOTE}</p> : null}
      <ArtifactList artifacts={completion.artifacts} />
      {completion.note ? (
        <p className="card-detail-multiline">説明: {completion.note}</p>
      ) : null}
    </div>
  );
}
