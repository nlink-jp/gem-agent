# gem-agent

Vertex AI Gemini 3.x をバックエンドとする CLI エージェントランタイム —
意図的に最小限・サンドボックス化・対話とヘッドレスの両用。

> **リリース済。** `brew install nlink-jp/tap/gem-agent` または
> [リリースページ](https://github.com/nlink-jp/gem-agent/releases)から
> 導入できます（Developer ID 署名 + Apple notarize 済み、macOS arm64）。
> 現在の版はリリースページが正であり、ここには書きません（腐るため）。
> cli-series のツールであり、インターフェースの安定性は約束です。破壊的
> 変更は組織の破壊的変更プロセスを通ります（ADR-0061）。
> 完全な仕様は [RFP](docs/ja/gem-agent-rfp.ja.md) を参照。

[English README](README.md)

## Why

gem-agent は独立したエージェントランタイムです: Vertex AI Gemini 上の
最小限で監査可能なエージェントループ（read / edit / shell / MCP / 承認）を、
二層（sandbox-exec による封じ込め + human-in-the-loop 承認）で防御し、
分析・GUI サブシステムは持ちません。プロジェクトエコシステムとの
**drop-in** 互換を最重要要件とします: 既存プロジェクトの `AGENTS.md` /
`CLAUDE.md` / `.mcp.json`（および Claude Code 形式の skills）をそのまま
解釈するため、1 つのプロジェクト設定がその上で動くすべてのランタイムに
仕えます。出自は Claude Code のバックアップであり、実戦投入がその役割を
超えた時点で独立ランタイムとして再位置づけされました
（[ADR-0061](docs/ja/adr/0061-independent-runtime-promotion.ja.md)）。

## クイックスタート

```sh
brew install nlink-jp/tap/gem-agent
mkdir -p ~/.config/gem-agent
cp config.example.toml ~/.config/gem-agent/config.toml   # [gcp].project と [model].name を設定
```

```sh
cd /path/to/your/project
gem-agent                                  # 対話 REPL
gem-agent "テストを回して"                   # 対話モード、これが第1ターン
gem-agent -c                               # ここでの最新セッションを再開
gem-agent sessions                         # 再開可能なセッション一覧
gem-agent -p "このリポジトリを要約して"      # 単発実行、パイプ向け
```

カレントディレクトリがプロジェクトになります: ファイルツールはそこから
出られず、サンドボックス化されたシェルコマンドは外側に書き込めず、
変更系ツールコールは実行前に承認を求めます。さらにセッションごとに
専用の作業ディレクトリが用意され、プロジェクトの一部でないもの——
中間データ、大きすぎるツール結果、サーバーが返した画像——はそちらに
落ちるので、作業コピーは汚れません。`/status` が場所を表示し、
中身が自動で削除されることはありません — 過去セッションの残置分は
`gem-agent workdirs` で一覧し、`workdirs clean` が対象を提示して
確認の上で削除します。動作要件は macOS
（Apple Silicon）、Vertex AI が有効な Google Cloud プロジェクト、ADC
（`gcloud auth application-default login`）— 詳細は
[設定](docs/ja/reference/configuration.ja.md)。

## 何ができるか

以下の各行は [docs/ja/reference/](docs/ja/INDEX.ja.md) 配下の分冊で
詳述されています。判断の理由は ADR にあります。

**[インターフェース](docs/ja/reference/interface.ja.md)** — 素の
スクロールバックが生きた inline Bubble Tea TUI、実行中も生きている
最下部固定の入力欄（Enter は次メッセージの予約）、ライブなターン
ステータス — ストリーム鼓動・失速警告・リトライの可視化・思考中の
モデルの思考サマリ実況 — 必ず戻ってくる Ctrl+C（ファイル走査は
syscall 1 回分で止まり、それ以外は有界の猶予後に放棄 — ADR-0065）、
日本語 IME に優しい承認ダイアログ、`@` パス・`/` コマンド・スキル名の Tab 補完、
`!コマンド` シェルエスケープ、回答中の mermaid フェンスのその場描画
（flowchart / ASCII ラベルの sequence / ER。端末が忠実に描けない
ものはソースのまま）、13 のスラッシュコマンド（`/help`
`/tools` `/mcp` `/auto` `/compact` `/settings` `/usage` `/memory`
`/skills` `/skill` `/version` `/clear` `/quit`）、出所ファーストの `/settings`
パネル、テーマ、完全二言語のクローム（`[tui].language = auto|ja|en`）。
位置引数は対話セッションの第 1 ターン — `gem-agent "…"` はそれを
実行してからキーボードを渡します（ADR-0064）。
パイプは素の REPL に、`-p` は単発実行に（変更系ツールは拒否 —
`--allow` で実行単位のツール名指し付与、`--auto` でリスク階梯の
武装ができます。ADR-0053）。`データ | gem-agent -p "…"` のパイプ
stdin は隔離データとして添付され、プロンプト文にはなりません
（ADR-0055）。パイプは EOF まで読みます。2 秒経っても開いたままなら
stderr に 1 行出して対処を示します — 何も添付しない起動は
`< /dev/null` を付けてください（ADR-0067）。

**[組み込みツール](docs/ja/reference/tools.ja.md)** — 方向づけ
（`list_files`/`list_tree`/`search_files`、ignore 対応: 依存・ビルド
ディレクトリと `.gitignore` 対象はスキップされ、skip は必ず報告）、
窓読みと要約
（`read_file`/`summarize_file`）、隔離された子コンテキストでの
委任プロジェクト検索（`agentic_file_search`）、診断つきアトミック一括編集
（`edit_file`/`write_file` — 全文書き換えが文書を黙って要約消滅させ
ないための縮小ガード付き）、ハッシュ付きファイル同定
（`file_info`）、モデルのための画像と文書
（`view_image`/`read_document`）、read・write・operator のレーンをカーネルが
強制するサンドボックス化シェル（`shell_exec`）、決定的な時計とカレンダー（`datetime`）、モデル自身の
ランタイム像（`agent_info`）、ターン途中の構造化選択（`ask_user`）、出典付き Web アクセス
（`web_search`/`web_fetch`）。

**[添付](docs/ja/reference/attachments.ja.md)** — `@file`・`@dir/`・
スクリーンショット（`@~/Desktop/…`・`@clipboard`）・PDF と Office
文書・音声/動画 — バケット設定時は GCS 経由、未設定ならインライン。

**[承認と安全](docs/ja/reference/approval.ja.md)** — モデル自身が申告した
コールの目的を引数と並べて表示する都度 MITL ゲート（Block 段には
決して届かないセッション allowlist 付き）、1 行の「代わりにこうして」を
拒否そのものに乗せてモデルへ届ける理由つき拒否回答（`N`）、opt-in の
二層自動承認（ルール先行・モデルレビュー後段 — `AGENTS.md` や `.mcp.json`
など指示・設定ファイルへの編集は必ずあなたに確認し、信頼したプロジェクトの指示・
設定ファイルは内容でピン留めされ変化すれば読み込む前に再確認）、文字列ではなく宣言した
Seatbelt レーンで判定されるシェルコマンド（read レーンは無確認で走り、write
レーンは `AGENTS.md` や `.git/config` に触れられず、operator レーンはあなた
だけのもの）、スコープ対応解決と
信用ゲート付きプロジェクト緩和を持つツール別承認ポリシー、auto モード
の評価器が読む積層リスクルールブック — 手書きでも自分の回答記録から
の起草でも作れ、ゲートは決して飛ばさない — Seatbelt
サンドボックス、Claude Code と同じガードスクリプトを実行して階梯より
先にコールを拒否できるオペレーター pre-tool フック、同じ契約で出力が
ラベル付きデータとしてモデルに届くセッション開始・プロンプト送信
フック、広すぎるルートと初見プロジェクトの起動時ゲート、
全ツール出力のノンスタグ隔離 — これがリクエストプレフィックスを
バイト安定に保ち、実測 81〜95% のコンテキストキャッシュ命中も
もたらします。

**[セッション](docs/ja/reference/sessions.ja.md)** — 完全忠実な JSONL
トランスクリプト、プロジェクト別の状態配置、意図的な拒否（別
ディレクトリ・別モデル）を持つ `--continue`/`--resume`（UUID セッション id、前方一致で resume 可）、正直な通知
付きの自動コンテキスト圧縮、`/usage` トークン明細、承認ゲート付きの
セッション横断メモリ。

**[統合](docs/ja/reference/integration.ja.md)** — ディレクトリツリーを
遡る `AGENTS.md`/`AGENT.md`/`CLAUDE.md`/`GEMINI.md` の drop-in 読取、
Claude Code 形式 `.mcp.json` の MCP サーバー（グローバル +
プロジェクト）、progressive disclosure の Claude Code 形式スキル
（ロードした skill は Claude Code と同じく自分のディレクトリを名乗る
ので自前のスクリプトが走る）— どちらもセッション中に再読込可能
（`/mcp reload`・`/skills reload`）、
`--mcp on|off` で実行単位の MCP 切替。

**[設定](docs/ja/reference/configuration.ja.md)** — 設定リファレンス
全体、優先順位、CLI フラグ、コンテンツフィルタの挙動、エンドポイント
の注意、そして opt-in の監査ロギング — Cloud Logging（既定）または
OTLP コレクタへ（メタデータのみ・会話内容は送らない・起動時間は
増えない）。

設計上のスコープ外: RAG・ベクトルメモリ、データ分析、GUI、macOS 以外の
プラットフォーム。

## ビルド

```sh
make build    # dist/gem-agent に出力
make test
```

## ドキュメント

入口は [**docs/ja/INDEX.ja.md**](docs/ja/INDEX.ja.md)
（[English](docs/en/INDEX.md)）— 複数箇所で並列維持する一覧ではなく、
カタログ 1 箇所を正とする構成です。[RFP](docs/ja/gem-agent-rfp.ja.md)
（仕様の正本）、上でリンクした機能分冊 7 本、
[アーキテクチャ](docs/ja/reference/architecture.ja.md)、
[月次訓練](docs/ja/reference/drill.ja.md)、
[昇格基準](docs/ja/reference/promotion.ja.md)、そして全 ADR を
収録します。

## ライセンス

MIT
