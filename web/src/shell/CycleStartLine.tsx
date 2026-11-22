import { cycleStartNote } from "../guidance.ts";

// 承認の後に、サイクルを始めたか（cycle.reason）を 1 行で出す部品。計画の承認と完了の承認の画面で共通に使う。
export function CycleStartLine({ reason }: { reason: string }) {
  return (
    <p
      className="cycle-start-line"
      data-testid="cycle-start-line"
      data-reason={reason}
    >
      {cycleStartNote(reason)}
    </p>
  );
}
