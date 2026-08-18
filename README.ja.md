# gem-agent

Vertex AI Gemini 3.x をバックエンドとする CLI 対話型エージェント — Claude Code が
利用できない状況での開発作業継続手段。

> **Status: pre-release（開発 Phase 2 完了）。** エージェントループ・MCP
> クライアント・drop-in プロジェクト互換のすべてが Vertex AI に対して
> end-to-end で動作します（Gemini 3.7 で実機検証済み）。リリース前の残り
> （Phase 3）: 実プロジェクト E2E、訓練手順書、パッケージング。
> 完全な仕様は [RFP](docs/ja/gem-agent-rfp.ja.md) を参照してください。

[English README](README.md)

## Why

Claude Code が使えない状況（プロバイダ側障害・契約やネットワークの制約）でも
開発作業を止めないためのツールです。独立したバックエンド（Vertex AI）上の
意図的に最小限なフォールバックエージェントで、**drop-in** を最重要要件と
します: 既存プロジェクトの `AGENTS.md` / `CLAUDE.md` / `.mcp.json` をそのまま
解釈するため、乗り換えにプロジェクト単位の再設定は不要です。

## 機能（実装済み）

- **対話 TUI**（Bubble Tea、inline 方式 — 素のスクロールバックとコピペが
  そのまま使える）: スピナー/ステータス付きストリーミング表示、↑↓ 履歴と
  複数行編集（Ctrl+J）付き入力ボックス、色分けされたツールイベント、承認
  ダイアログ、glamour による Markdown 整形描画。パイプ/スクリプト利用時は
  自動的に素の行 REPL にフォールバック
- Gemini エージェントループ — Gemini 3 thought signature の capture/replay
  （実機検証済み）
- 組み込みツール: `list_files` / `read_file` / `write_file` / `edit_file` / `shell_exec`
  （すべてプロジェクトディレクトリに封じ込め。シンボリックリンク経由の脱出も検査）
- 変更系ツールの都度承認ゲート（MITL）+ セッション内 allowlist
  （`y` = 1回、`a` = このセッションでは常に許可。拒否側に倒れる設計）
- **自動承認モード**（opt-in、shift+tab または `/auto`）: 変更系コールごとに
  二層レビュー — まずルールベース分類器、不確実なものだけモデルによるリスク
  評価。安全なものは無人実行、破壊的・プロジェクト外・認証情報関連・不確実な
  ものは従来どおり確認。詳細は [ADR-0004](docs/ja/adr/0004-auto-approve.ja.md)
- `shell_exec` の macOS sandbox-exec ラップ — ファイル書き込みをプロジェクト
  ディレクトリ + scratch に制限（実 Seatbelt での強制テスト付き）
- ペースト安全な入力: 複数行ペーストは 1 つの入力になる（行ごとに LLM コールが
  飛ぶことはない）
- JSONL セッションログ（`~/.local/state/gem-agent/sessions/`）
- スラッシュコマンド: `/help` `/tools` `/mcp` `/clear` `/quit`
- **drop-in プロジェクト互換**: プロジェクトの `AGENTS.md` / `CLAUDE.md` を
  システムプロンプトに注入し、`.mcp.json`（Claude Code 形式、stdio サーバー）を
  そのまま接続 — プロジェクト単位の追加設定ゼロ
- MCP クライアント: ツールは `mcp__<server>__<tool>` として常に承認ゲート付き。
  タイムアウトした呼び出しはサーバー子プロセスを kill（MCP にキャンセルは無い）、
  次回呼び出しで遅延再起動
- ツール出力は呼び出しごとのノンス XML タグ（nlk/guard）で隔離 — ツールが返す
  内容は常にデータであり指示ではない、という枠付けを強制
- 単発実行モード `-p "<prompt>"`: 1 ターンで終了、回答は stdout、変更系ツールは
  拒否（パイプ向け）
- Vertex の一時障害（429/5xx）は指数バックオフでリトライ

設計上のスコープ外: メモリサブシステム、コンテキスト圧縮、データ分析、GUI、
セッション resume、macOS 以外のプラットフォーム。

## 使い方

```sh
cd /path/to/your/project
gem-agent                                  # 対話 REPL
gem-agent -p "このリポジトリを要約して"      # 単発実行、パイプ向け
```

カレントディレクトリがプロジェクトになります: ファイルツールはそこから出られず、
サンドボックス化されたシェルコマンドは外側に書き込めません。変更系ツールは実行前に
承認ダイアログが出ます。回答は**選択式**（←→ または Tab で移動、Enter で決定）と
**ショートカット**（`y` = 1回 / `n` = 拒否 / `a` = このセッションでは常に許可、
Esc = 拒否）のどちらでも可能です。選択式があるのは、**日本語 IME が ON のままだと
y/n/a が変換に吸われて入力できない**ためです。初期選択は「許可」ですが、自動承認が
エスカレーションしたコールでは「拒否」から始まり、反射的な Enter で危険な操作を
通してしまわないようにしています。`--no-sandbox` は Seatbelt ラップの無効化
（デバッグ専用）、`--model` はモデルの上書きです。

TUI キー操作（`/help` にも一覧あり）: Enter 送信、Ctrl+J 改行挿入、↑/↓ 入力
履歴、Ctrl+C 実行中ターンの中断（入力中はクリア）、Ctrl+D 終了。複数行
ペーストは 1 つのメッセージとして入力ボックスに入ります。

### 自動承認モード

