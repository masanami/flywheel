import { OUTSIDE_NOTE, guidesFor } from "../guidance.ts";
import type { Challenge } from "./types.ts";

// 承認ボタンの位置に出す案内。承認は端末の CLI で行う（UI からは書き込まない）。
export function ApprovalGuide({
  challenge,
  workspacePath,
  className,
}: {
  challenge: Challenge;
  workspacePath: string;
  className?: string;
}) {
  const guides = guidesFor(
    challenge.id,
    workspacePath,
    challenge.state,
    challenge.modifiers,
  );
  if (guides.length === 0) {
    return null;
  }
  return (
    <section
      aria-label="承認方法の案内"
      className={
        className ? `approval-control ${className}` : "approval-control"
      }
    >
      <p className="approval-control-question">{OUTSIDE_NOTE}</p>
      {guides.map((g) => (
        <pre key={g.id} data-guide={g.id}>
          <code>{g.command}</code>
        </pre>
      ))}
    </section>
  );
}
