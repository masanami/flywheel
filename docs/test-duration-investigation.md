# 重いパッケージのテストの時間と、並列にできない理由（2026-11-28 の測定）

`make check` の `go test -race` で時間のかかる 3 つのパッケージ（`internal/server`・`internal/core`・`internal/cli`）について、テストを 1 本ずつの時間で測った。重いテストの上位と、並列にできない理由、直す価値がある候補をまとめる。テストのコードはこの文書では直さない（直すかは続きで決める）。

## 測り方

| 項目 | 値 |
|---|---|
| head | `e8b87c9`（文書の文字列を読むだけのテストを外した後の統合ブランチの先頭） |
| コマンド | `go test -race -count=1 -timeout 20m -json ./internal/<パッケージ>` を server → core → cli の順に 1 つずつ |
| 機械 | Apple M3 Pro（12 コア）・macOS 26.6.2・go1.26.11 darwin/arm64 |
| 時刻 | 2026-11-28 14:50〜15:12（日本時間） |

**値の読み方（注意）**: ほかの検査（別のタスクの `make gate`）と重なった時間に測った 1 回だけの値である。測定中の負荷（1 分の平均）は 2〜23 で、静かな機械の値より長い。値は**重い順の目安**として読み、絶対の秒数としては使わない。オーナーの決定で、静かな時間を待って測り直すことはしていない。

## パッケージごとの全体

| パッケージ | 全体の時間 | 最上位のテストの数 | うち並列（`t.Parallel`） | 直列のテストの時間の和 | 全部 pass |
|---|---|---|---|---|---|
| `internal/server` | 580 秒 | 287 | **1** | 568 秒 | はい |
| `internal/core` | 368 秒 | 3,580 | 3,463 | 62 秒 | はい |
| `internal/cli` | 329 秒 | 982 | 757 | 198 秒 | はい |

- **server は、ほぼ全部が直列で走っている**。直列のテストの時間の和（568 秒）が、パッケージ全体の時間（580 秒）とほぼ同じ。パッケージ全体の時間は、テストの時間の足し算で決まっている。
- **core は最上位のテストはほぼ並列**だが、1 本のテストの中のサブテストが直列のものがあり、その 1 本（178 秒）が全体の時間の半分を占める。
- **cli は並列の同時数を 4 までに抑えている**（`internal/cli/busy_test.go` の `capTestParallel`）。端末（pty）を使うテストは並列にしない形になっている（`internal/cli/toolpath_test.go` の `parallelTools`）。

## 上位のテスト

表の「並列」は最上位のテストが `t.Parallel()` を呼ぶか。「サブテスト」は、数・時間の和・いちばん長い 1 本。「コードで確かめたこと」は、テストの本体を読んで見た点で、呼んでいる下請けの関数の中までは確かめていない。

### internal/server

