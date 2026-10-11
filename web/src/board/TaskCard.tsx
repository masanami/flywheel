import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { MODIFIER_LABELS, NO_PRIORITY_LABEL, label } from "../labels.ts";
import { ApprovalGuide } from "./ApprovalGuide.tsx";
import { CardDetailModal } from "./CardDetailModal.tsx";
import { CardFields } from "./CardFields.tsx";
import type { Challenge } from "./types.ts";

type TaskCardProps = {
  challenge: Challenge;
  agentName: string;
  workspacePath: string;
};

const TOOLTIP_GAP = 6;
const TOOLTIP_ESTIMATED_HEIGHT = 72;

type TooltipPosition =
  | { left: number; top: number; bottom?: undefined }
  | { left: number; bottom: number; top?: undefined };

// ツールチップは document.body へ portal で出すため、カードの位置から fixed 座標を求める。
function computeTooltipPosition(card: HTMLElement): TooltipPosition {
  const rect = card.getBoundingClientRect();
  const left = rect.left + 10;
  const columnBody = card.closest(".agent-column-body");
  const boundaryTop = columnBody ? columnBody.getBoundingClientRect().top : 0;
  if (rect.top - TOOLTIP_GAP - TOOLTIP_ESTIMATED_HEIGHT >= boundaryTop) {
    return { left, bottom: window.innerHeight - rect.top + TOOLTIP_GAP };
  }
  return { left, top: rect.bottom + TOOLTIP_GAP };
}

export function TaskCard({
  challenge,
  agentName,
  workspacePath,
}: TaskCardProps) {
  const [isTooltipVisible, setIsTooltipVisible] = useState(false);
  const [isModalOpen, setIsModalOpen] = useState(false);
  const cardRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const tooltipId = useId();
  const [tooltipPosition, setTooltipPosition] =
    useState<TooltipPosition | null>(null);

  const updateTooltipPosition = useCallback(() => {
    if (cardRef.current) {
      setTooltipPosition(computeTooltipPosition(cardRef.current));
    }
  }, []);

  const showTooltip = () => {
    if (challenge.summary) {
      updateTooltipPosition();
      setIsTooltipVisible(true);
    }
  };
  const hideTooltip = () => setIsTooltipVisible(false);

  const openModal = () => setIsModalOpen(true);
  const closeModal = () => {
    setIsModalOpen(false);
    triggerRef.current?.focus();
  };

  const tooltipVisible = isTooltipVisible && Boolean(challenge.summary);

  useEffect(() => {
    if (!tooltipVisible) {
      return;
    }
    window.addEventListener("scroll", updateTooltipPosition, true);
    window.addEventListener("resize", updateTooltipPosition);
    return () => {
      window.removeEventListener("scroll", updateTooltipPosition, true);
      window.removeEventListener("resize", updateTooltipPosition);
    };
  }, [tooltipVisible, updateTooltipPosition]);

  return (
    <>
      <div
        ref={cardRef}
        className="task-card"
        data-testid={`card-${challenge.id}`}
        data-needs-human={challenge.needsHuman || undefined}
      >
        <button
          ref={triggerRef}
          type="button"
          className="task-card-body"
          aria-describedby={tooltipVisible ? tooltipId : undefined}
          onMouseEnter={showTooltip}
          onMouseLeave={hideTooltip}
          onFocus={showTooltip}
          onBlur={hideTooltip}
          onClick={openModal}
        >
          <div className="task-card-title">{challenge.title}</div>
          <CardFields
            challenge={challenge}
            className="task-card-fields"
            testId="task-card-fields"
          />
          <div className="task-card-meta">
            <span className="status-dot" data-status={challenge.status} />
            <span className="task-card-id">{challenge.id}</span>
            <span className="task-card-position" data-field="priority">
              {challenge.priority ?? NO_PRIORITY_LABEL}
            </span>
            <span className="task-card-status">{challenge.status}</span>
            {challenge.modifiers.map((m) => (
              <span key={m} className="task-card-position" data-modifier={m}>
                {label(MODIFIER_LABELS, m)}
              </span>
            ))}
          </div>
        </button>
        <ApprovalGuide
          challenge={challenge}
          workspacePath={workspacePath}
          className="task-card-approval"
        />
      </div>
      {tooltipVisible &&
        tooltipPosition &&
        createPortal(
          <div
            id={tooltipId}
            className="task-card-tooltip"
            role="tooltip"
            style={tooltipPosition}
          >
            {challenge.summary}
          </div>,
          document.body,
        )}
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
