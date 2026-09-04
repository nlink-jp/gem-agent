# ADR-0069: セッション開始フックとプロンプト送信フック — 文脈はデータとして入り、契約は Claude Code と同じ

| 項目 | 値 |
|-------|-----|
| ステータス | **Accepted** |
| 日付 | 2026-09-04 |
| 拘束範囲 | gem-agent |
| 決定者 | nlink-jp メンテナ |
| 契機 | オペレーター: 並走するエージェントセッション（gem-agent と Claude Code の両方）がランタイム横断で読み書きする共有知識空間には、ターンごとの注入点が要る。Claude Code には `SessionStart` と `UserPromptSubmit` のフックがあり、gem-agent には `PreToolUse` しかない |
| 改訂 | ADR-0044 §1（同節が予期していた需要に応じてイベントを 2 つ追加） |

## Context（背景）

ADR-0044 はフックイベントを 1 つ実装し、残りを見送った: 「需要が
実証されているのは PreToolUse だけであり、契機なき能力は死重である。
需要が現れれば同じ機構にイベントを足せる」。その需要が現れた。組織は、
並走するエージェントセッションがランタイムを跨いで読み書きする、
マシン内共有の知識空間を設計している。その配信モデルは「ターン境界
ごとの差分注入」— モデルをターンの途中で割り込めない以上、これが唯一
正直な粒度である — であり、Claude Code はその境界をフックとして既に
提供している。gem-agent にはなかった。

設計前に実測した（Claude Code 2.1.226、`--settings` 経由で捕捉
スクリプトを登録。文書上の契約は後から読み、2 箇所で食い違っていた）:

- `SessionStart` の stdin: `{"session_id", "transcript_path", "cwd",
  "hook_event_name": "SessionStart", "source"}`。`source` は新規実行で
  `"startup"`、`--resume` 下で `"resume"`。文書は `permission_mode` も
  挙げているが、ペイロードにはなかった。
- `UserPromptSubmit` の stdin: 同じ識別フィールドに加え `prompt_id`、
  `permission_mode`、そしてタイプされた本文は **`prompt`** の下。文書は
  このフィールドを `user_input` と呼んでいるが、ペイロードは `prompt`
  と言っている。
- 出力: exit 0 で素の stdout はトランスクリプトに `hook_success`
  attachment として本文付きで記録され、`hookSpecificOutput.additionalContext`
  を持つ JSON オブジェクトは `hook_additional_context` attachment として
  記録される。どちらもモデルに届く。そのフィールドを持たない JSON
  オブジェクトは何も注入しない。
- ブロック: exit 2 する `UserPromptSubmit` フックはモデル呼び出しの前に
  ターンを止める — 結果は「operation blocked by hook」+ stderr の理由 +
  元のプロンプト — で、`{"decision": "block", "reason"}` 形式も同じ。
  `SessionEnd`（文書によれば `SessionStart` も）はブロックできない: exit
  2 はフック失敗として報告される。
- 定義のないイベントに `hookSpecificOutput` を返すと Claude Code の
  出力スキーマ検証に落ち、stderr に報告される。

## Decision（決定）

### 1. gem-agent 自身の設定からイベントを 2 つ追加、グローバルのみ

`config.toml` の `[[hooks.session_start]]` と
`[[hooks.user_prompt_submit]]` が `[[hooks.pre_tool_use]]` に並び、
それぞれ `command` と任意の `timeout_sec` を持つ。`session_start` の
エントリは source を選ぶ `matcher`（`startup`・`resume`・`clear`。
ツールと同じく `a|b` と `*` も可）を持てる。`user_prompt_submit` は
持たない — 全プロンプトで走り、そこに matcher を書くのは「静かに発火
しないフック」ではなく設定エラーになる。ADR-0044 §5 は維持:
プロジェクト面は無い。コンテキストフックは毎ターン走るので、プロジェクト
側に置けばクローンしたリポジトリがターンごとに任意コマンドを実行する
ことになる。`~/.claude/settings.json` は引き続き読まない（ADR-0011）。

### 2. ペイロードは Claude Code のもの、source は gem-agent が持つもの

