# gem-agent

Vertex AI Gemini 3.x をバックエンドとする CLI 対話型エージェント — Claude Code が
利用できない状況での開発作業継続手段。

> **リリース済。** `brew install nlink-jp/tap/gem-agent` または
> [リリースページ](https://github.com/nlink-jp/gem-agent/releases)から
> 導入できます（Developer ID 署名 + Apple notarize 済み、macOS arm64）。
> 現在の版はリリースページが正であり、ここには書きません（腐るため）。
> 実験的ツール（lab-series）であり、リリース間でインターフェースが変わる
> ことがあります。完全な仕様は [RFP](docs/ja/gem-agent-rfp.ja.md) を参照。

[English README](README.md)

## Why

Claude Code が使えない状況（プロバイダ側障害・契約やネットワークの制約）でも
開発作業を止めないためのツールです。独立したバックエンド（Vertex AI）上の
意図的に最小限なフォールバックエージェントで、**drop-in** を最重要要件と
します: 既存プロジェクトの `AGENTS.md` / `CLAUDE.md` / `.mcp.json` をそのまま
解釈するため、乗り換えにプロジェクト単位の再設定は不要です。

## クイックスタート

```sh
brew install nlink-jp/tap/gem-agent
mkdir -p ~/.config/gem-agent
cp config.example.toml ~/.config/gem-agent/config.toml   # [gcp].project と [model].name を設定
```

```sh
cd /path/to/your/project
gem-agent                                  # 対話 REPL
gem-agent -c                               # ここでの最新セッションを再開
gem-agent sessions                         # 再開可能なセッション一覧
gem-agent -p "このリポジトリを要約して"      # 単発実行、パイプ向け
```

カレントディレクトリがプロジェクトになります: ファイルツールはそこから
出られず、サンドボックス化されたシェルコマンドは外側に書き込めず、
変更系ツールコールは実行前に承認を求めます。動作要件は macOS
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
モデルの思考サマリ実況 — 日本語 IME に優しい
承認ダイアログ、`@` パス・`/` コマンド・スキル名の Tab 補完、
`!コマンド` シェルエスケープ、mermaid の flowchart / ASCII ラベルの
sequence / ER 図を端末に描き、描けなかったときは理由をモデルに返す
`render_diagram` ツール、12 のスラッシュコマンド（`/help`
`/tools` `/mcp` `/auto` `/compact` `/settings` `/usage` `/memory`
`/skills` `/skill` `/clear` `/quit`）、出所ファーストの `/settings`
パネル、テーマ、完全二言語のクローム（`[tui].language = auto|ja|en`）。
パイプは素の REPL に、`-p` は単発実行に。

**[組み込みツール](docs/ja/reference/tools.ja.md)** — 方向づけ
（`list_files`/`list_tree`/`search_files`）、窓読みと要約
（`read_file`/`summarize_file`）、隔離された子コンテキストでの
委任プロジェクト検索（`agentic_file_search`）、診断つきアトミック一括編集
（`edit_file`/`write_file`）、ハッシュ付きファイル同定
（`file_info`）、モデルのための画像と文書
（`view_image`/`read_document`）、サンドボックス化シェル
（`shell_exec`）、決定的な時計とカレンダー（`datetime`）、モデル自身の
ランタイム像（`agent_info`）、ターン途中の構造化選択（`ask_user`）、出典付き Web アクセス
（`web_search`/`web_fetch`）。

**[添付](docs/ja/reference/attachments.ja.md)** — `@file`・`@dir/`・
スクリーンショット（`@~/Desktop/…`・`@clipboard`）・PDF と Office
文書・音声/動画 — バケット設定時は GCS 経由、未設定ならインライン。

**[承認と安全](docs/ja/reference/approval.ja.md)** — Block 段には
決して届かないセッション allowlist 付きの都度 MITL ゲート、opt-in の
二層自動承認（ルール先行・モデルレビュー後段）、スコープ対応解決と
信用ゲート付きプロジェクト緩和を持つツール別承認ポリシー、Seatbelt
サンドボックス、Claude Code と同じガードスクリプトを実行して階梯より
先にコールを拒否できるオペレーター pre-tool フック、広すぎるルートと
初見プロジェクトの起動時ゲート、
全ツール出力のノンスタグ隔離 — これがリクエストプレフィックスを
バイト安定に保ち、実測 81〜95% のコンテキストキャッシュ命中も
もたらします。

**[セッション](docs/ja/reference/sessions.ja.md)** — 完全忠実な JSONL
トランスクリプト、プロジェクト別の状態配置、意図的な拒否（別
ディレクトリ・別モデル）を持つ `--continue`/`--resume`、正直な通知
付きの自動コンテキスト圧縮、`/usage` トークン明細、承認ゲート付きの
セッション横断メモリ。

**[統合](docs/ja/reference/integration.ja.md)** — ディレクトリツリーを
遡る `AGENTS.md`/`AGENT.md`/`CLAUDE.md`/`GEMINI.md` の drop-in 読取、
Claude Code 形式 `.mcp.json` の MCP サーバー（グローバル +
プロジェクト）、progressive disclosure の Claude Code 形式スキル —
どちらもセッション中に再読込可能（`/mcp reload`・`/skills reload`）、
`--mcp on|off` で実行単位の MCP 切替。

**[設定](docs/ja/reference/configuration.ja.md)** — 設定リファレンス
全体、優先順位、CLI フラグ、コンテンツフィルタの挙動、エンドポイント
の注意、そして opt-in の監査ロギング — Cloud Logging（既定）または
OTLP コレクタへ（メタデータのみ・会話内容は送らない）。

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
