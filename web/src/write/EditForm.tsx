import { useState } from "react";
import { api } from "../api/client.ts";
import type { Challenge } from "../api/types.gen.ts";
import { PRIORITIES, type Priority, URGENCIES } from "../api/write.ts";
import { ClampedText } from "../cards.tsx";
import { Field, errorText, isConflict } from "./common.tsx";

const fieldsOf = (c: Challenge) => ({
  title: c.title,
  description: c.description,
  done_criteria: c.done_criteria,
  urgency: c.urgency ?? "",
});
type Fields = ReturnType<typeof fieldsOf>;

const FIELD_NAMES: [keyof Fields, string][] = [
  ["title", "タイトル"],
  ["description", "説明"],
  ["done_criteria", "完了条件"],
  ["urgency", "緊急度"],
];

// 人間記入欄の編集と優先度の変更。開いた時点の版（base.version）を添えて送り、
// 版が違えば conflict で拒否される（M4P8）。自動では上書きしない。
export function EditForm({
  workspace,
  base,
  hasUpstream = false,
  onSaved,
}: {
  workspace: string;
  base: Challenge;
  // 取り込み元の対応がある課題か（上流の本文が変わると取り込みが置き換えることを示す）
  hasUpstream?: boolean;
  onSaved?: (c: Challenge) => void;
}) {
  const [version, setVersion] = useState(base.version);
  const [form, setForm] = useState<Fields>(fieldsOf(base));
  const [priority, setPriority] = useState<Priority>(
    (base.priority as Priority | null) ?? "P2",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [conflict, setConflict] = useState<Challenge | null>(null);
  const [currentPriority, setCurrentPriority] = useState(base.priority);

  const set = (k: keyof Fields) => (v: string) => setForm({ ...form, [k]: v });

  const run = async (
    what: "save" | "priority",
    call: () => Promise<{ challenge: Challenge }>,
  ) => {
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      const r = await call();
      setVersion(r.challenge.version);
      setCurrentPriority(r.challenge.priority);
      setConflict(null);
      setSaved(
        what === "save"
          ? `保存しました（版 ${r.challenge.version}）`
          : `優先度を ${r.challenge.priority} にしました（版 ${r.challenge.version}）`,
      );
      onSaved?.(r.challenge);
    } catch (err) {
      if (isConflict(err)) {
        setError(errorText(err));
        try {
          setConflict((await api.challenge(workspace, base.id)).challenge);
        } catch (e2) {
          setError(`${errorText(err)} / 最新の取得にも失敗: ${errorText(e2)}`);
        }
      } else {
        setError(errorText(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    void run("save", () =>
      api.editChallenge(workspace, base.id, {
        version,
        title: form.title,
        description: form.description,
        done_criteria: form.done_criteria,
        urgency: form.urgency === "" ? null : form.urgency,
      }),
    );
  };

  const canPrioritize =
    base.status !== "unclassified" && base.status !== "done";

  return (
    <div className="edit-form">
      <form onSubmit={save} className="write-form" aria-label="編集フォーム">
        <p className="form-meta">
          <code>{base.id}</code> ・ 表示している版: {version}
        </p>
        {hasUpstream ? (
          <p className="form-warn">
            この課題には取り込み元があります。上流の本文が変わると、取り込みがタイトル・説明・緊急度を上流の値で置き換えます。
          </p>
        ) : null}
        <Field label="タイトル">
          <input
            type="text"
            value={form.title}
            onChange={(e) => set("title")(e.target.value)}
          />
        </Field>
        <Field label="説明">
          <textarea
            rows={4}
            value={form.description}
            onChange={(e) => set("description")(e.target.value)}
          />
        </Field>
        <Field label="完了条件">
          <textarea
            rows={3}
            value={form.done_criteria}
            onChange={(e) => set("done_criteria")(e.target.value)}
          />
        </Field>
        <Field label="緊急度">
          <select
            value={form.urgency}
            onChange={(e) => set("urgency")(e.target.value)}
          >
            <option value="">未指定</option>
            {URGENCIES.map((u) => (
              <option key={u} value={u}>
                {u}
              </option>
            ))}
          </select>
        </Field>
        <button type="submit" className="primary" disabled={busy}>
          保存する
        </button>
      </form>

      <section className="write-form" aria-label="優先度の変更">
        <h3>優先度の変更</h3>
        <p className="form-meta">
          いまの優先度: {currentPriority ?? "なし"}
          （状態は変わりません）
        </p>
        {canPrioritize ? (
          <div className="inline-row">
            <label className="field-inline">
              優先度
              <select
                value={priority}
                onChange={(e) => setPriority(e.target.value as Priority)}
              >
                {PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              disabled={busy}
              onClick={() =>
                void run("priority", () =>
                  api.prioritize(workspace, base.id, { version, priority }),
                )
              }
            >
              優先度を変更する
            </button>
          </div>
        ) : (
          <p className="form-meta">
            優先度を変えられるのは、分類済み以降で完了していない課題だけです。
          </p>
        )}
      </section>

      {error ? (
        <p role="alert" className="form-error">
          {error}
        </p>
      ) : null}
      {saved ? <output className="form-ok">{saved}</output> : null}

      {conflict ? (
        <section
          className="conflict-panel"
          aria-label="版の衝突"
          data-testid="conflict-panel"
        >
          <h3>別の操作で先に更新されています（版 {conflict.version}）</h3>
          <p>
            あなたが開いた版は {version}
            です。自動では上書きしません。違いを見て選んでください。
          </p>
          <table>
            <thead>
              <tr>
                <th>項目</th>
                <th>あなたの入力</th>
                <th>最新</th>
              </tr>
            </thead>
            <tbody>
              {FIELD_NAMES.map(([k, name]) => {
                const mine = form[k];
                const theirs = fieldsOf(conflict)[k];
                return (
                  <tr key={k} data-differs={mine !== theirs}>
                    <th scope="row">{name}</th>
                    <td>
                      <ClampedText text={mine || "（空）"} />
                    </td>
                    <td>
                      <ClampedText text={theirs || "（空）"} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <div className="inline-row">
            <button
              type="button"
              onClick={() => {
                setForm(fieldsOf(conflict));
                setVersion(conflict.version);
                setCurrentPriority(conflict.priority);
                setConflict(null);
                setError(null);
              }}
            >
              最新を取り込む（入力は捨てる）
            </button>
            <button
              type="button"
              onClick={() => {
                setVersion(conflict.version);
                setCurrentPriority(conflict.priority);
                setConflict(null);
                setError(null);
              }}
            >
              入力を残して最新の版で保存し直す
            </button>
          </div>
        </section>
      ) : null}
    </div>
  );
}
