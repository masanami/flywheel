import { useState } from "react";
import { api } from "../api/client.ts";
import type { Challenge } from "../api/types.gen.ts";
import { URGENCIES } from "../api/write.ts";
import { Field, errorText } from "./common.tsx";

// 起票（M1 の create と同じ規則。起票した課題は「未分類」から始まる）。
export function CreateForm({
  workspace,
  onCreated,
}: {
  workspace: string;
  onCreated?: (c: Challenge) => void;
}) {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [doneCriteria, setDoneCriteria] = useState("");
  const [urgency, setUrgency] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<Challenge | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await api.createChallenge(workspace, {
        title,
        description,
        done_criteria: doneCriteria,
        urgency: urgency === "" ? null : urgency,
      });
      setCreated(r.challenge);
      onCreated?.(r.challenge);
      setTitle("");
      setDescription("");
      setDoneCriteria("");
      setUrgency("");
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="write-form" aria-label="起票フォーム">
      <Field label="タイトル（必須）">
        <input
          type="text"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          maxLength={300}
        />
      </Field>
      <Field label="説明">
        <textarea
          value={description}
          rows={4}
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
      <Field label="完了条件">
        <textarea
          value={doneCriteria}
          rows={3}
          onChange={(e) => setDoneCriteria(e.target.value)}
        />
      </Field>
      <Field label="緊急度">
        <select value={urgency} onChange={(e) => setUrgency(e.target.value)}>
          <option value="">未指定</option>
          {URGENCIES.map((u) => (
            <option key={u} value={u}>
              {u}
            </option>
          ))}
        </select>
      </Field>
      {error ? (
        <p role="alert" className="form-error">
          {error}
        </p>
      ) : null}
      {created ? (
        <output className="form-ok">
          {created.id}{" "}
          を起票しました（未分類）。優先度は分類のあとに変えられます。
        </output>
      ) : null}
      <button type="submit" className="primary" disabled={busy}>
        {busy ? "起票しています…" : "起票する"}
      </button>
    </form>
  );
}
