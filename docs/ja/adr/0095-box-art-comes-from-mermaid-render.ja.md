# ADR-0095: 罫線表示は mermaid-render で描く

| 項目 | 値 |
|-------|-----|
| Status | **Accepted**（2026-09-30）— 実装済み |
| Date | 2026-09-30 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | mermaid-render RFP 第 2 段階 2e（2026-09-30 の運用者の判断）: 罫線表示を自前に置き換え、`mermaid-ascii` を外す |
| Relates to | [ADR-0042](0042-terminal-diagrams.ja.md)（**描画器・変換表・2 つの忠実さの確かめを本記録で置き換える**。§4 の逐語の `-p` と素の REPL はそのまま）、[ADR-0063](0063-diagram-fences-render-in-place.ja.md)（レーンはそのまま: 罫線は Markdown の描画を通さず、拒否は注記つきでソースを見せ、未対応の種類は黙って通す）、[ADR-0092](0092-mermaid-fences-render-as-pictures.ja.md)（§1: 画像プロトコルの無い TUI はレーンのまま — 描くのは本エンジンになる）、[ADR-0093](0093-outside-text-is-made-inert-for-the-terminal.ja.md)（罫線は引き続き `inertArt` を通る） |

## Context

画像を描かない端末の罫線は `AlexanderGrooff/mermaid-ascii` が描いている。組織が外部のコードを出荷しないと
決める前に採ったコミュニティ製のコードである。中身を確かめられないので、ADR-0042 はそれを、mermaid をその文法へ
書き換える表（形を箱に、`-- text -->` を `-->|text|` に、ラベルの `&` を ＆ に）と 2 つの確かめ（ラベルが
すべてある、線 1 本に矢印 1 つ）で包んだ。実データで測ると、flowchart と ER の 26 ブロックのうち 21 を描き、
ASCII 以外のラベルの sequence は拒み、BT を TD、RL を LR で描き、`Z --> S` で幻のノードを描いた。

mermaid-render は今、flowchart / graph・sequenceDiagram・erDiagram を罫線で描く（`raster.RenderText`）。絵と
同じ構文解析から: 絵の層の配置をセルの格子で走らせ、ER は多重度を mermaid の記法で書いた表、sequence は専用の
列で。描くたびに格子の上で確かめ、欠陥は拒む。実データの 43 ブロックがすべて描け（日本語の sequence も）、
運用者はそのうち 15 を 4 回の目視で通した。

## Decision

### 1. レーンは mermaid-render で描く

画像プロトコルの無い TUI では、mermaid のフェンスを書かれたまま `raster.RenderText` に渡す。
`TextOptions.Width` は TUI 自身のセルの測り方（rune ごとの `ansi.StringWidth`）で、罫線と画面の他の部分を
合わせる。結果は今と同じ罫線の区切り: glamour を通さず（ADR-0063）、`inertArt` を通る（ADR-0093）。

### 2. 外すもの

`mermaid-ascii`（go.mod から外れる）、変換表、ラベルと矢印の確かめ、ASCII 以外の sequence の拒否。エンジンは
mermaid 12.0.0 と同じように読み、自分の罫線を自分で確かめる。書き換える先も、推し量る中身も、もう無い。

### 3. 結果

- 描けたフェンスは罫線。
- 未対応の図の種類（pie・state・gantt・mindmap など）は ADR-0063 §4 のとおり黙ってソースのまま。
- それ以外のエラー — 構文の誤り、未対応の構文、配置の欠陥、エンジンの panic — は注記 1 行つきでソースを見せる。

`-p` と素の REPL は逐語のまま（ADR-0042 §4）。

## Consequences

- 日本語ラベルの sequence が描ける。BT と RL がその向きで描ける。罫線が絵と同じものを読むので、幻のノードの類は
  無くなる。
- バイナリから mermaid-ascii が抜け（差し引き約 4.3 MB、RFP §4）、コミュニティ製のモジュールがサプライチェーン
  から外れる。
- East Asian の曖昧幅の文字を 2 桁で描く設定の端末では、今と同じく罫線がずれる: 問い合わせなしには分からない。
- 見た目は mermaid-ascii と違う（新しい見た目は運用者の目視で決めた）。絵が拒む図は罫線でも拒む。

## Alternatives considered

- **flowchart だけ mermaid-ascii を残す。** 却下: エンジンが扱わない場合が無いのに、コミュニティ製のコードと
  書き換えの表を残すことになる。
- **絵だけにして他はソース。** 却下: 画像プロトコルの無い端末（Terminal.app、多重化ソフト）は今ある図を失う。
