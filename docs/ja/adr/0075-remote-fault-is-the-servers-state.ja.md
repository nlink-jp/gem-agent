# ADR-0075: リモートツールの同一エラーの反復はサーバーの状態であり、ランタイムがそう告げる

| 項目 | 内容 |
|-------|-------|
| ステータス | **採択**（2026-09-06、§5 の独立レビュー後に改訂） |
| 日付 | 2026-09-05 |
| 拘束対象 | gem-agent |
| 決定者 | nlink-jp maintainers |
| 契機 | セッション 4d6bb685（2026-09-05）: mcp-bridge 経由の BigQuery リモート MCP サーバーが、全コールに `query` が含まれていたにもかかわらず `execute_sql*` 10 コールに「Required parameter is missing: query」と答え、モデルは自分が走るランタイムの調査に 56 ラウンド・プロンプト 650 万トークンを費やした |
| 修正する ADR | ADR-0040（ループガードの隣に姉妹検出器）、ADR-0058（intake は自分で `error:` を前置しない）、ADR-0060 §3（出所で信頼するツールメッセージのフィールドが 2 つ目に） |
| 関連 | ADR-0076（同じセッションが途中で読んだもの、そしてそれを放置する理由） |

## 背景 — 何が起きたか

以下の事実はすべて transcript または再現実行から得たもので、モデル自身の
説明から推定したものは無い。

| 時刻 (JST) | コール | 応答 |
|---|---|---|
| 23:10:03 | `execute_sql_readonly` | 行が返る |
| 23:10:30, 23:10:43 | 関数名を誤った 2 クエリ | 当然の SQL エラー |
| 23:10:54 | 正しいクエリ | **「Required parameter is missing: query」** |
| 23:11:13, 23:11:26 | 正しいクエリ 2 本 | 行が返る |
| 23:11:39 → 23:14:22 | 正しいクエリ 7 本。`execute_sql` も `SELECT 1 AS test` も含む | 毎回同じエラー |
| 23:13:13 | `list_table_ids`（同じサーバー） | テーブル一覧 |
| 23:16:34 → 23:18:47 | `dryRun: false` 付きの 11 クエリ | 行が返る |

- 失敗した 10 コールはすべて `query` を持っていた: transcript の引数・ゲートの
  詳細行・コードが一致する。実行器が引数に加える唯一の編集は自分の
  `gem_agent_purpose` フィールドの除去（ADR-0047 §2）で、`mcp.Client.CallTool`
  は map をそのまま marshal する。
- 1 時間後に同じ mcp-bridge バイナリと同じトークンコマンドで再現すると、
  4 形すべて — `dryRun` 無し・`dryRun: false`・失敗した複数行クエリそのまま・
  `SELECT 1 AS test` — が行を返す。失敗するのは未宣言の引数だけで、しかも
  別の文言（「Request contains an invalid argument.」）。サーバー自身のスキーマは
  `dryRun` を optional と宣言している。障害はサーバー側の一過性だった。経路上に
  リクエストを記録するものは無い — mcp-bridge は設計上ログを持たず、gem-agent は
  `GEMAGENT_MCP_STDERR=1` でなければ MCP の stderr を捨てる — ので送出バイト列は
  示せない。再現実行が証拠である。
- モデルが見つけた「修正」`dryRun: false` は、何も送らなかった 2 分の空白
  （bridge の 90 秒 idle 接続タイムアウトより長い）の後に来た。サーバーの回復と
  重なっただけである。

最初のエラーから最後のエラーまでにモデルがした順序: クエリを言い換えて再試行
（3 回。ADR-0040 のループガードは同一の正規化引数で判定するため発火しなかった）、
変更系の兄弟 `execute_sql` に切替（操作者が承認）、ゲートに達しない read レーンの
シェルコマンド 12 本 — `ps aux`、`~/.config/gem-agent/mcp.json` の `ls`/`grep`、
`cat ~/.config/mcp-bridge/config.json`、bridge バイナリへの `strings`、`~/works`
配下の `find`、sandbox 内で自前の `mcp-bridge run bigquery`（read レーンに
ネットワークが無いのでトークンコマンドで失敗）—、GitHub 検索（2 回目は操作者が
拒否）、Web 検索、そして推測。