各フックは stdin に JSON オブジェクトを 1 つ受け取る: `hook_event_name`、
`session_id`、`transcript_path`、`cwd`、それに `SessionStart` なら
`source`、`UserPromptSubmit` なら `prompt` — 実測したフィールドのうち
gem-agent が正直に供給できるもの。`prompt_id` と `permission_mode` は
gem-agent に対応物がなく、でっち上げず省く。セッションログが無効の
ときは `session_id` と `transcript_path` を空で送り、スクリプトが常に
同じ形を見るようにする。

`session_start` は起動時に 1 回、`source` は `startup`、
`--continue`/`--resume` 下では `resume` で発火し、`/clear` でも `clear`
として再び発火する — クリアされた会話はオペレーターのスクリプトに
とって新しい開始である。圧縮後には発火しない: Claude Code の `compact`
source にはここでの消費者が実証されておらず、ターン内で発火させると
§4 が避けている問いを立てることになる。`user_prompt_submit` は
モデルに届く全ターン — タイプしたメッセージ、argv の第 1 メッセージ
（ADR-0064）、`/skill` 展開されたターン、`-p` のプロンプト — で発火し、
ターンにならないスラッシュコマンドとオペレーターの `!` シェル
エスケープでは発火しない。

**追記（同日、v0.65.1）。** `PreToolUse` のペイロードも `session_id` と
`transcript_path` を運ぶようになった（ログ無効時は空）。セッションごとの
状態を持つ hook がコールをそのセッションに結び付けられるようにするため —
agent-board の claim 強制がまさにこれを必要とし、無いと claim の主が自分の
ファイルを拒否されていた。Claude Code の PreToolUse も同じ識別フィールドを
運ぶので、形は 1 つの契約のままである。

### 3. 出力は文脈か判定。プロンプトは拒否でき、セッション開始は拒否できない

exit 0 で素の stdout は注入文脈、JSON オブジェクトは判定であり、その
うち文脈になるのは `hookSpecificOutput.additionalContext` だけ。
`user_prompt_submit` フックは exit 2 + stderr の理由、または ADR-0044
§3 が既に受け付けている 2 つの JSON ブロック形式
（`hookSpecificOutput.permissionDecision: "deny"`、`decision: "block"`）
でプロンプトを拒否する。拒否されたプロンプトは消える: 履歴にも
トランスクリプトにも入らず、`turn.end` イベントも出ず、`Run` は理由
付きの `ErrPromptBlocked` を返し、オペレーターがそれを見る。最初の
ブロックが勝ち、他のフックがそのプロンプト用に作った文脈も一緒に
捨てられる。ブロックしようとする `session_start` フックは失敗として
報告され、何も注入しない — 実測した Claude Code の意味論。それ以外 —
非ゼロ exit・タイムアウト・解析不能な出力 — は通知付きで fail-open、
文脈なし（ADR-0044 §3、不変）。

### 4. 注入文脈はデータレーンに乗る。system prompt にもタイプ入力にも触れない

フック出力は次のユーザーメッセージの attachment として届く — ADR-0055
がパイプ stdin のために作ったレーン: トランスクリプトではタイプした
本文の隣に格納され、送信時にはその後ろへ平坦化されてターンのノンス
タグの中に入り、「quoted as data」と告げられる。ここから 3 つのことが
従い、それぞれがこの選択の理由である:

- **system prompt は不変。** よってセッション開始フックの出力は
  implicit cache が依存するバイト安定なリクエスト接頭辞（ADR-0018）を
  乱さず、ADR-0020 §7 の起動時スナップショット規則も保たれる:
  セッション中に接頭辞を書き換えるものは無い。`/clear` も同様で、
  そのフック出力は単に最初の新しいターンに乗る。
- **タイプ入力は不変。** 入力文字列はリスク評価器の「オペレーター
  指示」の証拠（ADR-0038/0054）であり、注入攻撃者が書けないからこそ
  信頼されている。フック出力は「そのコードが読んだものの上で走った
  コード」の産物 — 契機となった設計では、他のエージェントが書く共有
  ストア — であり、その地位を得てはならない。プロンプトに混ぜれば、
  評価器の唯一の清浄なチャネルをそのストアの全書き手に渡すことになる。
  `attachdata_test.go` が stdin に対してそうしているように、テストが
  境界を固定する。
