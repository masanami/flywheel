#!/bin/bash
# ticket-light-branch.sh --key <名前の鍵>
#
# ticket-light ワークフローの branch ステップ。resolve の key（task-<番号> か issue-<番号>）から、作業ブランチの名前
# feature/<鍵>-light を決めて返す（分析のステップが無いので、ブランチの名前をここで決める）。claude・gh・git は呼ばない。
# stdout に JSON を 1 つ出す（schemas/ticket-light-branch.json）。outcome は ok だけ。鍵が task-<番号>・issue-<番号>
# の形でなければ、stderr に理由を出して非 0 で終わる（runtime は step_error にする）。

set -u

key=""
usage="usage: ticket-light-branch.sh --key <task-N|issue-N>"
while [ $# -gt 0 ]; do
  case "$1" in
    --key)
      if [ $# -lt 2 ]; then echo "Error: --key needs a value ($usage)" >&2; exit 2; fi
      key="$2"; shift 2 ;;
    *) echo "Error: unknown argument '$1' ($usage)" >&2; exit 2 ;;
  esac
done
if ! [[ "$key" =~ ^(task|issue)-[1-9][0-9]*$ ]]; then
  echo "Error: --key must be task-<number> or issue-<number>, got '${key}'" >&2
  exit 2
fi
printf '{"outcome":"ok","branch":"feature/%s-light"}\n' "$key"
