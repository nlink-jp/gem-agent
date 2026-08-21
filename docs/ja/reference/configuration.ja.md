# 設定

導入、設定ファイル、優先順位、そして知っておく価値のあるプロバイダ側
挙動（コンテンツフィルタ・エンドポイント）。

## 導入

```sh
brew install nlink-jp/tap/gem-agent
```

または[リリースページ](https://github.com/nlink-jp/gem-agent/releases)
から署名 + notarize 済みアーカイブを取得します（macOS arm64）。
ソースからは `make build`（`dist/gem-agent` に出力）。

動作要件: macOS（Apple Silicon）、Vertex AI が有効な Google Cloud
プロジェクト、Application Default Credentials
（`gcloud auth application-default login`）と `roles/aiplatform.user`。

## 設定ファイル

リポジトリ同梱のテンプレートから始められます:

```sh
mkdir -p ~/.config/gem-agent
cp config.example.toml ~/.config/gem-agent/config.toml
cp mcp.example.json    ~/.config/gem-agent/mcp.json   # 任意: MCP サーバー
```

`~/.config/gem-agent/config.toml`:

```toml
[gcp]
project  = "your-project-id"
location = "global"        # デフォルト; Gemini 3 系は global エンドポイント専用
# bucket = "your-bucket"   # 任意; 音声/動画を GCS 経由にする（ADR-0027）

[model]
name = "<gemini model id>"
# summary = "<light model>" # 任意; summarize_file / web_fetch 要約用（ADR-0014）
# context_window = 1048576  # 任意; フッター表示と圧縮判定に使う厳密値
# thinking = "high"         # 任意; Gemini 3 の思考レベル: minimal|low|medium|high
#                           # (未設定 = モデル既定。対応レベルはモデル依存。
#                           #  要約モデルには効かない — ADR-0025)
# safety = "default"        # default | relaxed | off（コンテンツフィルタ参照）

[sandbox]
enabled = true             # デフォルト

[agent]
max_turns = 50             # デフォルト
shell_timeout_sec = 120    # デフォルト
auto_approve = false       # デフォルト; 起動時から自動承認モードにする
auto_compact = true        # デフォルト; ウィンドウ接近時に古い履歴を要約
compact_at_pct = 80        # デフォルト; 発動するウィンドウ占有率

[mcp]
enabled = true             # デフォルト; false で全 MCP サーバーを無効化
call_timeout_sec = 60      # デフォルト

[tui]
theme = "auto"             # auto | dark | light | plain
language = "auto"          # auto | ja | en（ADR-0029 — interface.ja.md 参照）

[approval]
# trusted_projects = ["/path/to/repo"]  # .gem-agent.toml の緩和を許すプロジェクト
[approval.tools]
# "mcp__tor-exit-lookup__*" = "never"   # ツール別ポリシー — approval.ja.md 参照
```

優先順位: フラグ（`--model`）> `GEMAGENT_*` > `GOOGLE_CLOUD_*` >
config file > defaults。設定ファイル内の未知キーはエラーになり
（strict decode）、不正な値はキー名を名指しして起動時に失敗します —
タイポが原因から遠い実行時エラーとして現れてはいけません。

`/settings` は全設定を出所付きでライブ表示します。機械が永続化する
決定（ポリシー編集・プロジェクト信用）は gem-agent 所有の
`~/.config/gem-agent/policy.toml` に入り、手書きの `config.toml` は
書き換えられません。

環境変数 `GEMAGENT_STATE_DIR` はテスト/訓練の隔離用に state ルート
（sessions と memory）を差し替えます。

## CLI フラグ

| フラグ | 内容 |
|---|---|
| `-p "<prompt>"` | 単発実行: 1 ターン・stdout・変更系ツール拒否 |
| `-c` / `--continue` | このプロジェクトの最新セッションを再開 |
| `--resume <id>` | 特定セッションを再開 |
| `--model <id>` | 設定モデルの上書き |
| `--thinking <level>` | この実行だけ `[model].thinking` を上書き: `minimal`\|`low`\|`medium`\|`high`、または `default` で設定済みレベルをクリア（対応レベルはモデル依存 — ADR-0025） |
| `--config <path>` | 別の設定ファイルを使う |
| `--no-sandbox` | Seatbelt ラップの無効化（デバッグ専用） |
| `sessions` | 再開可能なセッション一覧 |

## コンテンツフィルタ

Vertex はリクエストとレスポンスの両方にコンテンツフィルタを適用
します。フィルタが作動した場合、gem-agent は**空の応答を見せるのでは
なく、API が返した理由を明示して「ブロックされた」と報告**し、
**1 回だけ自動リトライ**します。判定対象はその試行で生成された
テキストなので**フィルタは確定的ではなく**、同じ要求でも次の試行では
通ることが多いためです。実測: インシデント対応手順書を文脈に含む同一
要求で、**`[model].safety` の設定に関係なく**ブロックされる試行と
通る試行がありました。

リトライも弾かれた場合はその旨を報告します。要求を狭める、`/clear`
で大きな文書を文脈から外す、といった対処が有効です。

`[model].safety` は調整可能な 4 カテゴリの閾値を変更します（`SAFETY`
由来のブロックには有効ですが、**`PROHIBITED_CONTENT` はこの設定が
及ばない別系統のフィルタ**である点に注意）:

| 値 | 効果 |
|---|---|
| `default` | プロバイダ既定の閾値 |
| `relaxed` | 確信度が高いものだけブロック |
| `off` | 該当カテゴリではブロックしない |

緩めるかどうかは意図的な判断であるべきなので、既定値は変更して
いません。

## エンドポイント

2026-08 時点で Gemini 3 系（gemini-3.7-flash / gemini-3-flash-preview
で実測）は global エンドポイントからのみ提供されており、リージョナル
指定は 404 になります。Gemini 2.5 系は `us-central1` 等のリージョナル
で動作するので、使う場合は `location` をそちらに設定してください。
Vertex の一時障害（429/5xx）は指数バックオフでリトライします。
