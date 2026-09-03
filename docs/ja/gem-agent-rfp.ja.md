# RFP: gem-agent

> Generated: 2026-08-18
> Status: Draft

## 1. Problem Statement

**gem-agent** は Vertex AI Gemini 3.x をバックエンドとする CLI 対話型エージェント
ランタイムである。ローカルファイルの読み書き・コマンド実行・MCP サーバー接続を
備え、既存プロジェクトの AGENTS.md / CLAUDE.md / .mcp.json をそのまま解釈する
ことで、1 つのプロジェクト設定がその上で動くすべてのランタイムに仕える
（drop-in）ことを最重要要件とする。対象ユーザーは nlink-jp 運営者本人。スコープは
意図的に最小限（read / edit / shell / MCP / 承認ゲート）とし、分析・GUI 等は
持たない。macOS 専用とし、sandbox-exec + 承認ゲートの二層で防御する。

> **位置づけ注記（2026-09-01）。** 本文書はもともと継続手段 — Claude Code が
> 使えないときのバックアップ — のために書かれた。その憲章は
> [ADR-0061](adr/0061-independent-runtime-promotion.ja.md) で退役した:
> gem-agent は独立したエージェントランタイムであり、下部の Discussion Log に
> 残るバックアップの言葉は歴史であって現行の憲章ではない。

## 2. Functional Specification

### Commands / API Surface

| コマンド | 説明 |
|---|---|
| `gem-agent` | カレントディレクトリをプロジェクトとして対話 REPL を起動 |
| `gem-agent -p "<prompt>"` | 単発実行モード（回答後に終了、パイプ可能） |
| `gem-agent --version` | バージョン表示（brew test が呼ぶため必須応答） |

**フラグ:**

- `--config <path>` — 設定ファイルパスの上書き
- `--model <name>` — モデル名の上書き
- `--no-sandbox` — sandbox-exec ラップを無効化（デバッグ用、起動時に警告表示）
- `--thinking <level>` — 推論レベルの上書き（[ADR-0025](adr/0025-thinking-level.ja.md)）
- `--mcp on|off` — この実行での MCP 有効/無効（[ADR-0039](adr/0039-integration-reload.ja.md)）
- `-p, --prompt <text>` — 単発実行、パイプ向け
- `-c, --continue` / `--resume <id>` — セッション再開（[ADR-0005](adr/0005-session-resume.ja.md)）

**REPL 内スラッシュコマンド:** 現行は `/help` `/tools` `/mcp` `/memory` `/skills`
`/skill` `/settings` `/usage` `/auto` `/compact` `/clear` `/quit`（`/exit` は
`/quit` の別名）で、`/mcp` と `/skills` には `reload` サブコマンドがある。
現行表は[インターフェースリファレンス](reference/interface.ja.md)が持つ —
ここは名前を挙げるだけで一覧を所有しないので、遅れようがない。

**組み込みツール:** v1 は 5 つ（`list_files`・`read_file`・`write_file`・
`edit_file`・`shell_exec`）と MCP ツールで出荷した。以降の追加はすべて ADR
（後述 *v1 以降に採用したもの*）であり、権威ある一覧は
[ツールリファレンス](reference/tools.ja.md)である — ここに二つ目の列挙を置けば
二つ目の同期対象が生まれる。ゲートの規則自体は変わっていない:

| 種別 | MITL 既定 |
|---|---|
| 読み取り専用ツール（探索・読取・要約・`agent_info`・`ask_user`） | 自動許可 |
| 変更系ツール（`write_file`・`edit_file`・`shell_exec`・メモリ書込・Web 送信） | 都度承認 |
| MCP ツール（外部サーバー由来） | 都度承認。構造的に Safe へ落ちない |

承認プロンプトでは「このセッションでは常に許可」を選択でき、セッション内
allowlist に登録される（永続化しない）。

### Input / Output

- **入力:** 複数行ペースト安全な読取り（単行 Scanner による残行のシェル漏れを
  構造的に防止）。入力履歴ナビゲーション（ArrowUp/Down）は「履歴ナビ中」state
  フラグ必須実装とする
- **出力:** モデル応答はストリーミング表示。ツール実行はイベント行として表示
- **セッションログ:** JSONL 追記保存、1 会話 1 ファイルで
  `~/.local/state/gem-agent/sessions/projects/<escaped-project-path>/` 配下
  （[ADR-0022](adr/0022-per-project-session-layout.ja.md) が v1 のフラット配置から
  移した）。v1 出荷後、これが resume の正本になった（ADR-0005）

