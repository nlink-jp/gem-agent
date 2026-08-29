# ADR-0057: すべてのモデル呼び出しは会計レコードを残す

| 項目 | 値 |
|------|----|
| ステータス | **Accepted** |
| 日付 | 2026-08-30 |
| 対象 | gem-agent |
| 意思決定者 | nlink-jp メンテナ |
| きっかけ | オペレーター:「セッションのコストを transcript からカタログ単価で計算できるか。API はリクエストごとのコストを返すか」 |
| 修正対象 | ADR-0019（side-call 会計）、ADR-0005（transcript レコード）、ADR-0035 §1（監査イベント） |

## 背景

**API は金額を返さない。** Vertex が返すのは `usageMetadata` のトークン数
だけであり、Cloud Billing が出すのは SKU × 日次のコストで、セッション・
ターン・呼び出しへは割り当てられない。したがって「このセッションはいくら
だったか」への唯一の経路は**トークン数 × カタログ単価**であり、そのために
は呼び出した瞬間にカウントがディスクへ落ちている必要がある — プロセスが
終われば in-memory の集計は道連れになる。

このマシンの transcript 63 本（主ループ 747 ラウンド）を実測: prompt 78.5M
（うち cached 64.4M）、output 258k、thoughts 92k。記録には 3 つの穴がある。

1. **リスク評価と圧縮は何も書いていない。** `/usage` 用の `Stats` を更新
   するだけで、終了時に消える。transcript には `tier: "review"` の
   `auto_decision` が 309 行あり、その 1 行ごとにモデル呼び出しが 1 回
   あったのに、そのトークンはもうどこにも無い。
2. **side-call レコードは `prompt` と `output` しか持たない**
   （`summary_usage` / `web_search` / `web_fetch` /
   `agentic_search_usage`）。thinking トークンは output として課金され、
   cached prompt トークンは割引単価で課金されるので、これらが無いレコード
   は値付けできない — 推測しかできない。
3. **どのモデルが使ったかが書かれていない。** ヘッダにあるのは主モデルで、
   別単価の要約モデルや fetch モデルはレコード種別ごとの散文にしかない。

計算そのものは 2 つの実測で確定した。`prompt + candidates + thoughts =
total`（実測 25 + 174 + 534 = 733）: thinking トークンは `candidates` の
一部ではなく**独立したバケツ**で、課金上は output 扱い。そして
`cached ⊆ prompt`（183 ラウンドで反例なし）: cached は prompt への加算では
なく、割引される**内訳**である。ここを取り違えた集計は倍数で外れる — この
マシンでは prompt の 82% がキャッシュヒットである。

## 決定

### 1. レコード種別は 1 つ、形は 1 つ、モデル呼び出し 1 回につき 1 行

プロセス内のすべてのモデル呼び出し — 主ループ・リスク評価・進捗レビュー・
圧縮・`summarize_file`・`web_search`・`web_fetch`・ファイル検索の子エージェ
ント — がちょうど 1 つの `usage` レコードを書く:

    {"kind":"usage","data":{"source":"risk","model":"…","prompt":4183,
     "output":42,"thoughts":81,"cached":0,"total":4306}}

集計に必要な新しい次元は `source` だけであり、`model` があることでヘッダと
join せずにレコード単体で値付けできる。

### 2. `total` は API 自身の数値 — チェックサムとして持つ

導出せず `usageMetadata.totalTokenCount` をそのまま記録する。thoughts が
別枠で課金されることを忘れた集計は、静かに過小計上するのではなく
`prompt + output + thoughts == total` で落ちる。

### 3. 記述系レコードからトークン欄を外す

`web_search` はクエリとソース数、`summary_usage` はパス、`web_fetch` は URL
と status、`agentic_search_usage` は質問とラウンド数を持ち続ける — トークン
は隣の `usage` レコードにある。数えられる場所が 2 箇所あるのは、最初の集計
ツールを待っている二重計上バグである。

### 4. ヘッダにリージョンを記録する

単価は SKU × リージョンで解決されるので、`location` を schema・version・
model・project に加える。

### 5. 監査ストリームも揃える

`model.usage`（ADR-0035）に `thought_tokens` と `total_tokens` を追加し、
Cloud Logging から全体を集計する場合もローカル transcript と同じ算術が
使えるようにする。メタデータのみという原則は不変 — プロンプトも内容も出さ
ない。

### スコープ外

単価表とコストレポート。この ADR が買うのは「後から計算できる」という可能性
であって、意図的にやらないのは、価格が動く世界で価格表をツールに焼き込む
ことである。

## 影響

- transcript はそのセッションの完全な会計文書になる: `source` で合計し、
  `model` で値付けし、`total` で検算する。
- 古い transcript は読めるが不完全なまま。`source` の無い `usage` レコード
  は 0057 以前の主ループのラウンドであり、集計してよいが、そのファイルは
  部分的だと報告しなければならない — リスク評価と圧縮の消費は記録されて
  いない。
- スキーマ更新ではない。usage レコードは診断用で `Load` は無視するため、
  resume には影響しない。
- transcript の増加はモデル呼び出しあたり数百バイト — 既にエージェントが
  読んだファイル全文を抱えている相手としては誤差である。