| 秒 | テスト | 並列 | サブテスト | コードで確かめたこと |
|---|---|---|---|---|
| 14.4 | `TestMain_UnsetsTheDelegatedMarkerForRealBinaries` | しない | 2・14.4・7.3 | テストのバイナリ自身（`os.Args[0]`）を子として起動し直す。本体に共有の状態の書き換えは無い |
| 12.9 | `TestCyclePostExitCode1StderrCases` | しない | 6・12.9・2.5 | ケースごとに偽の起動スクリプトのサーバを立てる。サブテストも直列 |
| 9.0 | `TestEvents_RealProcesses_CreateNotifiesNoGHAndSIGTERM` | しない | - | `buildFlywheel` で実バイナリを**別に** `go build` する（共有の `flywheelBinary` を使わない）。固定の待ち（`time.Sleep`）あり |
| 7.6 | `TestSpecBody_NoLocationIsNotFound` | しない | 5・7.6・1.7 | サブテストも直列 |
| 6.4 | `TestIngestAPI_BadRequestsCreateNothing` | しない | - | 本体に共有の状態の書き換えは無い |
| 6.2 | `TestWorkspacesAPI_ListsInDeclarationOrderWithNullError` | しない | - | 本体に共有の状態の書き換えは無い |
| 6.2 | `TestCyclePostNoSignalNorExitHonorsThePassedWait` | しない | - | 合図の待ち（`cycleSignalWait`＝3 秒）＋1.5 秒を**実際に待つ**ことを確かめるテスト |
| 6.0 | `TestHoldAnswer_SliceSplitOptions` | しない | 2・6.0・3.1 | サブテストも直列 |
| 5.4 | `TestApproveSucceedsWhenTreeRereadFails_837` | しない | 2・5.4・2.9 | サブテストも直列 |
| 5.1 | `TestEvents_NonOKWorkspaceDoesNotStopObservation` | しない | - | 固定の待ちあり（変化の通知の間隔を見るテスト） |
| 4.9 | `TestEvents_StoreWriteNotifiesWithinTwoIntervals` | しない | - | 固定の待ちあり（「2 回の間隔の内に通知する」を見るテスト） |
| 4.9 | `TestBrowseAPI_CardsDoNotHoldStoreLeaseWhileFetchingChecks` | しない | 2・4.9・3.1 | 固定の待ちあり |
| 4.7 | `TestCyclePostNoSignalNorExitReturnsStartedAfterThreeSeconds` | しない | - | 3 秒の合図の待ちを実際に待つ |
| 4.7 | `TestCyclePostOnlyTheSignalLineShapeCountsAsStarted` | しない | - | 合図の待ちを実際に待つ |
| 4.4 | `TestE2ECSRFWithRealBinary` | しない | - | 実バイナリを**別に** `go build` する |
| 4.3 | `TestIngestAPI_CreatesChallengeAndStartsTheCycleOnce` | しない | - | 本体に共有の状態の書き換えは無い |
| 4.3 | `TestOverviewBuiltOncePerRequest_837` | しない | 2・4.3・2.2 | サブテストも直列 |
| 4.2 | `TestNewCycleStarter_MapsTheChildsOutcome` | しない | - | 本体に共有の状態の書き換えは無い |
| 4.1 | `TestStoreTooNew_AppearingWhileRunningFollowsAndRecovers` | しない | - | 本体に共有の状態の書き換えは無い |
| 4.0 | `TestCycleGetRunningAndLastEndReason` | しない | - | 本体に共有の状態の書き換えは無い |

パッケージ全体で見た、並列を妨げるもの:

- `t.Setenv` は 3 ファイル・6 か所だけ（`approval_test.go`・`status_test.go` の `PATH`、`cycle_env_test.go` の対話の環境変数）。`os.Chdir`・`t.Chdir` は無い。
- 固定のポートは無い（`Listen(0)`・`--port 0` で空きポートを使う）。例外は `csrf_e2e_test.go` で、起動し直したサーバに前と同じポートを渡す（並列にすると、その間にほかのテストがそのポートを取るおそれがある）。
- 端末（pty）を起動するテストは見当たらない（core も同じ）。
- 実バイナリの `go build` は、共有の 1 回（`status_test.go` の `flywheelBinary`。`sync.Once`）のほかに、`events_test.go` の `buildFlywheel` と `csrf_e2e_test.go` が別々に行う。
- 時間そのものを確かめるテスト（合図の 3 秒の待ち・通知の間隔）は、並列にして機械が混むと、待ちの上限を越えて落ちるおそれがある。

つまり、**大半の server のテストは、共有の状態があって直列なのではなく、`t.Parallel()` を付けていないだけ**に見える（#567 の決定〔共有の状態を使わないと確かめたものだけに `t.Parallel()` を付ける〕の確かめが、server ではまだされていない）。

### internal/core