### Configuration

`~/.config/gem-agent/config.toml`（組織統一の Vertex AI config スキーマに準拠）:

```toml
[gcp]
project  = "your-project-id"
location = "global"   # Gemini 3 系は "global" / "us" / "eu" のみ。単一リージョンは 404

[model]
name = "<gemini-3.x model id>"

[sandbox]
enabled = true

[agent]
max_turns = 50
```

- **env 優先順位:** `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file > 組み込み default
- **プロジェクト側読取り（drop-in 互換の中核）:**
  - `AGENTS.md` / `CLAUDE.md` — システムプロンプトへ注入（AGENTS.md 優先、両方あれば併記）
  - `.mcp.json` — Claude Code 形式の MCP サーバー定義をそのまま解釈（stdio）

### External Dependencies

- **Vertex AI Gemini 3.x** — `google.golang.org/genai`（`BackendVertexAI`）、ADC 認証
- **nlk**（github.com/nlink-jp/nlk） — guard（ノンス XML 隔離）/ jsonfix / backoff
- 他の外部サービス依存なし

## 3. Design Decisions

### 言語・プラットフォーム

- **Go** — 組織の CLI 標準。単一バイナリ配布、署名・notarize フローが確立済み。
  Vertex AI の公式 Go SDK（`google.golang.org/genai`）が利用可能
- **macOS 専用** — sandbox-exec（Seatbelt）によるプロセスサンドボックスを前提に
  するため。クロスプラットフォーム対応は明示的に切り捨てる

### 既存ツールとの関係

- **gem-cli**（cli-series）— 単発呼び出しの Gemini CLI。gem-agent は対話型
  エージェントループを持つ点で役割が異なる（補完関係）
- **agent-skeleton**（lab-series）— ReAct ループ・PathGuard・ツール群の設計移植元
  （Python POC。コードは移植せず設計を踏襲）
- **shell-agent-v2**（util-series）— Gemini 3 thought signature 対応（ADR-0009）、
  MITL ゲート、MCP kill-and-respawn パターンの実装移植元
- **mcp-guardian** — stdio MCP を透過的にラップでき、監査証跡が付く（opt-in）
- **nlk** — プロンプトインジェクション隔離・JSON 修復・backoff の共通ライブラリ

### セキュリティ設計（二層防御）

1. **一次防御 = MITL 承認ゲート** — write / exec / MCP は都度承認 + セッション内
   allowlist
2. **defense-in-depth = sandbox-exec** — `shell_exec` を SBPL プロファイルでラップし、
   file-write をプロジェクトディレクトリ + scratch に制限。ヒューリスティックな
   パス検証だけでは動的パス構築（コマンド置換等）を防げないという agent-skeleton
   外部レビューの指摘への構造的回答
3. **ツール出力・ファイル内容の隔離** — nlk/guard のノンスタグ XML ラップで
   「データであって指示ではない」を強制。防御指示はシステムプロンプト冒頭に配置

### プロトコル対応

- **Gemini 3 thought signature echo-back を Phase 1 から実装** — tool-call ループ
  2 周目で 400 になる既知の罠。capture/replay は shell-agent-v2 ADR-0009 の
  パターンを踏襲
- **MCP キャンセル** — プロトコルにキャンセル通知が無いため、中断は子プロセス
  kill-and-respawn で実装

### Out of scope（明示的除外）

- データ分析機能（DuckDB 等）
- GUI
- Linux / Windows 対応

**v1 の後に ADR 付きで採り入れたもの**（運用が当初の判断に反論した。黙って
足すのではなく記録して足す）:

- セッション resume — [ADR-0005](adr/0005-session-resume.ja.md)。
  フォールバックツールは長い作業の途中で持ち出される。終わったセッションは
  文脈も持って行ってしまう。
- コンテキスト圧縮 — [ADR-0006](adr/0006-context-compaction.ja.md)。
  コンテキストウィンドウで死に、復旧手段が `/clear` だけのセッションは
  フォールバックとして頼りにならない。
- コンテキストキャッシュ — [ADR-0018](adr/0018-context-caching.ja.md)。
  セッションスコープの隔離タグでプレフィックスがキャッシュ可能に。同一
  タスク実測 0% → 81〜95%。
- Web アクセス — [ADR-0017](adr/0017-web-tools.ja.md)。グラウンディング検索
  （first-party。agentic-web-search の凍結がその歴史）と、軽量モデルが要約する
  URL Context フェッチ。
- file_info — [ADR-0016](adr/0016-file-info.ja.md)。種別判定・メタデータ・
  組織の lookup MCP が食べるハッシュ三点セット。
- edit_file v2 — [ADR-0015](adr/0015-edit-file-v2.ja.md)。診断つき不一致と
  成功時証拠を備えた一括アトミック編集。同じ無駄の書き込み側。
- コンテキスト経済 — [ADR-0014](adr/0014-context-economy-tools.ja.md)。
  行窓読みと、設定可能な軽量モデルによる summarize_file。「探す」は
  ADR-0013 で安くなった。残った費目が「読む」だった。
- ナビゲーションツール — [ADR-0013](adr/0013-navigation-tools.ja.md)。
  ツリー表示と依存ゼロの高速 grep。方向づけはディレクトリごとに 1 ラウンド、
  検索はファイルの丸読みという価格だった。
- 画像入力 — [ADR-0012](adr/0012-image-input.ja.md)。業務はスクリーン
  ショットから始まることが多く、MCP サーバーはモデル自身が見るべき画像を
  生成する。
- Skills — [ADR-0010](adr/0010-skills.ja.md)。Claude Code の skill 形式を
  そのまま読む。skills-series に書き溜めた手順は Claude Code 停止時に
  そのまま失われるが、それはこのツールが埋めるべき穴と同じものである。

上の 10 件は、この節の判断に最も強く反論した追加であり、その反論自体が
価値なので全文を残す。以降の追加も同じ形で記録され、権威ある一覧は
[`INDEX.ja.md`](INDEX.ja.md) が持つ: ツール別承認ポリシー（0008）、設定
パネル（0009）、使用量集計（0019）、**エージェントメモリ（0020）** — 本節が
かつて除外していたもので、「運用が当初の理由に反論した」という同じ基準で
採用した。作業中に得た事実がセッションと共に死に、フォールバックツールは
最も必要なときにその連続性を失っていた — 思考レベル（0025）、文書読取
（0026）、音声・動画（0027）、UI 言語（0029）、エージェント自己情報（0030）、
datetime（0032）、ターン可観測性（0033）、監査テレメトリ（0035）、`ask_user`
（0036）、エージェンティックファイル検索（0037）、統合リロード（0039）、
ラウンド上限介入（0040）、端末内図描画（0042）。この段落はカタログを複製せず
参照する — 2 箇所で保守される列挙は必ず片方が遅れ、実際にここが遅れた。

スコープ最小化は shell-agent v1 の「盛りすぎによる複雑化 → 作り直し」の教訓に
基づく。当初の物差し「バックアップ用途に必要なのは Claude Code の日常機能の
中核 2 割」は、バックアップ役割の退役（ADR-0061）と同時に自前の憲章へ置き
換えられた: 最小限で監査可能なエージェントループ — read / edit / shell /
MCP / 承認 — であり、分析・GUI サブシステムは持たない。機能追加は従来どおり
ADR 経由のみ。

## 4. Development Plan

### Phase 1: Core

- エージェントループ（Vertex AI + thought signature capture/replay + ストリーミング）
- 組み込みツール（list_files / read_file / write_file / edit_file / shell_exec）
- MITL 承認ゲート + セッション内 allowlist
- config loader（TOML + env 優先順位）
- ペースト安全 REPL（複数行読取り、履歴ナビ state フラグ）
- JSONL セッションログ
- sandbox-exec SBPL プロファイル生成
- ユニットテスト（モック LLM によるループ検証、ツール引数検証、SBPL 生成検証）

### Phase 2: Features

- MCP クライアント（stdio、`.mcp.json` 互換）
- mcp-guardian 経由の opt-in 構成
- AGENTS.md / CLAUDE.md のシステムプロンプト注入
- nlk/guard ノンス隔離の全ツール出力への適用
- 単発実行モード（`-p`）
- 429 backoff（nlk/backoff）

### Phase 3: Release

すべて完了（2026-08-19）:

- docs/{en,ja} 三層ドキュメント + ADR — [`INDEX.ja.md`](INDEX.ja.md)、
  `reference/`、`adr/`（当時 ADR 6 本。現行は [`INDEX.ja.md`](INDEX.ja.md) の
  カタログが正）。en/ja ミラーは
  `scripts/docs-mirror-check.sh` が `make check` で機械検証する
- 実プロジェクトでの E2E — 初回訓練を `json-filter` と本リポジトリに対して実施。
  実タスクのステップ（gem-agent だけ）はツール層のパス封じ込めの
  読み取り専用レビューで、回答をソースと照合して確認した
- リリース — 署名 + notarize 済み darwin/arm64、Homebrew tap
- [**月次訓練手順書**](reference/drill.ja.md) — 初回実行で自分自身の
  ステップ 3 つを書き換えた
- cli-series の[**昇格基準の明文化**](reference/promotion.ja.md)

各 Phase は独立してレビュー可能。

## 5. Required API Scopes / Permissions

- GCP Application Default Credentials（`gcloud auth application-default login`）
- 対象 GCP プロジェクトへの `roles/aiplatform.user`
- OAuth クライアント発行・API キーは不要。他サービスの権限なし

## 6. Series Placement

**Series: cli-series**（2026-09-01 利用者決定により昇格、ADR-0061）

**Reason:** 当初の配置は lab-series だった — 新規建造の初期は実験段階として
配置し、E2E と訓練運用の実績が確立した時点で cli-series への昇格を検討する。
「実験置き場」と「バックアップは常時稼働が要る」の緊張は、月次訓練の運用と
明文化された昇格基準で緩和する、というものである。バックアップの憲章が退役
した時点で、訓練ベースの基準は合格ではなく失効した: 新しい役割が必要とする
問いには実戦投入の実績が既に答えていた
（[promotion](reference/promotion.ja.md) が当初の基準とこの決定を記録する）。
以後は cli-series の契約が効く: インターフェースの安定性は約束であり、
破壊的変更は組織の破壊的変更プロセスを通る。

## 7. External Platform Constraints

- **Gemini 3 thought signature** — 各 Part の signature を次リクエストで echo back
  しないと 400 INVALID_ARGUMENT（プロトコル制約、回避不可）
- **Vertex AI rate limit** — 逐次大量コールで 429 が頻発。backoff 必須、
  スループット期待値は控えめに設定
- **Gemini 2.5 廃止（2026-10-16 以降）** — 3.x 専用設計とし、モデル名は config
  駆動（ハードコード禁止）
- **function_call 引数サイズ** — 実用上限は数百 KB〜1MB。大きなデータはファイル
  経由で受け渡す
- **sandbox-exec の位置づけ** — Apple 公式には deprecated（業界では実質標準）。
  将来の macOS で削除された場合の代替（コンテナ / Containerization framework）を
  リスクとして記録

---

## Discussion Log

- **2026-08-18 初回議論:** 「組織に類似ツールなし」という当初前提を訂正 —
  agent-skeleton（file/shell/MCP ツール + PathGuard を持つ Python POC）と
  shell-agent-v2（Vertex + MITL + sandbox + MCP を持つ GUI）が存在する。
  「CLI・Go・Vertex 直結・macOS sandbox」の組み合わせが未存在であることを確認し、
  新規建造を「ゼロから」ではなく「既存 2 プロジェクトの設計資産の移植」として
  位置づけた
- **バックアップ役割からの要件導出:** (1) drop-in 互換（AGENTS.md / CLAUDE.md /
  .mcp.json をそのまま読む）が最重要機能、(2) スコープは意図的に最小限、
  (3) 訓練しないバックアップは腐るため月次訓練を運用要件化
- **ツール名:** gem-agent に決定。gem-code（コード特化に見える）、gem-pilot
  （役割が名前から読めない）は却下。gem-* 命名系列に合流
- **シリーズ配置:** cli-series 推奨に対しユーザー判断で lab-series を選択。
  昇格基準の明文化（Phase 3）で実験置き場とバックアップ役割の緊張を緩和
- **承認モデル:** 都度承認 + セッション内 allowlist（Claude Code 風）に決定。
  常時都度承認（緊急時にテンポが落ちる）、config 事前 allowlist（設定ミス時の
  リスク大）は却下
- **sandbox 方式:** sandbox-exec を既定に採用。コンテナ（podman）案は起動の重さが
  バックアップ用途に不向きとして却下。macOS 26 Containerization framework は
  将来の代替候補として記録
- **コンテキスト圧縮・メモリ・resume:** v1 から明示的に除外（shell-agent v1 の
  盛りすぎ教訓）。ただし圧縮と resume は v1 出荷後に、それぞれ ADR（0006 /
  0005）付きで採用した。運用の結果、どちらもフォールバックという役割の
  土台であって便利機能ではないと分かったためである。メモリも後に同じ基準で
  ADR-0020 により採用した