## 背景 — なぜランタイムはそれを許したか

これはモデルの欠陥ではなくランタイムの欠陥である。理由は 3 つ。

1. **モデルは拒否されたコール 2 種に規則を持ち、失敗したコールには持たない。**
   システムプロンプトはゲート拒否について「a decision, not an obstacle — ask how
   to proceed instead of retrying」、レーン拒否について「needs the lane the refusal
   names, not a retry」と言う。どちらも該当する瞬間に function response で届く
   契機で、このセッションでも両方が機能した（23:08 の拒否後、モデルは止まって
   プロジェクト id を尋ねた）。リモートサーバーのエラーには規則も契機も無い。
   モデルは誰も書いていない経路 — ランタイムのデバッグ — で空白を埋めた。
   ADR-0062（契機のない能力は発火しない）と ADR-0063（禁止は第 3 の経路を生む）
   の教訓の再演であり、欠けているのは契機であって禁止ではない。
2. **モデルはそのエラーが誰の言葉か判別できない。** 転送失敗とサーバー報告の
   `isError` はどちらも `error:` で始まる。前者はたまたまサーバーとツールを
   名指しし後者はしないが、どちらも誰が話しているか・引数が届いたかを言わない。
   ランタイムは両方を知っている: 実行器は出所を知り、引数を無改変で渡したことも
   知っている。
3. **唯一存在する検出器はこの信号を見られない。** ループガードは設計上、同一
   引数で判定する — 構文エラーの反復試行は外から見れば同じ形で、これを発火
   させてはならない。ここに存在した信号 — 異なる引数に対する同一エラー文 —
   はループガードが無視するものそのもので、しかも引数が原因でないことの最強の
   証拠である。数えるのは機械的で、言い換えを跨いでモデルが数えるのは不得手。

## 決定

### 1. 出所は描画に載せる: そのエラーは誰の言葉か

失敗した MCP コールの出所は 3 つあり、レビュー（§5）は 3 つ目が 2 つ目の中に
隠れていることを見つけた: サーバーの `isError` 結果、サーバーの JSON-RPC エラー
オブジェクト（`-32602 Invalid params` など — これもサーバーの言葉だが、結果では
なくエラーとして届く）、そしてランタイム失敗（転送・タイムアウト・終了・
フレーミング）。MCP アダプタは全失敗を型付きの値 `*tools.RemoteError{Server,
Tool, Kind, Text, Sent}` として `Run` のエラー戻りで返し、`mcp.Client.CallTool` は
自身のあらゆる失敗 — 起動しないサーバーも含む — を型付きの `*mcp.CallError` で
包むので、アダプタは文字列照合なしに `*mcp.RPCError` と転送原因を区別できる。
`Sent` はコールの引数がそもそもサーバーへ書き出されたかというクライアントの事実:
拒絶とは送出されたコールをサーバーが拒むことであり、`initialize` に答える
`RPCError` はサーバーが起動を拒んだのであって、そのコールは未完了かつ未送出
（リリース前レビュー、§5）。実行器は `errors.As` で検出し
（`RoundLimitError` の ADR-0040 規則）、監査 outcome と添付ガードが検査する
`error:` 接頭辞を保って描画する:

- 結果: `error: MCP server "bigquery" answered execute_sql_readonly with an
  error:` に続けてサーバーの文
- 拒絶: `error: MCP server "bigquery" rejected the call to execute_sql_readonly:
  rpc error -32602: …`
- ランタイム: `error: gem-agent could not complete execute_sql_readonly on MCP
  server "bigquery": <cause>`

intake（`mcpIntake.render`）は予算と退避の役目を保ち、自分で `error:` を
前置するのをやめる（ADR-0058 修正）。リモートツール名はこれらの文の中に現れるが、
どの結果と同じくデータとしてラップされて運ばれる。

