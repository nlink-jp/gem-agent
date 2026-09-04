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
location = "global"        # デフォルト; Gemini 3 系は "global" / "us" / "eu" のみ
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
max_turns = 50             # デフォルト。介入チェックポイントで、延長は 3 倍まで（ADR-0040）
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
show_thoughts = true       # TUI に思考サマリをライブ表示（ADR-0033）

[telemetry]
enabled = false            # 監査ロギング（ADR-0035）— 後述
backend = "gcp"            # gcp（Cloud Logging・既定）| otlp-grpc | otlp-http
endpoint = "localhost:4317" # otlp-* のみ
insecure = false            # otlp-* のみ
# headers_file = "~/.config/gem-agent/auth.json"  # otlp-* 認証ヘッダ・mode 0600

[approval]
# trusted_projects = ["/path/to/repo"]  # プロジェクト全体の信頼 — 下の注意を参照
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

`~/.config/gem-agent/risk-rules.md` はリスクルールブック（ADR-0050）の
手書きベース層です: auto モードのリスク評価器が判定のたびに読む、
自由散文のガイダンス。gem-agent がこのファイルを書くことはありません。
ルールブックにできること・できないことは[承認](approval.ja.md)を
参照してください — 判定を傾けるだけで、ゲートは決して飛ばしません。

`trusted_projects` は承認の緩和だけでなく**プロジェクト全体の信頼**を与えます。
ここに列挙したディレクトリでは同時に 4 つのことが起きます: その
`.gem-agent.toml` が承認を外せるようになり、その `.mcp.json` のサーバーが起動し、
その `.claude/skills` が探索され、その指示ファイルが読み込まれます。起動時の
信頼プロンプトに yes と答えるのと同じ判断なので、承認 1 つを緩めたいだけの
つもりで追加しないでください。

### `[hooks]` — オペレーター pre-tool フック

`[[hooks.pre_tool_use]]` の各エントリ（ADR-0044）は、matcher が対象と
する全モデルツールコールの前にあなたのコマンドを実行します。1 フック
1 ブロック。一致したフックは順に実行され、最初の拒否で確定します。

```toml
[[hooks.pre_tool_use]]
matcher     = "shell_exec"
command     = "python3 /Users/you/hooks/guard.py --strict"
timeout_sec = 10
```

**`matcher`** — フックが*照合する*もの: 各コールのツール名だけです。
形式は 3 つのみ: 完全一致（`shell_exec`）・`|` 区切りの選択
（`shell_exec|write_file`）・全ツール対象の `"*"`（`mcp__server__tool`
のような名前の MCP ツールも含む）。Claude Code の名前も対応する
gem-agent ツールに一致します（`Bash` ↔ `shell_exec`、`Write` ↔
`write_file`、`Edit` ↔ `edit_file`、`Read` ↔ `read_file`）。glob や
正規表現はこの 3 形式以外にはありません。

**`command`** — フックが*実行する*もの: シェルのコマンド行であって、
照合パターンではありません。`/bin/sh -c` でプロジェクトディレクトリを
作業ディレクトリに、gem-agent の環境変数を引き継いで実行されるので、
引数・フラグ・`~`・`$VAR`・パイプはすべて使えます。スクリプトは絶対
パスで指定してください（組織のパス規則）。コマンドは stdin でコールを
1 つの JSON オブジェクトとして受け取ります:

```json
{"hook_event_name": "PreToolUse",
 "tool_name": "shell_exec",
 "tool_input": {"command": "sed -i '' 's/a/b/' notes.txt"},
 "cwd": "/path/to/project"}
```

`tool_input` にはモデルが送ったとおりのツール引数が入ります —
`shell_exec` ならコマンド文字列は `tool_input.command` です。

**判定** — フックの拒否方法は 2 つ:

- stdout に `{"hookSpecificOutput": {"permissionDecision": "deny",
  "permissionDecisionReason": "理由"}}` を出力して exit 0 — 組織の
  Claude Code ガードが出す形式で、そうしたスクリプトは無改修でここに
  登録できます。または
- exit code 2 で終了し、理由を stderr へ。

それ以外はすべて素通りです: 出力なし（または情報表示だけ）の exit 0
はコールを通常の承認階梯へ送ります — フックはコールを拒否できますが、
承認は決してできません。クラッシュ・タイムアウト（`timeout_sec`・
既定 10）・解析不能な出力は、セッションに警告を出して続行します。

完全な最小フックの例:

```sh
#!/bin/sh
# ダウンロードをシェルに直接パイプするコマンドを拒否する
payload=$(cat)
case "$payload" in
  *curl*"| sh"*|*curl*"|sh"*)
    echo "ダウンロードのシェル直パイプはここでは禁止" >&2
    exit 2 ;;
esac
exit 0
```

登録は `matcher = "shell_exec"`、
`command = "/Users/you/hooks/no-curl-sh.sh"`。

deny は最終決定です — 承認階梯・自動承認・セッション allowlist はその
コールを見ることすらなく — 理由はモデルに返り、モデルは修正して再試行
します。グローバル設定専用です: プロジェクト側フックはクローンした
リポジトリに任意コマンドを実行させる経路になります。挙動とゲート順の
中での位置は[承認と安全](approval.ja.md)を参照してください。

