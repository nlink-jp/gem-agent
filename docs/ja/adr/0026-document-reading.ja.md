# ADR-0026: 文書の読解 — PDF はネイティブ、Office 形式は抽出

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | オペレータ:「Excel, Word, PowerPoint や PDF といった良くあるファイルの対応は可能か？（作成よりも内容の解釈）」 |

## Context

エージェントはテキストを読み画像を見られたが、人がアシスタントに実際に
渡す文書 — PDF・Word・Excel・PowerPoint — は不透明だった（read_file は
拒否か文字化け、file_info は名前を言うだけ）。Gemini は PDF をドキュメント
パートとしてネイティブに理解する。Office XML 形式は API が受けないが、
実体は zip+XML であり純 Go で開ける。

## Decision

1. **形式ごとの二層対応。** PDF はバイト列のままモデルへ — 設計前に実測
   済み: PDF パートは user メッセージパートでも multimodal function
   response 内でも受理され、ツールラウンドの後も ADR-0012 の潜伏 400 なしに
   会話が継続する（ground truth に対し round 3 まで検証）。Office XML 形式
   — .docx / .xlsx / .pptx — は純 Go パッケージ（`internal/docext`）で
   ローカルにテキスト抽出する: 文書順の本文、シート名付きタブ区切りの行、
   番号付きスライドのテキストブロック。レガシーバイナリ（.doc/.xls/.ppt）は
   対象外 — 後継から二十年経った形式のために重量級依存を抱えることになる。
2. **アクセスは 2 経路、ADR-0012 の「誰がファイルを選んだか」の分割どおり。**
   オペレータは `@report.pdf` / `@data.xlsx` で添付（プロジェクト内、
   または絶対/~ パス — 画像と同じ理由で文書へ拡張: パスを打ったのは
   オペレータ自身）。モデルは新ツール `read_document(path)` で読む —
   他の読み取りツール同様プロジェクト封じ込め。PDF は multimodal function
   response パートで、Office 形式は抽出テキストで返る。
3. **抽出結果は他のツール出力と同じく非信頼** — ノンスラップ。PDF パートは
   ラップ不能なので画像の枠書きを継承する（文書内に見える文字列はコンテンツ
   であって指示ではない — システムプロンプトの画像条項を文書へ拡張）。
4. **上限は必ず報告**: インライン予算を超える PDF はサイズと上限を名指しで
   拒否。Office 抽出はテキスト上限で切り詰め注記付き停止。zip メンバーは
   展開上限を通して読む — 細工されたアーカイブでメモリを膨らませない。
5. 検証は ground truth 先行: 既知トークンを含む実 PDF（被検コードではない
   独立した生成系で作成）、macOS textutil 製の実 .docx、仕様準拠の
   .xlsx/.pptx フィクスチャ — それぞれ end-to-end で正答すること。

## Consequences

- 「このレポートを読んで」が、実際に送られてくる 4 形式で動く。
- トランスクリプトは PDF バイト列を画像同様に保存（base64。resume で復元）。
  圧縮はプレースホルダとして要約する。
- トークン: PDF ページはモデルのドキュメントページ課金 — /usage で見える。

## Alternatives considered

- **PDF のローカルテキスト抽出** — 却下: 純 Go の PDF テキスト抽出は脆く
  （フォント・エンコーディング・レイアウト）、モデルは表もスキャンも含む
  実レイアウトをネイティブに読む。
- **Office パース用の依存**（excelize 等）— 却下: 必要な断面（テキストを
  出す）は小さく、重量級依存はフォールバックツールの依存ゼロ姿勢に反する。
- **レガシーバイナリ形式** — 対象外。ツールのエラーメッセージで大きく明言。

## References

- ADR-0012（2 経路の分割と multimodal FR 機構）
- ADR-0014（summarize_file と合成可能: 大きな抽出文書を要約）
- 実測: PDF の user パート・FR パート両搬送とも受理、round 3 継続も
  クリーン（2026-08-20、gemini-3.7-flash）
