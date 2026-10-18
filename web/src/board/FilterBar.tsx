export type BoardFilter = "all" | "needsHuman";

type FilterBarProps = {
  value: BoardFilter;
  onChange: (value: BoardFilter) => void;
  showCompleted: boolean;
  onShowCompletedChange: (value: boolean) => void;
};

const FILTERS: Array<{ value: BoardFilter; label: string }> = [
  { value: "all", label: "すべて" },
  { value: "needsHuman", label: "🔔 人間の操作待ち" },
];

// 新しい API に対応する機能が無い操作（アーカイブ表示・エージェント追加）は、見た目はそのまま押せない状態にする。
const NOT_AVAILABLE = "この画面では使えません（読み取り専用）";

export function FilterBar({
  value,
  onChange,
  showCompleted,
  onShowCompletedChange,
}: FilterBarProps) {
  return (
    <div className="filter-bar">
      {FILTERS.map((filter) => (
        <button
          key={filter.value}
          type="button"
          className="filter-chip"
          aria-pressed={filter.value === value}
          onClick={() => onChange(filter.value)}
        >
          {filter.label}
        </button>
      ))}
      <button
        type="button"
        className="filter-chip filter-toggle"
        aria-pressed={showCompleted}
        disabled={value === "needsHuman"}
        onClick={() => onShowCompletedChange(!showCompleted)}
      >
        完了を表示
      </button>
      <button
        type="button"
        className="filter-chip filter-archive-toggle"
        aria-pressed={false}
        disabled
        title={NOT_AVAILABLE}
      >
        🗄 アーカイブ表示
      </button>
      <button
        type="button"
        className="add-agent-button"
        disabled
        title={NOT_AVAILABLE}
      >
        ＋ エージェント追加
      </button>
    </div>
  );
}
