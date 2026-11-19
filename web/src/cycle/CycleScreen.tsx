import {
  CYCLE_RESULTS,
  CYCLE_RESULT_LABELS,
  type CycleResult,
  cycleStateText,
} from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { LOADING_TEXT } from "../shell/Shell.tsx";
import { CycleWorkPart } from "./CycleWorkPart.tsx";

export const NO_TIME_TEXT = "まだありません";

export function Time({ iso }: { iso: string | null }) {
  if (iso === null) return <>{NO_TIME_TEXT}</>;
  const d = new Date(iso);
  return (
    <time dateTime={iso}>
      {Number.isNaN(d.getTime()) ? iso : d.toLocaleString()}
    </time>
  );
}

export function cycleResultText(result: string | null): string {
  if (result === null) return NO_TIME_TEXT;
  return CYCLE_RESULTS.some((r) => r === result)
    ? CYCLE_RESULT_LABELS[result as CycleResult]
    : result;
}

// サイクルの画面。いまの API が返す状態と、サイクルを回すと進むものだけ。止めるボタンは置かない。
export function CycleScreen({
  live,
  agentName,
}: {
  live: LiveState;
  agentName: string;
}) {
  const data = live.data[agentName];
  const cycle = data?.cycle;
  return (
    <section aria-labelledby="screen-title">
      <h2 id="screen-title">サイクル</h2>
      {cycle ? (
        <dl className="cycle-facts">
          <div>
            <dt>状態</dt>
            <dd data-testid="cycle-screen-state">
              {cycleStateText(cycle.state)}
            </dd>
          </div>
          <div>
            <dt>最後の始まり</dt>
            <dd data-testid="cycle-last-started">
              <Time iso={cycle.last_started_at} />
            </dd>
          </div>
          <div>
            <dt>最後の終わり</dt>
            <dd data-testid="cycle-last-ended">
              <Time iso={cycle.last_ended_at} />
            </dd>
          </div>
          <div>
            <dt>最後の結果</dt>
            <dd data-testid="cycle-last-result">
              {cycleResultText(cycle.last_result)}
            </dd>
          </div>
        </dl>
      ) : (
        <p>{data ? "サイクルの状態を読めませんでした。" : LOADING_TEXT}</p>
      )}
      <CycleWorkPart
        workspace={agentName}
        work={data?.status?.cycle_work}
        cycleState={cycle?.state ?? ""}
      />
    </section>
  );
}
