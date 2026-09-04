# ドキュメント索引

gem-agent の保守者向けドキュメントの入口。利用者向けは
[`README.ja.md`](../../README.ja.md) を参照。

英語版ミラー: [`INDEX.md`](../en/INDEX.md)。`scripts/docs-mirror-check.sh` が
`make check` で構造面を機械検証する — `docs/en` の各ファイルに `docs/ja` の
対応物があること（逆も）、ADR カタログが両言語で完全かつ昇順であること、
各ペアのコードスパンが一致すること。散文の対応は著者の責任。

## 仕様

- [`gem-agent-rfp.ja.md`](gem-agent-rfp.ja.md) — 仕様の正本: 問題設定、機能面、
  スコープ境界、フェーズ計画。ここから外れる機能は ADR を必要とする。実際に
  セッション再開とコンテキスト圧縮はその経路で入った。

## Reference（現状）

現在の挙動。evergreen でコードに追随して in-place 更新する。

機能分冊（README はここへリンクする。1 ドメイン 1 ファイル）:

- [`reference/interface.ja.md`](reference/interface.ja.md) — TUI・素の
  REPL・単発実行・キー・スラッシュコマンド・補完・`/settings`・
  テーマと UI 言語
- [`reference/tools.ja.md`](reference/tools.ja.md) — 全組み込みツールと
  背後の設計判断
- [`reference/attachments.ja.md`](reference/attachments.ja.md) —
  @ 参照: ファイル・画像・文書・音声/動画・GCS 経路
- [`reference/approval.ja.md`](reference/approval.ja.md) — MITL
  ゲート・自動承認・ツール別ポリシー・sandbox・起動時安全機構・
  非信頼コンテンツの隔離
- [`reference/sessions.ja.md`](reference/sessions.ja.md) — トランス
  クリプト・resume・状態配置・圧縮・`/usage`・エージェントメモリ
- [`reference/integration.ja.md`](reference/integration.ja.md) —
  プロジェクト指示ファイル・MCP サーバー・スキル
- [`reference/configuration.ja.md`](reference/configuration.ja.md) —
  導入・設定ファイル・優先順位・フラグ・テレメトリ・コンテンツ
  フィルタ・エンドポイント

プロジェクト参照:

- [`reference/architecture.ja.md`](reference/architecture.ja.md) —
  パッケージ構成、ターンループ、2 つの封じ込め境界、永続化、失敗時挙動の一覧
- [`reference/drill.ja.md`](reference/drill.ja.md) — オンデマンドの健全性
  チェック（旧・月次訓練、ADR-0061）: 勝手に腐るものは何か、それを捕まえる
  手順、初回実行の記録
- [`reference/promotion.ja.md`](reference/promotion.ja.md) — lab-series →
  cli-series の昇格基準と、それを失効させた 2026-09-01 の昇格決定のクローズ
  済み記録（ADR-0061）

## ADR

ある時点の設計判断。承認後は immutable であり、判断を変えるときは新しい ADR で
supersede する（typo とリンク修正は例外）。

- [`ADR-0001`](adr/0001-sandbox-mechanism.ja.md) — sandbox-exec + MITL:
  意思決定の境界と封じ込めの境界を分けた 2 層
- [`ADR-0002`](adr/0002-tui.ja.md) — Bubble Tea inline 方式。素の
  スクロールバックとコピペを保つため alt-screen を却下
- [`ADR-0003`](adr/0003-bottom-pinned-layout.ja.md) — alt-screen 無しで
  入力欄を最下部に固定する方式
- [`ADR-0004`](adr/0004-auto-approve.ja.md) — 自動承認の二層ラダー:
  モデルが持ち上げられないルールの床 + モデルの判断
- [`ADR-0005`](adr/0005-session-resume.ja.md) — セッションログを resume の
  正本にする。プロジェクトとモデルは警告ではなく拒否
- [`ADR-0006`](adr/0006-context-compaction.ja.md) — ウィンドウで死ぬ代わりに
  古い側を要約する。fail safe であって fail small ではない
- [`ADR-0007`](adr/0007-input-during-a-turn.ja.md) — ターン実行中の入力は
  捨てずに予約する。自動送信は正常終了のときだけ
- [`ADR-0008`](adr/0008-per-tool-approval-policy.ja.md) — ファンクション単位の
  承認ポリシー。プロジェクトは自由に締められ、緩められるのは信頼した場合だけ
