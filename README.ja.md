# gem-agent

Vertex AI Gemini 3.x をバックエンドとする CLI 対話型エージェント — Claude Code が
利用できない状況での開発作業継続手段。

> **Status: pre-release（開発 Phase 1 完了）。** 対話エージェントループは
> Vertex AI に対して end-to-end で動作します（Gemini 2.5 / Gemini 3 で実機
> 検証済み）。MCP / `.mcp.json` / AGENTS.md 注入は Phase 2 で追加されます。
> 完全な仕様は [RFP](docs/ja/gem-agent-rfp.ja.md) を参照してください。

[English README](README.md)

## Why

Claude Code が使えない状況（プロバイダ側障害・契約やネットワークの制約）でも
開発作業を止めないためのツールです。独立したバックエンド（Vertex AI）上の
意図的に最小限なフォールバックエージェントで、**drop-in** を最重要要件と
します: 既存プロジェクトの `AGENTS.md` / `CLAUDE.md` / `.mcp.json` をそのまま
解釈するため、乗り換えにプロジェクト単位の再設定は不要です。

## 機能（Phase 1、実装済み）

- Gemini エージェントループの対話 REPL — ストリーミング出力、Gemini 3
  thought signature の capture/replay（実機検証済み）
- 組み込みツール: `list_files` / `read_file` / `write_file` / `edit_file` / `shell_exec`
  （すべてプロジェクトディレクトリに封じ込め。シンボリックリンク経由の脱出も検査）
- 変更系ツールの都度承認ゲート（MITL）+ セッション内 allowlist
  （`y` = 1回、`a` = このセッションでは常に許可。拒否側に倒れる設計）
- `shell_exec` の macOS sandbox-exec ラップ — ファイル書き込みをプロジェクト
  ディレクトリ + scratch に制限（実 Seatbelt での強制テスト付き）
- ペースト安全な入力: 複数行ペーストは 1 つの入力になる（行ごとに LLM コールが
  飛ぶことはない）
- JSONL セッションログ（`~/.local/state/gem-agent/sessions/`）
- スラッシュコマンド: `/help` `/tools` `/clear` `/quit`

## 予定（Phase 2）

- MCP クライアント（stdio、Claude Code の `.mcp.json` 互換）+ mcp-guardian opt-in
- AGENTS.md / CLAUDE.md のシステムプロンプト注入（drop-in 互換）
- nlk/guard ノンス隔離、単発実行モード（`-p`）、429 backoff

設計上のスコープ外: メモリサブシステム、コンテキスト圧縮、データ分析、GUI、
セッション resume、macOS 以外のプラットフォーム。

## 使い方

```sh
cd /path/to/your/project
gem-agent
```

カレントディレクトリがプロジェクトになります: ファイルツールはそこから出られず、
サンドボックス化されたシェルコマンドは外側に書き込めません。変更系ツールは実行前に
承認プロンプトが出ます。`--no-sandbox` は Seatbelt ラップの無効化（デバッグ専用）、
`--model` はモデルの上書きです。

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
location = "us-central1"   # Gemini 3 プレビューモデルは "global" が必要

[model]
name = "<gemini model id>"

[sandbox]
enabled = true             # デフォルト

[agent]
max_turns = 50             # デフォルト
shell_timeout_sec = 120    # デフォルト
```

優先順位: フラグ（`--model`）> `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file
> defaults。設定ファイル内の未知キーはエラーになります（strict decode）。

注: 2026-08 時点で Gemini 3 プレビューモデルはリージョナルエンドポイントから
提供されていません — `location = "global"` を指定してください。Gemini 2.5 系は
`us-central1` 等のリージョナルエンドポイントで動作します。

## ドキュメント

- [RFP（日本語）](docs/ja/gem-agent-rfp.ja.md) / [RFP (English)](docs/en/gem-agent-rfp.md)
- [ADR-0001: サンドボックス方式](docs/ja/adr/0001-sandbox-mechanism.ja.md)

## ライセンス

MIT
