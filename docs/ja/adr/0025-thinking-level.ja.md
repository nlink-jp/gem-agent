# ADR-0025: 思考レベルの設定

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | オペレータ:「思考レベルの設定もあるとよいと考えました」 |

## Context

Gemini 3 はリクエスト単位の思考レベル（MINIMAL / LOW / MEDIUM / HIGH）を
持つが、gem-agent は常にモデル既定で走っていた。思考トークンは毎ラウンド
課金され（フッターと /usage が既に表示している）、仕事によって欲しい深さは
違う — 即答の調べ物に HIGH は要らず、リファクタには欲しいかもしれない。

## Decision

1. **`[model] thinking = "minimal" | "low" | "medium" | "high"`**。
   空/未設定はモデル自身の既定。未知の値は起動エラー — 黙ったフォール
   バックにしない（strict config の原則）。
2. **レベルはメインモデルの全 `ChatStream` 呼び出しに適用** — 会話ループ、
   および同じバックエンドに乗るリスク評価・圧縮の側呼び出し。要約モデル
   （ADR-0014）は自身の既定のまま: `WithModel` は意図的にレベルを継承せず、
   グラウンディング検索 / URL コンテキストの側呼び出しも不変。
3. **/settings は出所付きの読み取り専用表示**（モデル名と同格）: 変更は
   config 編集 + 再起動。ライブ編集は検討の上で見送り — めったに変えない
   設定のために、共有 Vertex クライアントに最初の可変フィールドを
   持ち込むことになる。
4. 検証は実測 — 済み: gemini-3.7-flash に同一の算数プロンプトで
   93 (low) / 170 (medium) / 222 (high) thought トークン。"minimal" は
   同モデルが初回ターンで明瞭な 400（"Thinking level is unsupported"）で
   拒否した。受け付けるレベルはモデル依存であり、config は SDK の 4 値を
   すべて受け、判定は API に委ねる — 失敗は大きく・即座で・レベル名を
   名指すから。

## Consequences

- config キー 1 つで思考トークンの支出が操作可能になる。
- ThinkingBudget（2.5 世代のトークン数ノブ）は意図的に出さない: gem-agent
  は Gemini 3.x 前提で、1 つのダイヤルに 2 つのノブは設定の衝突を招く。

## References

- ADR-0009（読み取り専用の設定行は理由を持つ）
- ADR-0014（意図的に影響させない要約モデル）
- ADR-0019（効果を可視化する利用会計）