### 2. 実行器にツール単位の障害カウンタ、同一エラー文で数える

- キー: registry のツール名（サーバー + ツール）。数えるもの: そのツールの
  連続した失敗結果のうち、文がバイト単位で同一のもの。同じツールの他の結果 —
  行が返る・別のエラー — はリセットする。拒否や中断されたコールはサーバーの
  結果ではないので触らない。閾値 `mcpFaultThreshold = 3`、ループガードと同じ
  数で、梯子の段長が 1 つになる。
- サーバー単位ではなくツール単位: `execute_sql` の障害の最中に `list_table_ids`
  は成功した。観測された粒度はツールである。
- どのエラーでもなく同一文: モデル自身の構文エラー反復は毎回別の文になるので
  発火させてはならず、サーバー障害は引数が何であれ同じ文を返す。受け入れる
  限界: リクエスト id やタイムスタンプを埋め込むエラーは毎回異なり発火しない —
  ラウンド梯子が天井のまま。リリース後に測る。設計で回避しない。
- ターン単位、ループガードの状態と同じ: どちらも毎ターン（`Run`）で初期化
  される。報告を読んだ操作者が「もう一度」と言ったとき、最初のコールで注記が
  返ることはない。`/clear` は新しいターンを始めるので新しいカウントになる。
- ループガードとは独立: 同一コール 3 回は今までどおり ADR-0040 が先に発火し、
  言い換えた 3 コールに 1 つの答えはこちらが発火する。

### 3. 閾値でランタイムが語る — ノンスタグの外側で、出所により

`llm.Message` にツールロールのフィールド `runtime_note` を足す（追加のみ、
`denial` と同じ）。設定する関数は 1 つ — ツールメッセージを組み立てる
`Agent.Run` — で、カウンタが閾値に達したとき、以降は同一エラーごとに更新
されたカウントで設定する。`wrapToolMessages` はラップしたサーバー文の後に
これを付ける — 添付注記に既に使っている機構。ランタイムの言葉は gem-agent の
もので、システムプロンプトと同じ信頼水準にある。サーバーの文はデータとして
ラップされたまま。注記を内容で認識する案は ADR-0060 §3 の理由で却下:
注記の形をした文字列を返すサーバーがラップ無しで通ってはならない。

注記はランタイムが計測したことを述べ、行動を名指しする — このモデルが既に
従っている拒否規則と同じ形。ツールは registry 名
（`mcp__bigquery__execute_sql_readonly`）で呼ぶ。これはモデルが全リクエストで
ラップ無しに持っている識別子であり、サーバーが供給した名前は使わない。
サーバーが話した場合（結果または拒絶）:

> gem-agent: MCP server "bigquery" has answered
> mcp__bigquery__execute_sql_readonly with this same error 3 times in a
> row. gem-agent sent each call's arguments to the server exactly as
> you wrote them, removing only its own gem_agent_purpose field. Tell
> the user what you asked and what the server answered, and ask how to
> proceed.

送出したコールを gem-agent が完了できなかった場合:

> gem-agent: 3 calls in a row to mcp__bigquery__execute_sql_readonly
> could not be completed, each failing the same way (the result above
> says how). gem-agent sent each call's arguments exactly as you wrote
> them. Tell the user what you asked and what happened, and ask how to
> proceed.

コールがサーバーに届かなかった場合（サーバーが起動しない、書き込めない）、
注記は送出を主張しない:

> gem-agent: 3 calls in a row to mcp__bigquery__execute_sql_readonly
> could not be completed — each failed before the call reached the
> server (the result above says how). Tell the user what you asked and
> what happened, and ask how to proceed.

どちらの注記も誰の責任かを言わない。ランタイムが計測したのは 2 つの事実 —
引数は無改変で出た、答えは繰り返された — で、レビュー（§5）は初稿の判定
（「not a fault in your arguments or in this runtime」）がその両方を超えて
いたことを示した: 必須パラメータ名を一貫して誤るモデルはどう言い換えても同じ
サーバーエラーを受け、タイムアウトは `[mcp].call_timeout_sec`、gem-agent 自身の
設定である。行動はどちらでも同じで、拒否規則が求めるものと同じ。

