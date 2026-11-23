import {
  CONVERSATION_NOTE,
  OUTSIDE_NOTE,
  guidesFor,
  showsConversationNote,
} from "../guidance.ts";

// 詳細の操作の案内。ボタンで承認できない操作にはターミナルのコマンドを示し、
// 対話で伝えれば LLM が記録できる操作にはその旨を添える。
export function ApprovalGuide({
  challengeId,
  actions,
  workspacePath,
  className,
  openHoldIds,
  needsHold,
}: {
  challengeId: string;
  actions: string[];
  workspacePath: string;
  className?: string;
  // 開いている保留の ID（H-<番号>）。2 つ以上のときは、回答の案内が保留ごとに --hold つきになる。
  openHoldIds?: string[];
  // 回答の相手の保留を --hold で指定するか（省略は openHoldIds が 2 つ以上のとき）。
  needsHold?: boolean;
}) {
  const guides = guidesFor(
    challengeId,
    workspacePath,
    actions,
    openHoldIds,
    needsHold,
  );
  const conversation = showsConversationNote(actions);
  if (guides.length === 0 && !conversation) {
    return null;
  }
  return (
    <section
      aria-label="承認方法の案内"
      className={
        className ? `approval-control ${className}` : "approval-control"
      }
    >
      {guides.length > 0 && (
        <p className="approval-control-question">{OUTSIDE_NOTE}</p>
      )}
      {guides.map((g) => (
        <div key={g.id} className="approval-guide-item">
          {g.note && <p className="approval-control-question">{g.note}</p>}
          <pre data-guide={g.id}>
            <code>{g.command}</code>
          </pre>
        </div>
      ))}
      {conversation && (
        <p
          className="approval-control-question"
          data-testid="conversation-note"
        >
          {CONVERSATION_NOTE}
        </p>
      )}
    </section>
  );
}
