import { Fragment } from "react";
import type { PassedConflictFix } from "../api/types.gen.ts";
import {
  CONFLICT_FIX_HEADING,
  CONFLICT_FIX_TARGET_LABELS,
  label,
} from "../labels.ts";
import { shortTime } from "../when.ts";

// 通した衝突の解消の一覧。0 件なら区画ごと出さない。直したファイルの名前は解釈せず、文字のまま出す。
// 統合先宛てはタスクの題名を添える（titleOfTask が題名を返せなければ、タスクの ID を出す）。
export function ConflictFixesView({
  fixes,
  titleOfTask,
  showAt,
}: {
  fixes: PassedConflictFix[];
  titleOfTask: (taskId: string) => string | undefined;
  showAt: boolean;
}) {
  if (fixes.length === 0) return null;
  return (
    <section aria-label={CONFLICT_FIX_HEADING} data-testid="conflict-fixes">
      <h4>
        {CONFLICT_FIX_HEADING}（
        <span data-testid="conflict-fixes-count">{fixes.length}</span> 件）
      </h4>
      <ul>
        {fixes.map((f, n) => (
          // run_id が読めない記録は空文字になりうるので、並びの位置を足す
          <li key={`${n}-${f.run_id}`} data-testid="conflict-fix">
            {showAt ? (
              <>
                <time data-testid="conflict-fix-at" dateTime={f.at}>
                  {shortTime(f.at)}
                </time>
                {" ・ "}
              </>
            ) : null}
            <span data-testid="conflict-fix-target" data-target={f.target}>
              {label(CONFLICT_FIX_TARGET_LABELS, f.target)}
              {f.task_id !== null
                ? `（${titleOfTask(f.task_id) ?? f.task_id}）`
                : ""}
            </span>
            {f.files.length > 0 ? " ・ " : null}
            <span data-testid="conflict-fix-files">
              {f.files.map((file, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: 同じ名前が並びうる
                <Fragment key={i}>
                  {i > 0 ? " " : null}
                  <code>{file}</code>
                </Fragment>
              ))}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