禁止は書かない。ADR-0063 は禁止が何をするかを実測した: 一般化し、第 4 の
経路を生む。契機と名指しされた行動が指示の全部である。

`-p` では尋ねる相手がいない。モデルの報告がターンを終える — 拒否と同じ
（ADR-0060）。`--auto` では操作者がいて報告を読む。transcript には閾値到達時と
以降のヒットごとに `mcp_fault` レコード `{server, tool, kind, sent, count, round,
error}`（error は切詰め）が入り、注記の効果をリリース後に測れる（§5、論点 2）。
操作者には連続 1 回につき閾値で通知 1 行。テレメトリは不変: `tool.call` は
既にコール毎の `outcome=error` を持つ。

### 4. しないこと、その理由

- **ランタイム再試行は無し。** 透過的な再試行はサーバーの状態を隠し、モデル
  自身の再試行が再試行である。
- **エラー文の分類は無し**（一過性か恒久か、サーバーの責任かモデルの責任か）。
  文は無限領域であり、分類できたはずのサーバーはしなかった。ランタイムが計測
  したのは §3 の言うことだけ。
- **ハードストップは無し。** 梯子は昇るだけで殺さない（ADR-0040）: 注記、
  それからラウンド梯子。
- **プロンプト変更は無し。** 契機は該当する瞬間に function response で届く —
  ターン途中のモデルに届く唯一のスロット（ADR-0012、ADR-0060 §2）。

### 5. 独立レビュー（2026-09-06）と改訂

fresh context の検証者によるコード検証つきレビュー（Seatbelt と挙動の主張は
全件再実行）が初稿に加えた変更:

- **3 つ目の出所。** `rawCall` はサーバーの JSON-RPC エラーオブジェクトを
  エラー戻りで返すので、初稿の 2 形では `-32602 Invalid params` が「gem-agent
  could not complete」と描画されていた。§1 は 3 種を型で端から端まで区別する。
- **注記は registry 名を持つ。** リモートツール名ではない: ラップ無しの位置は
  ADR-0060 §3 が gem-agent と操作者の言葉のために確保したもので、サーバーの
  ツール名は第三者の文字列。
- **注記は計測したことだけを断定する** — §3 参照。初稿の「not a fault in your
  arguments or in this runtime」は計測されておらず、常に真でもなかった。
- **ターン単位の寿命を明記。** 初稿はカウンタを「ループガードの状態と共に
  `/clear` で消す」と書いたが、ループガードの状態は `/clear` ではなく毎ターン
  で消える。§2 はターン単位と言う。
- **アーキテクチャテストは両フィールドを固定。** 初稿は「`Denial` と同じく」
  archtest が `RuntimeNote` を固定すると書いたが、`Denial` にそのテストは
  無かった — 挙動テストのみ。`TestProvenanceFieldsAreSetOnce` が両方を
  `Agent.Run` に固定する。
- **事実の訂正。** `--auto` には操作者がいる（初稿は `-p` と同列にした）。
  mcp-bridge が pass-through であることは README の scope 記述であり、その
  ADR-0001（事前登録 OAuth クライアントについて）ではない。転送失敗と
  サーバー報告エラーは形で既に区別可能だった — 欠けていたのは出所を声に出す
  ことである。
- **論点の決着。** 閾値 3（ループガードと同じ段長。誤免責のリスクは閾値が
  下がるほど増える）。中段は v1 では無し。`mcp_fault` レコードが round を持ち、
  注記の効果を先に測る。ランタイム失敗も数え、専用の注記を持つ。

リリース前の実装に対する 2 度目の独立パスが加えたもの:

- **`Sent`。** `CallTool` は起動失敗を包まずに返していたため、`initialize` を
  JSON-RPC エラーで拒むサーバーは「rejected the call」と描画され、3 回の再起動の
  後に注記は引数を送ったと言った — 一度も出ていないのに。クライアントのあらゆる
  失敗は `Sent` 付きの `CallError` で運ばれ、拒絶は送出されたコールを要し、未完了
  の注記には送出を主張しない変種がある。テストは起動拒否・未書込のリクエスト・
  復号できない結果を覆う。
