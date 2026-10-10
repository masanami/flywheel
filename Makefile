# Makefile — 品質ゲートの入口。macOS の GNU Make 3.81 と Linux の GNU Make の
# 両方で動く書き方にする（bash 拡張構文や GNU Make 4 以降専用の機能は使わない）。

GOLANGCI_LINT_VERSION := v2.10.1
BUILD_DIR := build
BINARY := $(BUILD_DIR)/flywheel
# UI（web/）。ビルド成果物は internal/server/dist/ui（git の追跡外）へ出て、go:embed で
# バイナリへ入る。Node が無くても go build は通る（最小の index.html を返す）。
NPM := npm
WEB_DIR := web

.PHONY: check ui-install ui-check ui-lint ui-test ui-build fmt-check vet lint test build print-golangci-lint-version clean

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

## test: go test -race ./... が終了コード 0 で終わることを確認する。
test:
	@echo "==> go test -race ./..."
	go test -race ./...

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
