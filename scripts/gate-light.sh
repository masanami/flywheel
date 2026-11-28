#!/bin/sh
# gate-light.sh [--plan]
#
# タスクの PR の軽い検査（make gate-light）。POSIX sh で書く。リポジトリの根で実行する。
# --plan は何も実行せず、決めた内容（mode・reason・ui・test）だけを出す。
#
# PR の宛先が統合ブランチ（integration/*）だと分かったときだけ軽い検査にする。宛先が main のとき・
# 判定できないときは make gate（フル）を回す。宛先は次の順で判定する。
#   1. 環境変数 GATE_BASE（あれば、これだけで決める）
#   2. いまのブランチの upstream が origin/integration/* なら統合ブランチ、origin/main なら main
#   3. origin/integration/* のどれかの「origin/main に無いコミット」を HEAD が含めば統合ブランチ
#
# 軽い検査で拾えないもの（依存の無いパッケージのテストが読むファイルの変更・Go のファイルを変えない
# 埋め込み資材の変更など）は、main へ上げる前のフルの検査で拾う。
set -u

MAKE="${MAKE:-make}"
plan=no
[ "${1:-}" = "--plan" ] && plan=yes

full() {
	echo "gate-light: mode=full reason=$1"
	[ "$plan" = yes ] && exit 0
	exec "$MAKE" gate
}

detect_base() {
	if [ -n "${GATE_BASE:-}" ]; then
		case "$GATE_BASE" in
		integration/?*) echo integration ;;
		main) echo main ;;
		*) echo unknown ;;
		esac
		return
	fi
	upstream=$(git rev-parse --symbolic-full-name '@{upstream}' 2>/dev/null) || upstream=""
	case "$upstream" in
	refs/remotes/origin/integration/*) echo integration; return ;;
	refs/remotes/origin/main) echo main; return ;;
	esac
	for ref in $(git for-each-ref --format='%(refname)' refs/remotes/origin/integration/); do
		common=$(git merge-base HEAD "$ref" 2>/dev/null) || continue
		if ! git merge-base --is-ancestor "$common" refs/remotes/origin/main; then
			echo integration
			return
		fi
	done
	echo unknown
}

git rev-parse --verify -q 'refs/remotes/origin/main^{commit}' >/dev/null || full "origin/main-not-found"
case $(detect_base) in
integration) ;;
main) full "base-is-main" ;;
*) full "base-unknown" ;;
esac

root=$(git rev-parse --show-toplevel) || full "no-toplevel"
cd "$root" || full "no-toplevel"
root=$(pwd -P)
mb=$(git merge-base HEAD refs/remotes/origin/main) || full "no-merge-base"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/gate-light.XXXXXX") || full "no-tempdir"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM

# merge-base からの差分（コミット済み・未コミット）と、追跡していないファイル。
# --no-renames で、移動は元と先の両方を出す。
git -c core.quotepath=false diff --no-renames --name-only "$mb" -- >"$tmp/changed" || full "diff-failed"
git -c core.quotepath=false ls-files --others --exclude-standard >>"$tmp/changed" || full "diff-failed"

ui=no
go_changed=no
all=no
while IFS= read -r f; do
	case "$f" in
	web/*) ui=yes ;;
	go.mod | go.sum | Makefile | .golangci.yml | scripts/gate-light.sh) all=yes ;;
	*.go) go_changed=yes ;;
	esac
done <"$tmp/changed"

test_mode=none
pkgs=""
if [ "$all" = yes ]; then
	test_mode=all
elif [ "$go_changed" = yes ]; then
	test_mode=packages
	if ! go list -f '{{.Dir}}|{{.ImportPath}}|{{join .Deps " "}}|{{join .TestImports " "}} {{join .XTestImports " "}}' ./... >"$tmp/list"; then
		test_mode=all
	else
		# 変えたファイルを持つパッケージと、その逆依存（テストだけの依存を含む）を選ぶ。
		# Go のファイルのディレクトリがパッケージでない（パッケージを消した等）ときは全部にする。
		pkgs=$(awk -v root="$root" '
			FNR == NR {
				split($0, a, "|")
				bydir[a[1]] = a[2]; deps[a[2]] = a[3]; tests[a[2]] = a[4]
				next
			}
			{
				n = split($0, part, "/")
				exact = ($0 ~ /\.go$/ && $0 !~ /(^|\/)testdata\//)
				for (i = n - 1; i >= 0; i--) {
					dir = root
					for (j = 1; j <= i; j++) dir = dir "/" part[j]
					if (dir in bydir) { changed[bydir[dir]] = 1; break }
					if (exact) { print "ALL"; exit }
				}
			}
			function hit(p,    d, k, m) {
				if (p in changed) return 1
				m = split(deps[p], d, " ")
				for (k = 1; k <= m; k++) if (d[k] in changed) return 1
				return 0
			}
			END {
				for (p in deps) {
					found = hit(p)
					m = split(tests[p], t, " ")
					for (k = 1; !found && k <= m; k++) found = hit(t[k])
					if (found) print p
				}
			}
		' "$tmp/list" "$tmp/changed" | LC_ALL=C sort) || pkgs=ALL
		case "$pkgs" in
		*ALL*) test_mode=all; pkgs="" ;;
		"") test_mode=all ;;
		esac
	fi
fi

echo "gate-light: mode=light merge-base=$mb"
echo "gate-light: ui=$ui"
if [ "$test_mode" = packages ]; then
	echo "gate-light: test=packages"
	for p in $pkgs; do echo "gate-light: package=$p"; done
else
	echo "gate-light: test=$test_mode"
fi
[ "$plan" = yes ] && exit 0

if [ "$ui" = yes ]; then
	"$MAKE" ui-check || exit 1
fi
"$MAKE" fmt-check vet lint || exit 1
case "$test_mode" in
all)
	echo "==> go test -race -count=1 ./..."
	go test -race -count=1 -timeout 20m ./... || exit 1
	;;
packages)
	echo "==> go test -race -count=1 (changed packages and their reverse dependencies)"
	# shellcheck disable=SC2086 # パッケージのパスは空白を含まないので、分割して渡す。
	go test -race -count=1 -timeout 20m $pkgs || exit 1
	;;
esac
"$MAKE" build || exit 1
if [ "$ui" = yes ]; then
	"$MAKE" e2e || exit 1
fi