既定は OFF。shift+tab（または `/auto`）で切り替え、ON の間はステータスバーに
`⚡auto` が出ます。ON のとき変更系ツールコールは次を通ります:

1. **ルール層**（モデル呼び出しなし）: *safe* → 実行、*block* → 必ず確認
   （`rm -rf`・`sudo`・`git push`・ダウンロードのシェル直パイプ・ディスク
   書き込み・認証情報パス・プロジェクト外など）、*不確実* → 第 2 層へ
2. **モデル層**: 別の評価ラウンドが対象コールを判定（コールはノンス隔離した
   非信頼データとして渡し、この評価ラウンドにはツールを一切与えない）。
   承認 **かつ** 高確信のときだけ自動実行、それ以外は確認へ

失敗系（モデルエラー・壊れた verdict・未知ツール）はすべて確認へ倒れます。
block 層はモデルが覆せない硬い床で、sandbox はどのモードでも有効です。

**どちらの結果も理由が表示されます**: 自動承認されたコールは理由付きで表示され、
確認に回されたコールは承認ダイアログに `⚠` 行が付き、**どの層が止めたのか**まで
分かります — 決定論的な床なら `blocked by rule (always asks): …`、モデルの
判断なら `escalated by risk review: …`。

`!<コマンド>` でシェルコマンドを直接実行できます — `shell_exec` と同じ
sandbox 下（タイムアウト・出力上限も共通）ですが、自分で打ったコマンドなので
承認プロンプトは出ません。コマンドと出力はモデルの文脈にも追加されるため、
`!git status` してから「これを直して」という流れがそのまま成立します。

入力欄とステータスバーは Claude Code と同様に**ウィンドウ最下部に固定**され、
会話はその上を流れます（素のスクロールバックは維持。ADR-0003; 起動時に画面を
1 回クリアします）。ステータスバーには使用モデル・コンテキスト使用量と
ウィンドウサイズ（モデルメタデータから自動検出、`[model].context_window` で
上書き可）・累計消費トークン・プロジェクトディレクトリを表示します。

## MCP サーバー

サーバー定義は 2 つのスコープから読み込みます。どちらも Claude Code の
`.mcp.json` 形式（stdio トランスポート、`${VAR}` 展開対応）なので、エントリを
そのまま移動できます:

| スコープ | パス | 用途 |
|---|---|---|
| グローバル | `~/.config/gem-agent/mcp.json` | 全プロジェクトで使うサーバー |
| プロジェクト | `<project>/.mcp.json` | そのリポジトリ固有のサーバー |

どちらも任意です。両者はマージされ、**名前が衝突した場合はプロジェクト側が
優先**されます。`/mcp` で接続中のサーバーがスコープ付きで一覧表示されます。

```json
{
  "mcpServers": {
    "tor-exit": { "command": "tor-exit-lookup", "args": ["mcp"] }
  }
}
```

ガバナンスと監査証跡を付けたい場合は
[mcp-guardian](https://github.com/nlink-jp/mcp-guardian) を経由させます —
guardian 自体が stdio MCP サーバーなので、opt-in は `.mcp.json` のエントリ
1 つで済みます:

```json
{
  "mcpServers": {
    "guarded": { "command": "mcp-guardian", "args": ["--profile", "myserver"] }
  }
}
```

## 動作要件

- macOS（Apple Silicon）
- Vertex AI が有効な Google Cloud プロジェクト
- Application Default Credentials（`gcloud auth application-default login`）
  と `roles/aiplatform.user`

## ビルド

```sh
make build    # dist/gem-agent に出力
make test
```

## 設定

`~/.config/gem-agent/config.toml`:

```toml
[gcp]
project  = "your-project-id"
location = "global"        # デフォルト; Gemini 3 系は global エンドポイント専用

[model]
name = "<gemini model id>"
# context_window = 1048576  # 任意; フッター表示の上書き（デフォルト: 自動検出）

[sandbox]
enabled = true             # デフォルト

[agent]
max_turns = 50             # デフォルト
shell_timeout_sec = 120    # デフォルト
auto_approve = false       # デフォルト; 起動時から自動承認モードにする

[tui]
theme = "auto"             # auto | dark | light | plain
```

TUI のアクセント色は ANSI 16 色パレット（ターミナルテーマに追従）、フッターや
ヒントなどの控えめテキストは背景の明暗判定に応じた中間グレーを使い、どの
テーマでも背景との輝度差を確保します。`theme = "auto"` は起動時に明暗を判定。
判定が合わない場合は `dark`/`light` を明示、どうしても合わないテーマでは
`plain` で全装飾を無効化できます（エラーは `✗` マーカー付きなので色に
依存しません）。

優先順位: フラグ（`--model`）> `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file
> defaults。設定ファイル内の未知キーはエラーになります（strict decode）。

注: 2026-08 時点で Gemini 3 系（gemini-3.7-flash / gemini-3-flash-preview で
実測）は global エンドポイントからのみ提供されており、リージョナル指定は 404 に
なります。Gemini 2.5 系は `us-central1` 等のリージョナルで動作するので、使う
場合は `location` をそちらに設定してください。

## ドキュメント

- [RFP（日本語）](docs/ja/gem-agent-rfp.ja.md) / [RFP (English)](docs/en/gem-agent-rfp.md)
- [ADR-0001: サンドボックス方式](docs/ja/adr/0001-sandbox-mechanism.ja.md)

## ライセンス

MIT