- [`ADR-0009`](adr/0009-settings-panel.ja.md) — 出所を見せる設定パネルと、
  コメントを守るための機械所有ポリシーファイル
- [`ADR-0010`](adr/0010-skills.ja.md) — Claude Code の skill 形式をそのまま
  読む。skill の内容は指示であり、封じ込めた読み取りで境界づける
  （設置場所の項は 0011 が supersede）
- [`ADR-0011`](adr/0011-skill-scope-separation.ja.md) — skill は gem-agent
  自身のディレクトリに置く。Claude Code との共有は symlink
- [`ADR-0012`](adr/0012-image-input.ja.md) — 画像入力: オペレータ添付は @
  （クリップボード含む）、モデル閲覧は view_image
- [`ADR-0013`](adr/0013-navigation-tools.ja.md) — list_tree と
  search_files: 方向づけと高速 grep。索引なし・依存なし
- [`ADR-0014`](adr/0014-context-economy-tools.ja.md) — 軽量モデルの
  summarize_file と、read_file の行窓読み
- [`ADR-0015`](adr/0015-edit-file-v2.ja.md) — edit_file v2: 一括アトミック
  編集・診断つき不一致・成功時の証拠
- [`ADR-0016`](adr/0016-file-info.ja.md) — file_info: 内容判定の種別・
  メタデータ・MD5/SHA1/SHA256 三点セット
- [`ADR-0017`](adr/0017-web-tools.ja.md) — グラウンディング検索と要約
  フェッチ。既定でエグレスゲート、SSRF は構造的に死ぬ
- [`ADR-0018`](adr/0018-context-caching.ja.md) — セッションスコープの隔離
  タグで implicit caching が効く。実測 0% → 81〜95%
- [`ADR-0019`](adr/0019-usage-accounting.ja.md) — カテゴリ別の利用会計と
  /usage。側呼び出しがフッターを踏み潰さなくなる
- [`ADR-0020`](adr/0020-agent-memory.ja.md) — セッションをまたぐエージェント
  メモリ: 2 スコープ、リポ外の機械所有、信頼境界は書き込み
- [`ADR-0021`](adr/0021-review-fixes.ja.md) — 全体コードレビューの修正一括:
  トランスクリプトの clear/tear/lock、allowlist の床、スコープ優先
  ポリシー、実測で反証された 2 所見
- [`ADR-0022`](adr/0022-per-project-session-layout.ja.md) — セッションの
  プロジェクト別サブディレクトリ（メモリの規約を採用）、旧配置はその場で
  読み取り、GEMAGENT_STATE_DIR での隔離
- [`ADR-0023`](adr/0023-startup-safety.ja.md) — 起動時の安全機構: 広すぎる
  ルートは確認、初回信用の質問 1 つがプロジェクトの指示・.mcp.json・skills
  を覆う
- [`ADR-0024`](adr/0024-bottom-hold.ja.md) — bottom-hold: 画面が埋まったら
  フレーム合計高を保持し、フッターの弾みを止める（ADR-0003 の満杯時条項を
  supersede）
- [`ADR-0025`](adr/0025-thinking-level.ja.md) — メインモデルの Gemini 3
  思考レベル設定。要約モデルは対象外、対応レベルはモデル依存（実測）
- [`ADR-0026`](adr/0026-document-reading.ja.md) — 文書の読解: PDF は実測済み
  multimodal パートでネイティブ、Office XML はローカル抽出、レガシー
  バイナリは対象外
- [`ADR-0027`](adr/0027-audio-video.ja.md) — 音声・動画入力: バケット設定が
  常にインラインに勝つ（ラウンド再送の経済）、content-addressed
  アップロード、何も削除しない
- [`ADR-0028`](adr/0028-self-healing-line-counter.ja.md) — 印字行カウンタは
  現実に追従: 高すぎるフレームは端末をスクロールさせ、カウンタは溢れ分を
  自己修復する（ADR-0003 の定義を修正）
- [`ADR-0029`](adr/0029-ui-language.ja.md) — UI 言語モード:
  `[tui].language` auto/ja/en、カタログ構造体 1 つに完全なリテラル 2 面、
  完全性はテストで強制。ログ体裁とモデル向けテキストは英語のまま
