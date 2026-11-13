# Makefile — 品質ゲートの入口。macOS の GNU Make 3.81 と Linux の GNU Make の
# 両方で動く書き方にする（bash 拡張構文や GNU Make 4 以降専用の機能は使わない）。

GOLANGCI_LINT_VERSION := v2.10.1
BUILD_DIR := build
BINARY := $(BUILD_DIR)/flywheel
# UI（web/）。ビルド成果物は internal/server/dist/ui（git の追跡外）へ出て、go:embed で
# バイナリへ入る。Node が無くても go build は通る（最小の index.html を返す）。
NPM := npm
WEB_DIR := web

.PHONY: check gate e2e e2e-real ui-install ui-check ui-lint ui-test ui-build fmt-check vet lint test build print-golangci-lint-version clean

## check はすべての品質ゲートを順に実行する。1 つでも失敗すれば非 0 で終わる。
## UI の lint・テスト・ビルドを先に実行し、その成果物を埋め込んだ状態で go test・go build を行う。
## コミット済みのファイルは書き換えない（成果物は追跡外の internal/server/dist/ui へ出る）。
check: ui-lint ui-test ui-build fmt-check vet lint test build

## ui-check: UI のゲート（lint・テスト・ビルド）だけを実行する。
ui-check: ui-lint ui-test ui-build

## ui-install: npm の依存をロックファイルどおりに入れる（package-lock.json が新しいときだけ）。
ui-install: $(WEB_DIR)/node_modules/.install-stamp

# 印は npm ci が成功した後にだけ作る（npm ci は node_modules を作り直すため、
# ディレクトリの更新時刻を印にすると失敗した install が再実行されない）。
$(WEB_DIR)/node_modules/.install-stamp: $(WEB_DIR)/package-lock.json
	@echo "==> npm ci (web)"
	cd $(WEB_DIR) && $(NPM) ci
	@touch $(WEB_DIR)/node_modules/.install-stamp

## e2e: UI のブラウザの E2E（Playwright。偽の API の mock モードで画面を操作する）。make check には入れない。
## 本物の serve に向けた試験（web/e2e-real/）は流さない（make e2e-real）。
## 初回は cd web && npx playwright install chromium が要る（落とせない環境では PLAYWRIGHT_CHANNEL=chrome で手元の Chrome を使う）。
## ブラウザが無いときは、何をすればよいかを出して落とす（web/scripts/check-browser.mjs）。
e2e: ui-install
	@echo "==> check browser (web)"
	cd $(WEB_DIR) && node scripts/check-browser.mjs
	@echo "==> npm run e2e (web)"
	cd $(WEB_DIR) && $(NPM) run e2e

## gate: 手元の必須のゲート（claude-harness の品質ゲートの宣言 .claude-harness/quality-gate.json が呼ぶ）。
## make check に続けて make e2e を流す。CI は同じ 2 つを別の手順で流す。
gate:
	$(MAKE) check
	$(MAKE) e2e

## e2e-real: 関係グラフのブラウザの E2E を、本物の flywheel serve に向けて流す（Playwright。web/e2e-real/）。
## make check と make e2e には入れない。ui-build と build の後に、準備の段（web/e2e-real/global-setup.ts）が
## 一時ディレクトリへ種のストア（Go の TestRelationGraphSeed）を作り、build/flywheel を serve --port 0 で起動する。
## 委譲の子の環境（FLYWHEEL_DELEGATED_RUN がある）では serve が拒否されて失敗する（CI のジョブ e2e-real で流す）。
e2e-real: ui-build build
	@echo "==> npm run e2e:real (web)"
	cd $(WEB_DIR) && $(NPM) run e2e:real

## ui-lint: UI の lint（Biome）。
ui-lint: ui-install
	@echo "==> npm run lint (web)"
	cd $(WEB_DIR) && $(NPM) run lint

## ui-test: UI のテスト（Vitest）。
ui-test: ui-install
	@echo "==> npm run test (web)"
	cd $(WEB_DIR) && $(NPM) run test

## ui-build: UI のビルド（Vite）。成果物は internal/server/dist/ui へ出る。
ui-build: ui-install
	@echo "==> npm run build (web)"
	cd $(WEB_DIR) && $(NPM) run build

## fmt-check: gofmt -l . の出力が空であることを確認する。
fmt-check:
	@echo "==> gofmt -l ."
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "gofmt found unformatted files:"; \
		echo "$$files"; \
		exit 1; \
	fi

## vet: go vet ./... が終了コード 0 で終わることを確認する。
vet:
	@echo "==> go vet ./..."
	go vet ./...

## lint: golangci-lint run。事前にインストール済みの版が GOLANGCI_LINT_VERSION と
## 一致することを検査してから実行する。
lint:
	@echo "==> golangci-lint run (want $(GOLANGCI_LINT_VERSION))"
	@installed="$$(golangci-lint version 2>/dev/null | sed -n 's/.*version v\{0,1\}\([0-9][0-9.]*\).*/\1/p')"; \
	wanted="$$(echo $(GOLANGCI_LINT_VERSION) | sed 's/^v//')"; \
	if [ "$$installed" != "$$wanted" ]; then \
		echo "golangci-lint version mismatch: installed=v$$installed want=$(GOLANGCI_LINT_VERSION)"; \
		echo "(see 'make print-golangci-lint-version' for the pinned version)"; \
		exit 1; \
	fi
	golangci-lint run

## test: go test -race ./... が終了コード 0 で終わることを確認する。-timeout 20m はパッケージごとにかかる。テストの用意を速くした後（flywheel#567）の main の CI（Linux）の実測では、最も遅いパッケージでも約 4.2 分（internal/core 187〜252 秒、internal/cli 180〜222 秒）なので、20m で足りる。
test:
	@echo "==> go test -race ./..."
	go test -race -timeout 20m ./...

## build: CGO_ENABLED=0 go build ./cmd/flywheel。成果物は build/ 配下（.gitignore 済み）へ出す。
build:
	@echo "==> CGO_ENABLED=0 go build ./cmd/flywheel"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -o $(BINARY) ./cmd/flywheel

## print-golangci-lint-version: CI がインストールする版を読み取るための出力。
print-golangci-lint-version:
	@echo $(GOLANGCI_LINT_VERSION)

clean:
	rm -rf $(BUILD_DIR)