| 秒 | テスト | 並列 | サブテスト | コードで確かめたこと |
|---|---|---|---|---|
| 178.2 | `TestImport_MalformedDocuments_AreRejected` | する | 15・168.3・23.1 | **サブテストが直列**。サブテストごとに新しいワークスペースのストアを作る（`newWorkspaceStore`＝`Init` と `OpenWorkspace`） |
| 66.7 | `TestChat_UnresolvableViewingIsRefusedWithoutWriting` | する | 6・66.7・11.5 | サブテストが直列 |
| 54.5 | `TestChat_ResumeFollowsEngineStartedNotTheEnding` | する | 5・54.5・12.1 | サブテストが直列 |
| 51.5 | `TestChat_ViewingTitlesAreLookedUpByCore` | する | - | 1 本で長い |
| 43.9 | `TestChatReply_StoppedOrFailedClosesRunningToolsAndPendingPrompts` | する | 4・43.9・12.0 | サブテストが直列 |
| 43.0 | `TestChatPrompt_AnsweredExactlyOnce` | する | 4・43.1・11.5 | サブテストが直列 |
| 36.1 | `TestInit_LeavesAnExistingClaudeSettingsAlone` | する | - | ループの中で 4 つのワークスペースを順に作る（`t.Run` を使わない） |
| 34.2 | `TestMultiTask_ConcurrentLaunchesAreNotRefusedByDoubleCounting` | する | - | 20 回の繰り返し |
| 23.8 | `TestImport_ForeignKeyViolation_IsRejectedAndLeavesNoRows` | する | - | ストアを作って書き出す |
| 21.6 | `TestChat_ExportImportRoundTripKeepsTheRows` | する | - | ストアを作って書き出す |
| 20.7 | `TestImport_KeepsLargeIntegersExact` | する | - | 同上 |
| 20.5 | `TestImport_HoldOptionsColumnsSurviveTheRoundTrip` | する | - | 同上 |
| 20.4 | `TestImport_EmptyExportIntoStoreWithRows_IsRejected` | する | - | 同上 |
| 20.4 | `TestInit_CreatesClaudeSettingsWhenMissing` | する | - | ワークスペースを作る |
| 20.1 | `TestImport_SequenceOfTableWithoutIntegerKeyIsNotComparedWithRowid` | する | - | ストアを作って書き出す |
| 20.1 | `TestImport_IntoStoreWithRows_IsRejected` | する | - | 同上 |
| 20.1 | `TestImport_RoundTripIsByteIdenticalAndKeepsNumbering` | する | - | 同上 |
| 19.8 | `TestImport_KeepsSequencesAboveTheLargestID` | する | - | 同上 |
| 19.6 | `TestImport_S3aColumnsSurviveTheRoundTrip` | する | - | 同上 |
| 19.5 | `TestImport_S4aCycleColumnsSurviveTheRoundTrip` | する | - | 同上 |

- core は 3,463 本が同時に走るので、1 本ごとの時間は混み合いで延びている（ストアを 1 つ作るだけで 10 秒前後に見える）。
- パッケージ全体の時間は、いちばん長い 1 本（178 秒）と、CPU の取り合いで決まる。

### internal/cli

| 秒 | テスト | 並列 | サブテスト | コードで確かめたこと |
|---|---|---|---|---|
| 23.4 | `TestWorklog_EveryMutatingOperationRecordsAllFields` | しない | 46・34.2・2.1 | サブテストは `parallelTools` で、端末（pty）を使うケースだけ直列 |
| 19.3 | `TestServeFleet_WorkspaceFlagNameDerivation` | する | 7・19.3・3.3 | サブテストが直列 |
| 16.3 | `TestRunResults_AreTheDocumentedClosedSetInBothDirections` | する | 8・16.3・3.1 | サブテストが直列 |
| 13.1 | `TestTransitionCommands_UndefinedStatusCombinationsAreRejected` | する | 81・13.1・0.3 | 短いサブテストが多い |
| 12.1 | `TestCycle_NotStartedReasonsAreTheClosedSetInBothDirections` | する | 7・12.1・3.7 | サブテストが直列 |
| 9.5 | `TestAllCommands_JSONSuccessWritesSingleDocumentToStdout` | しない | 43・19.8・2.8 | `parallelTools` で、pty を使うケースだけ直列 |
| 9.3 | `TestMergeIn_NotApprovedUnlessMergeInOnlyAndCIPasses` | する | 5・9.3・2.5 | サブテストが直列 |
| 7.5 | `TestRun_CreateReturnsStoreBusyWhileAnotherProcessHoldsTheWriteLock` | する | - | 書き込みの排他を別のプロセスで握って確かめる |
| 7.3 | `TestFlowCommands_ThroughTheCLI` | する | - | 固定の待ちあり |
| 7.2 | `TestRun_SingleWorktreeSlot_SameRepoSecondRun_IsLocked` | する | - | 排他を確かめる |
| 5.4 | `TestCycle_RunningCycleDoesNotBlockClassifyAutoByID` | する | - | - |
| 5.2 | `TestRun_HoldsTheLockWhileRunningAndReleasesAfterwards` | しない | - | 排他を確かめる |
| 4.6 | `TestScenario_CLIOnly_InitThroughApprove_ReachesDoneWithConsistentLogAndStatus` | しない | - | 通しのシナリオ |
| 4.6 | `TestExportImport_RoundTripShowAndLogMatch` | する | - | - |
| 4.5 | `TestClassifyAuto_AppliesTheSliceSplitOutput` | する | 2・4.5・3.1 | サブテストが直列 |
| 4.5 | `TestDocumentedJSON_AutoOperationsReturnAPhaseObject` | する | 3・4.4・2.5 | コードと文書を照らすテスト（残すもの） |
| 4.4 | `TestInit_OutputSaysWhetherTheClaudeSettingsWereCreated` | する | - | - |
| 4.2 | `TestCycle_AllFivePhases_DelegatesAndVerifiesInTheSameCycleWithin` | する | - | - |
| 4.2 | `TestShow_RunsAreNewestFirstAtMost100AndOnlyThisChallenges` | する | - | - |
| 4.0 | `TestIngest_DoesNotHoldStoreWriteLockWhileGHIsInFlight` | する | - | 固定の待ちあり |

