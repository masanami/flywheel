// リポジトリの絞り込み（2026-10-17 オーナー: リポジトリを多数持つエージェント・名前の長いリポジトリがあるため、
// 並べた札を押す形をやめ、キーワードを入れて候補から選ぶ形にする）。選んだものは札として並べ、×で外せる。
// 入力欄は WAI-ARIA の combobox（候補は listbox）で、上下の矢印で候補を移り、Enter で足し、Esc で候補を閉じる。

import { type KeyboardEvent, useId, useState } from "react";

// 候補として一度に出す数の上限。超える分は件数だけ示し、文字を足して絞ってもらう
export const MAX_CANDIDATES = 8;

// 入力した文字を含むリポジトリ（大文字小文字を区別しない）。選んだものは除き、名前の順に並べる。
export function repoCandidates(
  repos: string[],
  query: string,
  chosen: string[],
): string[] {
  const q = query.trim().toLowerCase();
  return repos
    .filter((r) => !chosen.includes(r) && r.toLowerCase().includes(q))
    .sort();
}

export function RepoFilter({
  repos,
  chosen,
  onChange,
}: {
  repos: string[];
  chosen: string[];
  onChange: (next: string[]) => void;
}) {
  const id = useId();
  const listId = `${id}-list`;
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const all = repoCandidates(repos, query, chosen);
  const shown = all.slice(0, MAX_CANDIDATES);
  const expanded = open && shown.length > 0;
  const add = (r: string) => {
    onChange([...chosen, r]);
    setQuery("");
    setActive(0);
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setOpen(true);
      setActive((a) => Math.min(a + 1, shown.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => Math.max(a - 1, 0));
    } else if (e.key === "Enter") {
      const r = shown[active];
      if (expanded && r) {
        e.preventDefault();
        add(r);
      }
    } else if (e.key === "Escape") {
      setOpen(false);
    } else if (e.key === "Backspace" && query === "" && chosen.length > 0) {
      // 空の入力欄で Backspace を押すと、最後に選んだものを外す
      onChange(chosen.slice(0, -1));
    }
  };
  return (
    <div className="graph-repo-filter">
      <label htmlFor={`${id}-input`} className="graph-repo-filter-label">
        リポジトリ
      </label>
      {chosen.length > 0 ? (
        <ul aria-label="絞り込むリポジトリ" className="graph-repo-chosen">
          {chosen.map((r) => (
            <li key={r} title={r}>
              <span className="graph-repo-chosen-name">{r}</span>
              <button
                type="button"
                aria-label={`${r} を絞り込みから外す`}
                onClick={() => onChange(chosen.filter((x) => x !== r))}
              >
                ×
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="graph-repo-combo">
        <input
          id={`${id}-input`}
          type="text"
          role="combobox"
          autoComplete="off"
          aria-expanded={expanded}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={
            expanded
              ? `${id}-opt-${Math.min(active, shown.length - 1)}`
              : undefined
          }
          placeholder={`名前の一部を入力（${repos.length} 件）`}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          // 候補を押す前に閉じないよう、少し待ってから閉じる
          onBlur={() => setTimeout(() => setOpen(false), 120)}
          onKeyDown={onKey}
        />
        {/* フォーカスは入力欄に置いたまま aria-activedescendant で候補を指す（WAI-ARIA の combobox の形）。
            候補の並びと候補はフォーカスを受けず、キーボードの操作は入力欄の onKeyDown が受ける */}
        {expanded ? (
          <div className="graph-repo-options">
            {/* biome-ignore lint/a11y/useFocusableInteractive: フォーカスは入力欄が持ち、候補は aria-activedescendant で指す */}
            <div
              id={listId}
              // biome-ignore lint/a11y/useSemanticElements: select は入力欄と組み合わせた候補の形にできない
              role="listbox"
              aria-label="リポジトリの候補"
            >
              {shown.map((r, k) => (
                // biome-ignore lint/a11y/useKeyWithClickEvents: キーボードでは入力欄の上下の矢印と Enter で選ぶ
                // biome-ignore lint/a11y/useFocusableInteractive: フォーカスは入力欄が持ち、候補は aria-activedescendant で指す
                <div
                  key={r}
                  id={`${id}-opt-${k}`}
                  // biome-ignore lint/a11y/useSemanticElements: 上と同じく combobox の候補
                  role="option"
                  aria-selected={k === active}
                  title={r}
                  // 入力欄のフォーカスを外さずに選ぶ
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => add(r)}
                  onMouseEnter={() => setActive(k)}
                >
                  {r}
                </div>
              ))}
            </div>
            {all.length > shown.length ? (
              <p className="graph-repo-more">
                ほか {all.length - shown.length} 件（文字を足して絞る）
              </p>
            ) : null}
          </div>
        ) : open && query.trim() !== "" ? (
          <output className="graph-repo-none">
            「{query.trim()}」を含むリポジトリはありません
          </output>
        ) : null}
      </div>
    </div>
  );
}
