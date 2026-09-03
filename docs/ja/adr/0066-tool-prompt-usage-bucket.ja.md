# ADR-0066: 4 つ目のバケツ — usage レコードにツール利用プロンプトのトークンを載せる

| 項目 | 値 |
|------|----|
| ステータス | **Accepted** |
| 日付 | 2026-09-03 |
| 対象 | gem-agent |
| 意思決定者 | nlink-jp メンテナ |
| きっかけ | Issue #1（オペレーター、gem-usage-lens の `verify` 経由）: 2026-09-01 の transcript で `web_search` / `web_fetch` のレコード 2 件が ADR-0057 のチェックサムを満たさない |
| 修正対象 | ADR-0057 §1（レコードの形）、§2（チェックサム）、§5（監査ストリーム） |

## 背景

ADR-0057 §2 は、すべての集計側が検算すべき式を
`prompt + output + thoughts == total` と定めた。主ループでの実測
（25 + 174 + 534 = 733）に基づく。実測は正しく、式は間違っていた —
プローブが代表的でなかったからである。主ループが送るのは function
declaration だけで、プロバイダのツールは一度も使わない。したがって
式が落としているバケツを一度も通らない。

SDK 自身の `totalTokenCount` の定義は加数が **4 つ** である
（`genai.GenerateContentResponseUsageMetadata`、v1.54.0）:

> The total number of tokens for the entire request. This is the sum of
> `prompt_token_count`, `candidates_token_count`,
> `tool_use_prompt_token_count`, and `thoughts_token_count`.

`toolUsePromptTokenCount` は「ツール実行の結果としてモデルに入力として
戻されたトークン数」— プロバイダの**組み込み**ツール（Google 検索
グラウンディング、URL コンテキスト）の出力で、モデルは答える前にそれを
読む。入力単価で課金され、`promptTokenCount` には含まれない。
したがって `cached ⊆ prompt` は成り立ったままで、このバケツが
キャッシュされることはない。

gem-agent で組み込みツールを有効にする呼び出し口はちょうど 2 つ:
`web_search`（グラウンディング）と `web_fetch`（URL コンテキスト）、
ADR-0017 のサイドコールである。どちらも `sideUsage` で消費を読むが、
それは 4 バケツと `total` をコピーする。ツールが内容を返したときは
必ず、レコードの `total` が各項の和より大きくなる — そして集計側の
*過小計上*を捕まえるために ADR-0057 が置いたチェックサムが、
レコードそのものを壊れていると報告する。

表面化のしかたについて 2 つの事実:

- 作者のマシンにある web レコード 5 件はすべて釣り合っていた（バケツが
  ゼロだった: グラウンディングがカウントに何も足さなかった検索）。
  報告者のマシンの 2 件は釣り合わなかった。バケツが空の n=5 の
  プローブは「そんなバケツはない」と言った — ADR-0063 の空白なし
  プローブと同じ失敗形: 通ったプローブは、その場合を覆ったプローブ
  ではない。
- gem-usage-lens v0.1.1 はバケツを `total − (prompt + output + thoughts)`
  として導出し、入力として課金する。この導出が厳密なのは、これが
  **唯一の**未記録バケツである間だけ。5 つ目が現れた日から残差は
  2 つの未知数の和になり、transcript の中に両者を分ける手がかりは
  ない。transcript は会計文書である（ADR-0057）— API が言ったことを
  そのまま言うべきで、読み手に名づけさせる残差を残すべきではない。

## 決定

### 1. `usage` レコードが `tool_prompt` を持つ

`llm.Usage` と `session.UsageRecord` に `ToolPrompt`（`"tool_prompt"`）を
追加し、両経路で `ToolUsePromptTokenCount` から埋める — ストリーミング
の accumulator（`Response.ToolPromptTokens`）と非ストリーミングの
`sideUsage`。レコードを組み立てる呼び出し口はすべて 2 つの `logUsage`
ヘルパを通るので、取りこぼす source はない。バケツは web の 2 source
以外では構造的にゼロである。

キーは常に書く。ゼロも含めて、`total` の直前の位置に（加数は
チェックサムの前）:

    {"kind":"usage","data":{"source":"web_fetch","model":"…","prompt":1200,
     "output":900,"thoughts":40,"cached":0,"tool_prompt":7000,"total":9140}}

`cached` と同じく常に書く: キーが**無い**のは 0066 以前のレコードで、
**ゼロ**は測定されたゼロである。`omitempty` はこの 2 つを 1 つに
畳み、集計側から「導出するか信じるか」を決める唯一の合図を奪う。

### 2. チェックサムを言い直す

    prompt + output + thoughts + tool_prompt == total

値付け: `(prompt − cached) × 入力単価 + cached × 入力単価 × 割引 +
tool_prompt × 入力単価 + (output + thoughts) × 出力単価`。キーの無い
レコードについては、集計側は非負の残差を `tool_prompt` として導出して
よい。残差が負なら、それは依然として壊れたレコードである。

### 3. 監査ストリームも揃える

`model.usage`（ADR-0035、ADR-0057 §5）に `tool_prompt_tokens` を追加し、
Cloud Logging から計算した数字が transcript から計算した数字と同じ
算術を使い続けるようにする。引き続きカウントのみ。

### 4. 変えないもの

- **`/usage` と終了レシート。** in-memory の明細はカテゴリごとの
  prompt と output を示し、サイドコールの thoughts と cached は
  もともと省いている。あれは一瞥であり、会計文書は transcript である。
  `UsageStats` は不変 — 主ループに組み込みツールはなく、そこでは
  バケツは構造的にゼロ。
- **`Usage.Empty()`** は 3 項のまま: ツール結果のトークンだけあって
  prompt のトークンが無い呼び出しは存在しない。
- **schema bump ではない。** usage レコードは診断用で、`Load` は
  無視し resume は影響を受けない（ADR-0057 の影響節）。
- **単価表は持たない。** ADR-0057 の「スコープ外」は有効。

### 5. 根拠はツリーに残す

`-tags live` のテストが URL コンテキストの fetch を 1 回発行し、
4 項のチェックサムを 4 項目が非ゼロの状態で検証する。既存の主ループ
実測の隣に置く。受け入れ時の実測（gemini-3.5-flash-lite、global、
RFC 2119 を fetch）:

    prompt=48 output=53 thoughts=0 cached=0 tool_prompt=953 total=1054

取得したページが呼び出しの 9 割を占め、その全トークンが 3 項の
レコードからは見えていなかった。将来 SDK がバケツを動かしたとき、
気づくのはこのテストであって、3 週間後の集計側ではない。

## 影響

- `web_search` と `web_fetch` のレコードが再び釣り合い、その消費は
  残差ではなくレコード単体から値付けできる。
- 0066 以前の transcript はレコードをそのまま保つ。lens 側の導出は
  キーの不在で選ばれるレガシー経路になる。
- モデル呼び出し 1 回あたり transcript に整数 1 つ、`model.usage`
  イベント 1 件あたり属性 1 つ増える。
- 3 項のチェックサムを固定していたユニットテストは、4 項目が非ゼロの
  4 項チェックサムを固定するようになる。抜けが黙って戻ることはない。
