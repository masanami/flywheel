import type { SliceSplit } from "../api/types.gen.ts";

// 区切りを分ける保留の所に足す中身: 案の区切りの一覧・分ける理由・証跡の場所（文字で出す）。表示だけで、
// 選ぶのは保留の選択肢のボタン。どの保留に出すかは呼び出し側が決める（split.hold_id と同じ ID の保留）。
export function SliceSplitHold({ split }: { split: SliceSplit }) {
  return (
    <div data-testid="slice-split-proposal">
      <p>区切りの案</p>
      {split.slices.map((s, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: 固定の一覧（番号が区切りの識別）
        <div key={i} data-testid="slice-split-slice">
          <p>
            <b>
              {i + 1}. {s.title}
            </b>
          </p>
          <p>要旨: {s.summary}</p>
          <p>リポジトリ: {s.repo}</p>
          <p>
            前に済ませる区切り:{" "}
            {s.depends_on.length > 0 ? s.depends_on.join(", ") : "なし"}
          </p>
        </div>
      ))}
      {split.reason !== null && <p>分ける理由: {split.reason}</p>}
      {split.evidence_path !== null && <p>証跡の場所: {split.evidence_path}</p>}
    </div>
  );
}
