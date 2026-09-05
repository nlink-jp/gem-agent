# ADR-0001: サンドボックス方式 — sandbox-exec + MITL の二層防御

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | エージェントループ実装（開発 Phase 1）の前に `shell_exec` の隔離方式を決める必要がある |

*ADR-0073 による修正: 1 つのプロファイルはコール単位で選ばれ同じ機構で
強制される 3 レーン（read・write・operator）になった。起動時に検証された
read レーンの `shell_exec` は非変更系で MITL の確認なしに走る。非封じ込め
実行（`--no-sandbox`）は劣化した既定ではなく操作者専用のモード。*

## Context

gem-agent は LLM が提案したシェルコマンドをオペレータのマシン上で実行する。
組織のエージェント PoC である agent-skeleton の外部レビューで、ヒューリスティック
なパス検証（PathGuard 方式）では動的パス構築を防げないことが確立している —
`$(echo /etc/passwd)` の類いは文字列レベルの検査をすべて通過する。人間の承認に
加えて、OS レベルの構造的な境界が必要である。

本ツールは Claude Code のバックアップであり、障害発生時に macOS 上で即座に、
インフラのウォームアップなしに起動できなければならない。

## Decision

役割の異なる二層で防御する:

1. **一次防御 — MITL 承認ゲート。** `write_file` / `edit_file` / `shell_exec` /
   MCP ツールは都度承認とし、セッションスコープ（非永続）の allowlist を持つ。
   これが意思決定の境界。
2. **defense-in-depth — sandbox-exec（Seatbelt）。** `shell_exec` の子プロセスは
   SBPL プロファイル下で実行し、ファイル書き込みをプロジェクトディレクトリ +
   scratch 領域に制限する。これが承認では予見できない事象の封じ込め境界。

`--no-sandbox` はデバッグ専用とし、起動時に警告を表示する。

## Consequences

- gem-agent は macOS 専用となる。これは修正すべき制限ではなく、設計制約として
  受容し文書化する。
- sandbox-exec は Apple 公式には deprecated だが、業界の実質標準であり続けて
  いる（Claude Code や同種エージェントが使用）。将来の macOS で削除された場合の
  代替はコンテナ隔離または Apple の Containerization framework — RFP §7 に
  プラットフォームリスクとして記録済み。
- SBPL プロファイルはセッションごとに生成し（プロジェクトパスが動的なため）、
  生成器はユニットテスト必須。

## Alternatives considered

- **コンテナ隔離（podman）** — shell-agent-v2 の opt-in サンドボックス。境界は
  強いが、マシンセットアップ（Podman Machine）とコンテナ起動が、障害時に即座に
  動くべきバックアップツールには重すぎる。既定としては却下。将来の opt-in は
  排除しない。
- **ヒューリスティックなパス検証のみ（PathGuard）** — agent-skeleton 外部
  レビューで単独では不十分と評価済み（動的パス構築で迂回可能）。単独方式として
  却下。ただし引数レベルの検証は承認プロンプトの可読性のために承認前に実施する。
- **App Sandbox（entitlement ベース）** — 署名済み entitlement で配布される
  バンドル GUI アプリ向けの仕組みで、任意のプロジェクトディレクトリを読む必要が
  ある開発者 CLI には不適合。却下。
- **macOS Containerization framework（macOS 26+）** — 有望な後継だが、最新 OS
  への依存と VM 起動レイテンシが加わる。将来候補として記録し、現時点では
  採用しない。

## References

- RFP §3（セキュリティ設計）、§7（プラットフォーム制約）—
  `docs/ja/gem-agent-rfp.ja.md`
- agent-skeleton 外部レビュー バックログ項目 3（サンドボックス実行）
- shell-agent-v2 コンテナサンドボックス（却下した代替案の設計参照元）
