import { useState } from "react";
import type { Screen } from "../screens.tsx";
import { WorkspaceSelect, useWorkspaceChoice } from "../write/common.tsx";
import type { MockStore } from "./store.ts";

// 「モックの操作」画面。画面の部品ではなく、偽のデータを動かすための操作だけを置く。
function MockControls({
  store,
  live,
}: {
  store: MockStore;
  live: Parameters<Screen["render"]>[0];
}) {
  const { ok, current, setPicked } = useWorkspaceChoice(live.workspaces);
  const [id, setId] = useState("");
  const [note, setNote] = useState<string | null>(null);
  const items = current ? (store.workspaces[current.name]?.items ?? []) : [];
  const target = items.find((i) => i.c.id === id) ?? items[0];
  return (
    <section aria-label="モックの操作">
      <h2>モックの操作</h2>
      <p>偽のデータを動かすための操作です。本番のビルドには含まれません。</p>
      {current ? (
        <>
          <WorkspaceSelect
            workspaces={ok}
            value={current.name}
            onChange={setPicked}
          />
          <label className="field-inline">
            課題
            <select
              value={target?.c.id ?? ""}
              onChange={(e) => setId(e.target.value)}
            >
              {items.map((i) => (
                <option key={i.c.id} value={i.c.id}>
                  {i.c.id} {i.c.title.slice(0, 30)}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            disabled={!target}
            onClick={() => {
              if (!target) return;
              // 別の操作者が更新したことを再現する（版が進む。タイトルも変える）
              target.c.version += 1;
              target.c.title = `${target.c.title.replace(/ \(他者が更新\)$/, "")} (他者が更新)`;
              if (target.operations[0]) target.operations[0].version += 1;
              store.emit(current.name);
              setNote(
                `${target.c.id} を版 ${target.c.version} に進めました。開いたままのフォームで保存・承認すると conflict になります。`,
              );
            }}
          >
            別の操作者が更新したことにする（版を進める）
          </button>
        </>
      ) : null}
      <button
        type="button"
        onClick={() => {
          store.reset();
          setNote("偽のデータを初期状態に戻しました。");
        }}
      >
        偽のデータを初期化する
      </button>
      {note ? <output className="form-ok">{note}</output> : null}
    </section>
  );
}

export function mockScreens(store: MockStore): Screen[] {
  return [
    {
      id: "mock-controls",
      title: "モックの操作",
      render: (live) => <MockControls store={store} live={live} />,
    },
  ];
}