- [`ADR-0030`](adr/0030-agent-self-info.ja.md) — `agent_info`: read-only
  自己情報ツール（モデル・コンテキスト占有・使用量・制限・プラット
  フォーム）。フィールドは「モデルの行動を変えるか」で選別 — GCP
  識別子とホスト名は非開示
- [`ADR-0031`](adr/0031-review-fixes-round-2.ja.md) — レビュー第 2 回:
  修正約 30 件（Msgs 配線・拒否バイパス・メディアストア汚染・
  flock 下 resume・ルーン安全補完・承認バジェット適応・flock 付き
  policy 変更・docext 合計上限）。400 主張 1 件を実測で反証。
  非変更 4 件を記録
- [`ADR-0032`](adr/0032-datetime-tool.ja.md) — `datetime`: 時計と
  カレンダー算術の read-only ツール 1 本（now/info/add/diff/convert）。
  月末正規化は明言・営業日は拒否。セッション開始日はキャッシュ安定な
  形でシステムプロンプトに載る
- [`ADR-0033`](adr/0033-turn-observability.ja.md) — ターン可観測性:
  ストリーム鼓動と失速警告・バックオフ再試行の可視化・揮発性の
  思考サマリ実況（表示のみ・保存しない）
- [`ADR-0034`](adr/0034-cancellation-deadlock.ja.md) — キャンセルは
  呼び出しを終わらせる: プロセスグループ kill + WaitDelay（パイプを
  握る孫がタイムアウトも Ctrl+C もハングさせた）、3 回押しの最終
  脱出口
- [`ADR-0035`](adr/0035-opentelemetry-audit.ja.md) — OpenTelemetry
  監査ロギング: OTLP ログイベント・既定 OFF・グローバル config 限定
  （エクスポーターはエグレス経路）・メタデータのみ・セッションを
  決して害さないテレメトリ
- [`ADR-0036`](adr/0036-ask-user-tool.ja.md) — `ask_user`: 承認
  ダイアログの操作文法によるターン途中の構造化選択。辞退は情報。
  全モードが正直に答える。自由入力は設計として無し
- [`ADR-0037`](adr/0037-agentic-file-search.ja.md) —
  `agentic_file_search`: 隔離された子コンテキストへの委任
  プロジェクト検索。読み取り専用許可リスト・再帰なし・ラベル付き
  テレメトリ。ADR-0014 の 1 ファイルから 1 質問への一般化
- [`ADR-0038`](adr/0038-risk-eval-instruction-context.ja.md) —
  自動承認モデル層はターン序盤だけ操作者のタイプ入力を見る。
  証拠としてラップ・不整合はエスカレーション・後半ラウンドは
  コール単体視点へバイト同一でフォールバック
- [`ADR-0039`](adr/0039-integration-reload.ja.md) — `/skills reload`
  と `/mcp reload` は起動時の経路と trust 判定を再利用。宣言と
  システムプロンプトが追随。単発パイプライン向け `--mcp on|off`。
  リロードは監査される
- [`ADR-0040`](adr/0040-round-limit-intervention.ja.md) — ラウンド
  上限を介入ラダーへ: ループ検出器・進捗レビュー・オペレーター
  ダイアログ（auto は高確信の裁定で自動続行）・どんな裁定も
  持ち上げられない 3 倍の天井・/clear ではなく「続けて」を教える
  停止メッセージ
- [`ADR-0041`](adr/0041-review-round-3.ja.md) — コード全体レビュー
  第 3 回: 所見 16 件、高 3 件（子エージェントのモデル作成 @ 参照
  展開・live 領域のタブ幅穴・2 本目の stdin reader）+ 停滞検知器・
  ask ダイアログ・監査の穴の修正
- [`ADR-0042`](adr/0042-terminal-diagrams.ja.md) — mermaid 図を端末に描画:
  実測で忠実な種別だけ（flowchart・ASCII sequence・ER）、形状は矩形に
  正規化、忠実性ガード、残りはソース表示（FIT 規則とプロンプト節は後に
  0063 が削除）