## 直す価値がある候補

見込みの短縮は、上の値（混んだ時間の値）からの粗い見積もりで、直した後に測って確かめる。

| # | 候補 | 直し方 | 見込み | 気をつけること |
|---|---|---|---|---|
| 1 | **server のテストを並列にする** | `t.Setenv` を使わない・時間を確かめない・実バイナリを別に作らないテストから、共有の状態が無いことを確かめて `t.Parallel()` を付ける（#567 と同じやり方） | 最大。いまの 580 秒は直列の足し算なので、大半を並列にできれば数分単位で縮む | 時間を確かめるテスト（合図の 3 秒・通知の間隔）は直列に残すか、待ちの上限に余裕を持たせる。`t.Setenv` の 3 ファイルは直列のまま |
| 2 | **core の `TestImport_MalformedDocuments_AreRejected` のサブテストを並列にする** | サブテストに `t.Parallel()` を付ける（各サブテストは自分のストアを作るので、共有の状態は無いと見える） | 全体の時間の半分（178 秒）を占める 1 本が、いちばん長いサブテスト（23 秒）程度まで縮む見込み。ただし CPU の取り合いで、パッケージ全体はそれほどは縮まない | 同時に走る数が増え、ほかのテストの 1 本ごとの時間は延びうる |
| 3 | **core の対話のテスト（`TestChat_*`・`TestChatReply_*`・`TestChatPrompt_*`）のサブテストを並列にする** | 候補 2 と同じ | 40〜67 秒のテストが、それぞれ 12 秒前後まで縮む見込み | 対話のテストは偽の Claude のプロセスを起動するので、プロセスの数が増える |
| 4 | **server の実バイナリの `go build` を 1 回にまとめる** | `events_test.go` の `buildFlywheel` と `csrf_e2e_test.go` を、共有の `flywheelBinary` へ寄せる | 1 回の `go build` の分（数秒〜十数秒） | `csrf_e2e_test.go` はビルドの情報（vcs）の有無に依存しないかを確かめる |
| 5 | cli の、サブテストが直列の上位（`TestServeFleet_*`・`TestRunResults_*`・`TestCycle_NotStartedReasons*`・`TestMergeIn_*`） | サブテストに `t.Parallel()`（または `parallelTools`）を付ける | 各 10〜20 秒が数秒に | cli は同時数を 4 に抑えているので、効きは小さめ |

### 端末（pty）を使うテストの扱い

以前の並列化（#597）では、並列にしたサブテストで端末（pty）を起動すると、Linux の CI で `fork/exec: operation not permitted` で落ち、#599 で戻した。いまの cli は `parallelTools(t, needsTTY)` で、pty を使うケースを並列にしない形になっている。

- 上の候補は、**pty を使うテストを並列にしない**。cli の `TestWorklog_*`・`TestAllCommands_*` の pty のケースは直列のまま残す。
- server・core のテストには、いま pty を起動するものは見当たらない。並列にするときにもう一度確かめ、含まれるものがあれば `parallelTools` と同じく直列にする。
- 並列にした後は、Linux（CI）でも通ることを確かめてから main へ入れる（macOS の手元だけで通っても足りない）。

## やらなかったこと

- テストのコードの直し・並列化（続きで決める）。
- 静かな時間に測り直すこと（オーナーの決定で、1 回目の値を目安として使う）。
