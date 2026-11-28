#!/bin/sh
# gate-pr.sh — タスクの PR の品質ゲート（make gate-pr の本体。.claude-harness/quality-gate.json が呼ぶ）。
# POSIX sh で書く（macOS と Linux の両方で動かす）。
#
# PR の宛先が統合ブランチ（integration/…）なら軽い検査、それ以外と宛先を判定できないときはフルの make gate を回す。
# 宛先の判定:
#   1. いまのブランチ名を head にする開いた PR があれば、その宛先（gh pr list --head。複数あれば、すべてが integration/… のときだけ軽い検査）
#   2. 無い・gh が使えなければ、いまのブランチの上流（remote が origin の branch.<名前>.merge。作業ツリーは origin の宛先から切るので、
#      上流が宛先を指す。自分のブランチ名を上流にしていれば、判定できない扱いになる）
#
# 軽い検査: gofmt・vet・golangci-lint・build は全体。go test -race -count=1 は、origin/main との merge-base からの差分
# （未コミットと追跡外を含む）のファイルがあるパッケージと、それに依存するパッケージ（テストの import を含む）だけ。
# Go のファイルの差分が無ければ go test を飛ばす。go.mod・go.sum・Makefile・.golangci.yml の差分があれば全パッケージを試す。
# web/ の差分があるときだけ、UI のゲート（make ui-check）と make e2e を流す。
# ここで拾えないもの（差分の外のパッケージが読むファイル・Go の差分が無い変更・UI の差分が無いときの E2E）は、
# 反映先へ上げる前のフルの make gate で拾う。
set -eu

MAKE=${MAKE:-make}

full() {
	echo "==> gate-pr: full gate (make gate): $1"
	exec $MAKE gate
}

root=$(git rev-parse --show-toplevel)
cd "$root"

branch=$(git symbolic-ref -q --short HEAD) || full "cannot tell the destination of the pull request (detached HEAD)"
bases=""
if command -v gh >/dev/null 2>&1; then
	bases=$(gh pr list --head "$branch" --state open --json baseRefName --jq '.[].baseRefName' 2>/dev/null) || bases=""
fi
if [ -z "$bases" ]; then
	remote=$(git config --get "branch.$branch.remote") || remote=""
	merge=$(git config --get "branch.$branch.merge") || merge=""
	# 自分と同じ名前の上流は宛先を示さない（PR の head と宛先は同じにならない）。
	if [ "$remote" = "origin" ] && [ "$merge" != "refs/heads/$branch" ]; then
		bases=${merge#refs/heads/}
	fi
fi
[ -n "$bases" ] || full "cannot tell the destination of the pull request"
for base in $bases; do
	case "$base" in
	integration/?*) ;;
	*) full "the destination is not an integration branch ($base)" ;;
	esac
done

mb=$(git merge-base origin/main HEAD) || full "cannot find the merge base with origin/main"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/gate-pr.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
# 名前の変更は削除と追加の 2 件にする（移動元のパッケージも試す）。
git -c core.quotepath=false diff --name-only --no-renames "$mb" -- >"$tmp/changed"
git -c core.quotepath=false ls-files --others --exclude-standard >>"$tmp/changed"

web=false
if grep -q '^web/' "$tmp/changed"; then web=true; fi
scope=none
if grep -q -E '^(go\.mod|go\.sum|Makefile|\.golangci\.yml)$' "$tmp/changed"; then
	scope=all
elif grep -q '\.go$' "$tmp/changed"; then
	scope=changed
fi

echo "==> gate-pr: light gate (destination: $(echo $bases), merge base: $mb, web: $web, go test: $scope)"

# UI の成果物は go:embed でバイナリへ入るので、make check と同じく Go の検査より先に作る。
if [ "$web" = true ]; then $MAKE ui-check; fi
$MAKE fmt-check vet lint

pkgs=""
case "$scope" in
all) pkgs="./..." ;;
changed)
	module=$(go list -m -f '{{.Dir}}')
	go list -f '{{.ImportPath}}|{{.Dir}}|{{join .Deps " "}}|{{join .TestImports " "}} {{join .XTestImports " "}}' ./... >"$tmp/packages"
	# 差分のファイルは、いちばん近い上のディレクトリのパッケージに数える（埋め込みの資材・testdata を含む）。
	pkgs=$(MODULE_DIR="$module" awk -F'|' '
		FILENAME == ARGV[1] {
			dir = $2
			if (index(dir, ENVIRON["MODULE_DIR"] "/") == 1) dir = substr(dir, length(ENVIRON["MODULE_DIR"]) + 2)
			else dir = "."
			bydir[dir] = $1; deps[$1] = $3; tests[$1] = $4; order[++n] = $1
			next
		}
		{
			d = $0
			while (d != ".") {
				if (sub(/\/[^\/]*$/, "", d) == 0) d = "."
				if (d in bydir) { changed[bydir[d]] = 1; break }
			}
		}
		function touches(p,    list, i, m) {
			if (p in changed) return 1
			m = split(deps[p], list, " ")
			for (i = 1; i <= m; i++) if (list[i] in changed) return 1
			return 0
		}
		END {
			for (k = 1; k <= n; k++) {
				p = order[k]
				hit = touches(p)
				m = split(tests[p], imports, " ")
				for (j = 1; !hit && j <= m; j++) hit = touches(imports[j])
				if (hit) print p
			}
		}
	' "$tmp/packages" "$tmp/changed")
	;;
esac

if [ -n "$pkgs" ]; then
	echo "==> go test -race -count=1" $pkgs
	go test -race -count=1 -timeout 20m $pkgs
else
	echo "==> gate-pr: no Go package to test"
fi

$MAKE build
if [ "$web" = true ]; then $MAKE e2e; fi
echo "==> gate-pr: light gate passed"
