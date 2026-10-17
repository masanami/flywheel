import { useRef, useState } from "react";
import { MARK_LABELS, NO_PRIORITY_LABEL } from "../labels.ts";
import { CardDetailModal } from "./CardDetailModal.tsx";
import { faceHeadline } from "./marks.ts";
import type { Challenge } from "./types.ts";

type TaskCardProps = {
  challenge: Challenge;
  agentName: string;
  workspacePath: string;
};

// カードの面は、見出し・課題の ID・優先度・区分の印だけ（M4H23）。説明・状態・次の操作は詳細に置く。
export function TaskCard({
  challenge,
  agentName,
  workspacePath,
}: TaskCardProps) {
  const [isModalOpen, setIsModalOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);

  const closeModal = () => {
    setIsModalOpen(false);
    triggerRef.current?.focus();
  };

  return (
    <>
      <div
        className="task-card"
        data-testid={`card-${challenge.id}`}
        data-needs-human={challenge.needsHuman || undefined}
      >
        <button
          ref={triggerRef}
          type="button"
          className="task-card-body"
          onClick={() => setIsModalOpen(true)}
        >
          <div className="task-card-title" data-field="headline">
            {faceHeadline(challenge.headline ?? challenge.title)}
          </div>
          <div className="task-card-meta">
            <span className="task-card-id">{challenge.id}</span>
            <span className="task-card-position" data-field="priority">
              {challenge.priority ?? NO_PRIORITY_LABEL}
            </span>
            {challenge.marks.map((m) => (
              <span key={m} className="task-card-position" data-mark={m}>
                {MARK_LABELS[m]}
              </span>
            ))}
          </div>
        </button>
      </div>
      {isModalOpen && (
        <CardDetailModal
          challenge={challenge}
          agentName={agentName}
          workspacePath={workspacePath}
          onClose={closeModal}
        />
      )}
    </>
  );
}
