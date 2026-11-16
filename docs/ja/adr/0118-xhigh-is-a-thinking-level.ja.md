# ADR-0118: `xhigh` を思考レベルに加える — ダイヤルは 5 つ目の値を取る

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-16) |
| Date | 2026-11-16 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | 操作者から、Gemini 4 の各モデルで使える思考オプションを問われた。調べると、Gemini 4 Hydrogen の最上位のレベルを gem-agent では選べなかった |
| Amends | ADR-0025 §1 と §4（4 つの値）、ADR-0098 §1–2（`/thinking` と /settings の行が取る語） |
| Relates to | ADR-0082、ADR-0083（語彙を共有する枠） |

## Context

ADR-0025 は SDK が名前を持つ 4 つのレベル — `minimal`・`low`・`medium`・
`high` — を受け付け、どれをモデルが使えるかの判定は API に委ねた（§4）。
Gemini 4 Hydrogen は `HIGH` の上に 5 つ目の `XHIGH` を持つ。提供元のレベル
対応表（2026-11-16 に読んだもの）:

| モデル | レベル | 既定 |
|---|---|---|
| Gemini 4 Carbon | LOW、MEDIUM、HIGH | MEDIUM |
| Gemini 4 Argon | LOW、MEDIUM、HIGH | MEDIUM |
| Gemini 4 Hydrogen | LOW、MEDIUM、HIGH、XHIGH | HIGH |

gem-agent は、レベルを指定する 3 か所 — 起動時の設定ファイル、
`--thinking`、`/thinking` — のすべてで `xhigh` という語を断るので、この
レベルには届かない。

SDK（`google.golang.org/genai` v1.77.0、最新版）にこの値の定数は無い。
`ThinkingLevel` 型は通信に載る文字列そのものなので、定数が無くても値は
送れる。

**実測**（`internal/llm/levels_live_test.go`、2026-11-16、小さな算数の
問い 1 つ、レベルごとに 1 回）:

| モデル | 送ったレベル | 結果 |
|---|---|---|
| Gemini 4 Hydrogen | （なし） | 受理、思考 78 トークン |
| Gemini 4 Hydrogen | `LOW` | 受理、145 |
| Gemini 4 Hydrogen | `MEDIUM` | 受理、232 |
| Gemini 4 Hydrogen | `HIGH` | 受理、201 |
| Gemini 4 Hydrogen | `XHIGH` | 受理、289 |
| Gemini 4 Hydrogen | `MINIMAL` | 400 `Thinking level is unsupported: THINKING_LEVEL_MINIMAL` |
| Gemini 4 Carbon | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |
| Gemini 4 Carbon | `MINIMAL` | 400 `Thinking level is unsupported: THINKING_LEVEL_MINIMAL` |
| Gemini 4 Argon | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |
| Gemini 4 Argon | `MINIMAL` | 400 `Thinking level is unsupported: THINKING_LEVEL_MINIMAL` |
| Gemini 3.8 Flash | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |
| Gemini 3.7 Flash | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |

どの行も 1 回の呼び出しである。値がモデルに届くこと、どのモデルが受け
付けるかは分かるが、レベルごとの費用は分からない — この小ささの問いでは
数が順にも並ばない。拒否は最初のリクエストで返り、どれにも gem-agent の
ヒント（レベルの名前と戻し方、ADR-0098 §7）が既に付いていた。Gemini 3 の
2 行は、独立したレビューが「古いモデルは知らないレベルを無視するのでは
ないか」と問うたあとで測った。同じ形で断る。

**セッション中の切り替えは `xhigh` をまたいでも成り立つ**
（`levelswitch_live_test.go`、Gemini 4 Hydrogen、同日）: `xhigh` で行った
ツール呼び出しを、署名つきで `low` で再生 — 受理。その会話を `xhigh` と
モデルの既定で再生 — 受理。ADR-0098 が `high` と `low` で測った前提を、
新しいレベルでも測った。

## Decision

1. **`xhigh` を 1 つの語彙に加える。** レベルを指定するすべての場所で
   受け付ける: `[model].thinking`、`[model].risk_thinking`、
   `[model].librarian_thinking`、`--thinking`、`/thinking`、/settings の
   `model.thinking` 行。行は `default, minimal, low, medium, high, xhigh` を
   巡回する。これらが使う判定は、引き続き `config.ValidThinking` の 1 つ
   だけである。語はそこにある 1 つの一覧に置く: 判定・起動時のエラー・行の
   巡回はこの一覧を読み、語を足すと、通信の値、両言語の使い方とヘルプの行、
   フラグの説明がその語を書くまでテストが落ちる。
2. **通信には文字列 `XHIGH` で載せる。** SDK に定数が無いので gem-agent が
   定数を定義する。値はテストで固定する。
3. **どのモデルが受け付けるかは、引き続き API が決める**（ADR-0025 §4）。
   対応しないモデルに `xhigh` を指定すると、次のリクエストが上で実測した
   400 と既存のヒントで失敗する。この場合のために新しく作るものは無い。

## Not taken

- **モデルとレベルの対応表をコードに持ち、リクエストの前に断る。** モデル
  名は設定であってコードには書かない。表は次のモデルでまた誤りになる。
  API の拒否は即座で、レベルを名指す。
- **どんな語でも API に渡す。** 打ち間違いが起動時ではなく最初のリクエストで
  失敗するようになり、/settings の行が巡回する値も無くなる。
- **SDK が値に名前を付けるのを待つ。** 型は通信に載る文字列そのものなので、
  待つものが無い。定数が現れたら置き換え、テストは値を固定し続ける。
- **トークン数で指定するつまみ（`thinking_budget`）。** 引き続き出さない
  （ADR-0025 の Consequences）。提供元のモデルのページは、Gemini 4 の
  3 モデルが 400 で断ると書いている。gem-agent はこれを送らないので、
  読んだことであって測ったことではない。

## Consequences

- Gemini 4 Hydrogen の最上位のレベルを、起動時にもセッション中にも選べる。
- 思考トークンは出力として課金され、1 回の応答の出力上限に数えられる。
  ここまで高いレベルは、答えの前に上限を使い切ることがある。その場合の
  既存のメッセージは、既に `/thinking` を名指している。
- /settings の行が巡回する値が 1 つ増える。

## References

- ADR-0025（レベルと、対応がモデルごとであること）
- ADR-0098（セッション中のダイヤルと 400 のヒント）