- **モデルにはそれが何かを告げる。** 「Attached hook (session_start),
  quoted as data」は出所の陳述であって禁止ではない。契機となった設計が
  望むのはまさにこの枠書きである: その記録は他セッションが行った
  主張であり、重み付けするものであって、指令ではない。

これは、フックの stdout をそうしたラッパー無しにモデルの文脈へ置く
Claude Code より厳しい。差は意図的で文書化されている。Claude Code から
写した hooks ブロックはそのまま動き、出力がラベル付きで届くだけである。
出力はフック 1 つあたり 8000 rune で上限し、切り詰めは可視。オペレーター
は注入ごとに 1 行を見る（「session_start hook (startup) attached N bytes
of context as data for the next turn」）— 毎ターンを黙って形作る
チャネルは、間違った種類の静けさである。

### 5. 実機検証

2026-09-04、`--mcp off`、scratch の state ルートでの単発実行: 両フックが
§2 のペイロードで発火し（フックスクリプトがフィールドごとに検査）、
トランスクリプトの最初のユーザーメッセージは無傷のプロンプトの隣に
`hook` attachment を 2 つ持ち、モデルは両方のマーカー文字列を逐語的に
復唱した。exit 2 するプロンプトフックを付けた 2 回目の実行はモデル
呼び出しの前に止まった: 終了ステータス 1、stderr に理由、トランスクリプト
にはセッションヘッダーだけ。

## Consequences（帰結）

- 共有知識空間の設計は 1 つのスクリプトを両ランタイムに登録できる:
  stdin ペイロードと出力契約は同じで、唯一の差 — データとしての枠書き —
  はその設計が望むものである。
- 毎ターン、設定された `user_prompt_submit` フックごとに 1 プロセス
  起動（フック自身の仕事は別）を払う。セッション開始は `session_start`
  フックごとに 1 回。未設定なら何も走らない。
- 毎ターン出力するフックは、毎ターンの文脈に最大 8000 rune を足す。
  それはオペレーターが使う予算であり、上限と通知が使途を見えるようにする。
- `Agent.Options` に `PromptHook` が加わり、`Run` は何も記録する前に
  `ErrPromptBlocked` を返しうる。`slashOutput` は `/clear` がセッション
  開始フックを再実行できるよう `onClear` コールバックを取る。
- 設定リファレンスと同梱テンプレートに `[hooks]` エントリが 2 つ増える。
  設定パネルのトグルは引き続き無い（ADR-0044: 制御のトグルはバイパス）。

## Alternatives considered（検討した代替案）

- **セッション開始の出力を system prompt へ**（オペレーター指示の
  節として）— 却下: `/clear` でキャッシュ済み接頭辞を書き換え、コード
  生成のテキストに AGENTS.md の信頼階層を与えることになる（§4）。
- **Claude Code と同じく素の文脈として** — タイプ入力チャネルの一点で
  却下: フック出力をプロンプト文字列に混ぜるのは、生産者が違うだけの
  ADR-0055 の穴（§4）。データレーンは既に存在し、境界テストも既に
  あった。
- **4 つ目の source として `compact`** — 保留: 消費者がなく、ターン内で
  発火するフックには独自の配信規則が要る（§2）。
- **`SessionEnd`** — 保留: 契機となった設計の claim は TTL と heartbeat で
  失効するので、終了時解放イベントにまだ消費者がない。そのペイロード
  （`reason`: 単発モードで `other`）は他と一緒に捕捉済みで、同じ機構に
  足せる。

## References（参照）

- ADR-0044（pre-tool フック。実測契約の方法と、この ADR が援用する §1 の一節）
- ADR-0055（データ attachment としてのパイプ stdin。ここで再利用したレーンと境界テスト）
- ADR-0038 / ADR-0054（リスク評価器の信頼された指示チャネルとしてのタイプ入力）
- ADR-0018（バイト安定なリクエスト接頭辞）、ADR-0020 §7（起動時スナップショット）
- ADR-0064（argv の第 1 メッセージはタイプされたターン）