環境変数 `GEMAGENT_STATE_DIR` はテスト/訓練の隔離用に state ルート
（sessions と memory）を差し替えます。デバッグ用に、設定ファイル外で直接読まれる
環境変数がもう 2 つあります: `GEMAGENT_MCP_STDERR=1` は MCP サーバーの stderr を
端末へ流し、`RUNEWIDTH_EASTASIAN` は CJK ロケール下で罫線を測るために固定して
いる幅モデルを上書きします。

## CLI フラグ

| フラグ | 内容 |
|---|---|
| `-p "<prompt>"` | 単発実行: 1 ターン・stdout・変更系ツール拒否（`--auto` を除く — ADR-0053）。パイプ stdin はノンスラップ付きデータとして添付され、プロンプト文には決してならない（ADR-0055）。EOF まで読み、2 秒経っても開いたままの pipe は stderr に告知される（ADR-0067） |
| `--auto` | 自動承認モードで開始（ADR-0004）。単発 `-p` で武装する唯一の方法 — そこでは `[agent].auto_approve` は無視される（ADR-0053） |
| `--allow <names>` | 実行単位の承認付与: この実行だけ聞かないツール名または `mcp__server__*` 前置（繰り返し・カンマ区切り可。Block 床は引き続き有効 — ADR-0053） |
| `-c` / `--continue` | このプロジェクトの最新セッションを再開 |
| `--resume <id>` | 特定セッションを再開 |
| `--model <id>` | 設定モデルの上書き |
| `--thinking <level>` | この実行だけ `[model].thinking` を上書き: `minimal`\|`low`\|`medium`\|`high`、または `default` で設定済みレベルをクリア（対応レベルはモデル依存 — ADR-0025） |
| `--config <path>` | 別の設定ファイルを使う |
| `--mcp <on\|off>` | この実行だけ `[mcp].enabled` を上書き — `off` は全 MCP サーバー起動をスキップ。`-p` パイプラインが通常求めるもの（ADR-0039） |
| `--no-sandbox` | Seatbelt ラップの無効化（デバッグ専用） |
| `sessions` | 再開可能なセッション一覧 |

## テレメトリ（ADR-0035）

`[telemetry].enabled = true` で監査イベントがエクスポートされます。
既定バックエンド `gcp` は **[gcp].project の Cloud Logging** に、
Vertex と同じ ADC で直接書き込みます — コレクタ基盤ゼロ、Logs
Explorer でのログ名は `gem-agent`（`logging.googleapis.com` の有効化
と `roles/logging.logWriter` が必要）。`otlp-grpc` / `otlp-http`
バックエンドは代わりに自前のコレクタへ OpenTelemetry ログレコードを
送ります。イベント: `session.start/end`・`tool.call`
（名前・切詰め詳細・所要・結果 — `ok`・`error`・`denied`・`skipped`・
`interrupted`・`abandoned`）・`tool.late_return`（放棄された呼び出しが
結局戻った — ADR-0065）・`approval.decision`（判定とどの層が
決めたか）・`turn.end`・`model.usage`・`compaction`・`media.upload`・
`integration.reload`（セッション中の `/mcp reload` / `/skills reload`
がツール面を変えた — ADR-0039）—
サービス/セッション/プロジェクト/ホストのリソース属性付き。
Cloud Logging のエントリは `project_id` をラベルにした `global`
モニタードリソースを持ちます。これは検出ではなく宣言で、クライアント
作成はネットワークに触れないため、テレメトリを有効にしても起動時間は
増えません（ADR-0068）。
**メタデータのみ**: プロンプト・応答・ファイル内容・思考サマリは
この経路からマシンを出ません。全文はローカル transcript が正本の
ままです。テレメトリを有効化・宛先設定できるのはグローバル config
だけで、プロジェクトの `.gem-agent.toml` からは構造的に不可能です。
OTLP の認証ヘッダは `[telemetry].headers_file` — モード 0600 の JSON
ファイル（慣例として `~/.config/gem-agent/auth.json`、ヘッダ名 → 値）
から読みます。ファイルは launchd/cron/新しいシェルでも生きています —
環境変数は消えます。未設定なら標準の `OTEL_EXPORTER_OTLP_HEADERS` に
フォールバックします。
テレメトリは決してセッションをブロックしません: 送信失敗は stderr に
1 回警告して静かに劣化し、終了時 flush は 3 秒上限です。

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

2026-09 時点で Gemini 3 系は global エンドポイントと `us` / `eu`
マルチリージョンから提供されています（Vertex のモデルページによる;
`global` と `us` は gemini-3.8-flash / gemini-3.7-flash で実測）。
`us-central1` のような単一リージョン指定は 404 になります（実測）。
既定は `global` で、データ所在地の要件がある場合に
`location = "us"` または `"eu"` を設定してください。Gemini 2.5 系は
`us-central1` 等の単一リージョンで動作するので、使う場合は
`location` をそちらに設定してください。Vertex の一時障害（429/5xx）
は指数バックオフでリトライします。
