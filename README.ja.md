# gem-agent

Vertex AI Gemini 3.x をバックエンドとする CLI 対話型エージェント — Claude Code が
利用できない状況での開発作業継続手段。

> **Status: pre-release（scaffold 段階）。** エージェントループは未実装です。
> 完全な仕様は [RFP](docs/ja/gem-agent-rfp.ja.md) を参照してください。

[English README](README.md)

## Why

Claude Code が使えない状況（プロバイダ側障害・契約やネットワークの制約）でも
開発作業を止めないためのツールです。独立したバックエンド（Vertex AI）上の
意図的に最小限なフォールバックエージェントで、**drop-in** を最重要要件と
します: 既存プロジェクトの `AGENTS.md` / `CLAUDE.md` / `.mcp.json` をそのまま
解釈するため、乗り換えにプロジェクト単位の再設定は不要です。

## 予定機能（v0.1.0）

- Gemini 3.x エージェントループの対話 REPL（ストリーミング、thought signature 対応）
- 組み込みツール: `list_files` / `read_file` / `write_file` / `edit_file` / `shell_exec`
- 都度承認ゲート（MITL）+ セッション内 allowlist
- `shell_exec` の macOS sandbox-exec ラップ（書き込みをプロジェクトディレクトリに制限）
- MCP クライアント（stdio、Claude Code の `.mcp.json` 互換）
- 単発実行モード（`-p "<prompt>"`）

設計上のスコープ外: メモリサブシステム、コンテキスト圧縮、データ分析、GUI、
セッション resume、macOS 以外のプラットフォーム。

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

`~/.config/gem-agent/config.toml`（予定）:

```toml
[gcp]
project  = "your-project-id"
location = "us-central1"

[model]
name = "<gemini-3.x model id>"
```

環境変数の優先順位: `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file > defaults。

## ドキュメント

- [RFP（日本語）](docs/ja/gem-agent-rfp.ja.md) / [RFP (English)](docs/en/gem-agent-rfp.md)
- [ADR-0001: サンドボックス方式](docs/ja/adr/0001-sandbox-mechanism.ja.md)

## ライセンス

MIT