- **文言。** 承認リファレンスと RFP は「ラップを免除されるツール結果はちょうど
  2 つ」と言っていたが、skill 本文も免除され（ADR-0010）、注記は結果を
  アンラップしない — ラップされた結果の後に付く。どちらもそう言うよう改めた。
- **resume。** 出所フィールドは transcript から `json.Unmarshal` で戻り、
  アーキテクチャテストはそれを見ない。transcript は state ディレクトリにあり、
  全レーンの書込到達範囲と file ツールのルートの外なので、これは ADR-0060 §3 が
  `denial` について既に受け入れたクラスであって新しいものではない — 次の読者が
  再発見しないようここに記す。
- 記録のみ・変更なし: 起動失敗の原因文は phase を保つ（`initialize: rpc error …`）。
  操作者通知は英語のみで `make labels` の走査外（エージェント層の通知すべてと
  同じ）。CHANGELOG は ADR を引く（0.1.0 以来の全項目と同じ）。

## 検討した代替案

- **エラー反復についてのプロンプト規則** — 却下。契機のない規則は発火せず
  （ADR-0062、実測）、禁止は第 3 の経路を生む（ADR-0063、実測）。
- **サーバー単位のカウンタ** — 却下: 健全な兄弟ツールがリセットする
  （23:13:13 に観測）。
- **引数もキーに含める** — 却下: それはループガードであり、この検出器は
  ループガードが見られないケースのために存在する。
- **mcp-bridge に上流エラーの注釈を頼む** — 却下。bridge はその scope 記述
  どおり pass-through（ガバナンス層無し・監査ログ無し）で、モデルに必要な事実 —
  「gem-agent はあなたの書いたものを送った」— はこのランタイムだけが主張できる。
- **調査ができないように read レーンを閉じる** — ADR-0076 の主題で、逆に
  決まった。しかもそれでは再試行・`execute_sql` への昇格・GitHub 検索・Web
  検索は止まらなかった。

## 帰結

- `tools.RemoteError`（3 種）と `mcp.CallError`。MCP アダプタは前者を、
  `CallTool` は後者を返す。実行器は §1 の 3 形を描画し、intake は `error:` を
  前置しない。
- `llm.Message.RuntimeNote`（`runtime_note`）。transcript のスキーマ版は不変
  （追加のみ）。旧ビルドが新 transcript を resume するとフィールドを無視する:
  再生から注記が消えるだけで、安全性に影響なし。
- カウンタはエージェント内でループガードのターン単位状態の隣に住み、毎ターン
  初期化される。
- `mcp_fault` transcript レコード（`sent` を含む）と、連続 1 回につき操作者通知
  1 行。resume した transcript は両出所フィールドを書かれたまま復元する —
  state ディレクトリは全レーンと全 file ツールの到達範囲の外（§5）。
- テスト: カウンタ（行と別の文でリセット、拒否では不変、3 で発火し 4 でも
  発火、ツール単位）、ラップ（フィールドが設定されたときだけ注記がタグ外に
  乗る。注記と同文のツール結果はラップされたまま）、3 形の描画と `errors.As`
  検出、アダプタの `isError`・RPC エラー・転送失敗の対応付け、`mcp_fault`
  レコード、4d6bb685 の形の再現 — 言い換えた 3 コールに 1 つの答え — が注記で
  終わること、`internal/archtest` の `TestProvenanceFieldsAreSetOnce`。
- ドキュメント: README（承認の段落、両言語）、sessions リファレンス（レコードと
  フィールド）、approval リファレンス（信頼される 2 つ目のフィールド）、
  architecture リファレンス（ラップ）、tools・integration リファレンス（3 形と
  注記）、RFP のセキュリティ第 3 層、AGENTS.md の gotcha、CHANGELOG。ADR-0040・
  ADR-0058・ADR-0060 に *修正* 行。