- [`ADR-0043`](adr/0043-diagram-tool.ja.md) — 図はツールが描く。モデルが書いたものを書き換えない（0063 が廃止）
- [`ADR-0044`](adr/0044-pre-tool-hooks.ja.md) — オペレーター pre-tool フック: 組織のガードをフォールバック後も生かす
- [`ADR-0045`](adr/0045-transcript-approval-learning.ja.md) — トランスクリプト駆動の承認ルール学習: `/learn` が提案し、オペレータが決める（0049 が撤収）
- [`ADR-0046`](adr/0046-mcp-description-risk-evidence.ja.md) — MCP ツール description をリスク評価の証拠に: オペレータが既に導入したものを評価器に教える
- [`ADR-0047`](adr/0047-declared-purpose.ja.md) — 承認対象コールに宣言された purpose: 「何を」だけでなく「なぜ」を運用者に見せる
- [`ADR-0048`](adr/0048-learning-that-fires.ja.md) — 実利用で発火する学習へ: MCP はサーバ単位グローバル、そして人が実際に出した回答を数える（0049 が撤収）
- [`ADR-0049`](adr/0049-learn-withdrawn.ja.md) — `/learn` を撤収する: 確認は緩和の恒久的な境界にならなかった
- [`ADR-0050`](adr/0050-risk-calibration.ja.md) — リスクルールブック: 判定器への積層ガイダンス。学習はその執筆手段の一つ
- [`ADR-0051`](adr/0051-destructive-rewrite-floors.ja.md) — 縮む全文書き換えは危険信号: 縮小ガード・再生成規則・コンパクション失効通知・ダイアログのサイズ差分
- [`ADR-0052`](adr/0052-ignore-aware-navigation.ja.md) — ignore を理解するナビゲーション: walk は生成物/ignore 対象を skip (組み込みリスト + 完全 gitignore 意味論・新規依存なし)、検索は「どこ」に答え、list_tree はディレクトリごとに予算配分 (ADR-0013 の前提を修正)
- [`ADR-0053`](adr/0053-one-shot-approval-controls.ja.md) — 単発モードの承認制御: `--auto` がヘッドレスで ADR-0004 の階梯を武装 (エスカレーションは理由つき拒否になり、config キーは無視のまま — 付与は起動に属する)、`--allow` は通常のポリシー構築を通る実行単位の `"never"` エントリ、SessionStart は実効 auto 状態を報告
- [`ADR-0054`](adr/0054-risk-context-every-round.ja.md) — リスク評価器は全ラウンドでオペレータ指示を見る: ADR-0038 §3 のカットオフを実トランスクリプトで実測 (評価の 70%・ターン終端ゲートコールの 63% がウィンドウ外) して撤廃。egress ルーブリック・層・confidence 基準は不変
- [`ADR-0055`](adr/0055-piped-stdin-as-data.ja.md) — 単発モードのパイプ stdin はノンスラップ付きテキスト添付 (`@` ファイルレーン) になり、プロンプト文には決してならない: リスク評価器の指示チャネルは `-p` 文字列のみのまま。上限つき読み取り + 開示クリップ、バイナリはスキップ、端末 stdin は読まない
- [`ADR-0056`](adr/0056-stall-warning-threshold.ja.md) — 失速警告が狼を叫んでいた: Gemini の function call は丸ごと 1 part で届くため、大きな書き込み/編集の引数生成中は実測で数十秒〜数分のあいだ 1 バイトも流れない (チャンク以前の問題)。しきい値を 20 秒 → 90 秒に移し、画面には何も足さない — 供給側の事情は ADR に書き、ステータス行には書かない。`/riskbook learn` の抑止フラグは `beginTurnStats` の後に立てる
- [`ADR-0057`](adr/0057-usage-accounting-records.ja.md) — すべてのモデル呼び出しが `usage` レコードを 1 行残す (source・model・prompt/output/thoughts/cached/total): API は金額を返さないのでコストは「トークン数 × カタログ単価」しかなく、カウントは呼び出し時点でディスクに落ちている必要がある。リスク評価と圧縮の消費はプロセスと共に消えていた。thoughts は output として課金され、cached は prompt の割引内訳 (どちらも実測)
- [`ADR-0058`](adr/0058-session-work-directory.ja.md) — セッションごとの作業ディレクトリ (state root 配下・session id で採番・resume は同じ場所に戻る): sandbox の書き込みルートかつファイルツールの第 2 ルートで、`GEMAGENT_WORK_DIR` として export される。MCP の結果だけが唯一上限のないツール出力で、file-mediated なサーバが軒並み `workspace_root` を持つに至った原因だった — サーバはモデルの context window を知り得ない。大きすぎる結果は切り捨てずここへ保存し、これまで黙って捨てていた非テキストコンテンツも保存して `view_image` に渡す
- [`ADR-0059`](adr/0059-workdirs-cleanup-command.ja.md) — `gem-agent workdirs` 一覧 + `clean`: ADR-0058 の蓄積 note の「掃除側」（対処なき報告は無視の訓練にしかならない）。確認が既定で deny-on-EOF、稼働セッションのディレクトリは transcript への共有 flock プローブで判別して決して消さず、掃除はプロジェクト単位・CLI 側 — ディスクを空けるのにモデルセッションを要してはならない
- [`ADR-0060`](adr/0060-deny-with-reason.ja.md) — 理由つき拒否・`N` 回答: 固定拒否文は「利用者が `n` を押した瞬間に知っていた理由」の入手にモデル 1 往復を費やしていて、拒否自身の function response がラウンド途中で API が開けている唯一のスロット（ADR-0012）。`n` は 1 打拒否のまま。拒否結果のアンラップは内容ではなくメッセージ出所で判定（拒否の形をしたツール出力はラップされたまま）。理由は `gate_decision` に残り、テレメトリには載せない
- [`ADR-0061`](adr/0061-independent-runtime-promotion.ja.md) — 独立エージェントランタイム: バックアップの憲章を退役（実戦投入が役割を超えた）、drop-in 互換は根拠をエコシステム互換に書き換えて最重要要件のまま、スコープ最小主義は「Claude Code の 2 割」でなく自前の憲章で立ち、訓練はオンデマンドの健全性チェックへ、そして利用者決定で cli-series へ昇格 — 訓練ベースの基準は合格ではなく失効
- [`ADR-0062`](adr/0062-delegation-first-exploration.ja.md) — 委任ファースト探索: 75 セッション・788 ツールコールで agentic_file_search の自発発火ゼロ — システムプロンプトが手動 list/search/read ループを名指しで規定し、ツールには一度も言及していなかった（説明文レベルの契機はプロンプトレベルのワークフローに勝てない）。プロンプトは探索をまず委任へ振り（自力ナビは既知ターゲット向け経路）、説明文に「報告は信頼せよ」を追加、配線はテストで固定
- [`ADR-0063`](adr/0063-diagram-fences-render-in-place.ja.md) — 図のフェンスはその場で描画し、ランタイムは図について何も語らない: 2 ヶ月の実測でツールの発火は 1 回、モデルは代わりに罫線アートを手描きしていた（フェンス禁止文の過剰一般化 — 具体的な禁止の隣の曖昧な推奨は「めったにやるな」と読まれる）。フェンス経路が表示層の書き換えとして復活、FIT ゲートは削除（はみ出しは折り返すだけで情報を失わない — 実測）、「間違い」ガードは維持、描こうとして失敗したブロックはソース + 読み手向け 1 行注記
- [`ADR-0064`](adr/0064-first-message-argument.ja.md) — 位置引数は対話セッションの第 1 ターン: `gem-agent "メッセージ"` はバナー後にタイプ経路そのもの（シェルエスケープ・スラッシュ・/skill・@ 参照）で送信され、発火は 1 回きり、`--continue`/`--resume`/`--auto` とは無変更で合成、`-p` との併用は拒否、ADR-0055 のパイプ stdin 境界は不変
- [`ADR-0065`](adr/0065-cancellation-in-process.ja.md) — キャンセルは呼び出しを終わらせる（第 2 部）: ファイル走査は context を参照して名前付きの部分結果を返し、全ツール呼び出しの下の復帰保証の床が 1 秒の猶予後に詰まった呼び出しを放棄（監査は `abandoned`・終了レシートで計数・遅延復帰は記録し変更系なら次ターンで告知・`ask_user` は除外）、3 回押しの脱出はしごが素 REPL と `-p` にも届き、終了は監査イベントの flush 中であることを告げる
- [`ADR-0066`](adr/0066-tool-prompt-usage-bucket.ja.md) — 4 つ目のバケツ: SDK は `totalTokenCount` を 4 つのカウントの和と定義しているのに ADR-0057 のチェックサムは 3 項だった — プローブ（主ループ）が組み込みツールを一度も有効にしないため。結果、ツールが内容を返した `web_search` / `web_fetch` の全レコードが、通るはずの検算に落ちていた。`usage` レコードは `tool_prompt` を持ち（常に書く・ゼロも含む・キーの不在が 0066 以前の印）、チェックサムは `prompt + output + thoughts + tool_prompt == total`、`model.usage` に `tool_prompt_tokens` が加わる
- [`ADR-0067`](adr/0067-piped-stdin-wait-notice.ja.md) — パイプ stdin を待つ単発実行は、待っていると言う: `-p` は引き続き端末でない stdin を EOF まで読む（遅い生産者を打ち切らない）が、2 秒経っても開いたままの pipe には両方の対処（パイプを閉じる、または `< /dev/null` で起動）を名指しする stderr 1 行が出て、告げた待機は終わりも見える — スケジューラやハーネスが子に渡す何も流れない継承 pipe は、もうハングには見えない
- [`ADR-0068`](adr/0068-telemetry-resource-declared-not-detected.ja.md) — テレメトリクライアントは Cloud Logging のリソースを探索せず宣言する: `backend = "gcp"` ではライブラリの Logger が GCE メタデータサーバーから取得してホストを分類しており、Mac ではそのリンクローカル取得が間欠的に 4.5〜7.2 秒の沈黙した起動を費やしていた（存在しない隣接ノードをカーネルが ARP 探索する間、ダイヤルタイムアウトが一時的エラーとして再試行される）。検出がフォールバックしていた `global` リソースを宣言し、構築はネットワークに触れず、待機は契約ではなかったので待機通知は加えない
- [`ADR-0069`](adr/0069-session-and-prompt-hooks.ja.md) — セッション開始フックとプロンプト送信フック: `[[hooks.session_start]]`（source は `startup`・`resume`・`/clear` での `clear`。任意の `matcher`）と `[[hooks.user_prompt_submit]]`（モデルに届く全ターン）が Claude Code の実測契約で走る — 同じ stdin ペイロード、素の stdout または `hookSpecificOutput.additionalContext` が文脈、exit 2 か JSON ブロック形式がプロンプトを拒否（消去され何も記録されない）、セッション開始は決してブロックできない — その出力はタイプ入力の隣のデータ attachment としてモデルに届き（ADR-0055 のレーン、8000 rune 上限、告知あり）、system prompt にもリスク評価器の信頼された指示チャネルにも決して入らない
- [`ADR-0070`](adr/0070-skill-directory-and-shared-writable-roots.ja.md) — ロードされた skill は自分のディレクトリを名乗り、ルール層の「書ける場所」は sandbox のものにする: `load_skill(name)` と `/skill <name>` は Claude Code と同じ行 `Base directory for this skill: <dir>`（読み取りが既に閉じ込められているシンボリックリンク解決済みディレクトリ）で始まり、Claude Code の `SKILL_DIR` 契約で書かれた `SKILL.md` はディスクを探索せずに gem-agent のグローバル skill ディレクトリからスクリプトを走らせられる。`sandbox.ScratchDirs()` がプロファイルとリダイレクト規則の両方が読む唯一の scratch ルート一覧（`TMPDIR`・`/private/tmp`・`/dev`）となり、`2>/dev/null` はもうルート外への書き込みには読まれない。そして `/`・`~`・ルート外の絶対パスを起点にする read-only の木走査（`find`・`fd`・`du`・`rg`・再帰 `grep`）は Safe ではなく Review — sandbox の読み取り側は意図的に開いているので走査のコストはマウントのものであり、コストはモデル層が量る
- [`ADR-0071`](adr/0071-session-identity-contract.ja.md) — **Proposed。** Claude Code と揃えたセッション識別契約: UUID v4 のセッション id（タイムスタンプ id も引き続き一覧・resume 可。`--resume` は曖昧でない前方一致）、`/clear` は新セッションを開始（`SessionEnd` の後 `SessionStart`）、プロジェクトは `GEMAGENT_PROJECT_DIR` として export されるディレクトリ（発行・保存・git 由来のプロジェクト識別子は持たない）、`[[hooks.session_end]]` がイベントに加わる。state 配置・トランスクリプト形式・ペイロードの形は不変

## History（履歴）

supersede された文書の凍結された audit trail。まだ空である（supersede されたものが
無い）。発生したら「何に置き換わったか」の注記を付けてここへ移す — 削除はしない。
supersede された文書の議論が、現在の設計がその形である理由の唯一の記録である
ことが多いからである。
