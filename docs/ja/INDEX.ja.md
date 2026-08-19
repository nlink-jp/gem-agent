# ドキュメント索引

gem-agent の保守者向けドキュメントの入口。利用者向けは
[`README.ja.md`](../../README.ja.md) を参照。

英語版ミラー: [`INDEX.md`](../en/INDEX.md)（完全対応。
`scripts/docs-mirror-check.sh` で機械検証）。

## 仕様

- [`gem-agent-rfp.ja.md`](gem-agent-rfp.ja.md) — 仕様の正本: 問題設定、機能面、
  スコープ境界、フェーズ計画。ここから外れる機能は ADR を必要とする。実際に
  セッション再開とコンテキスト圧縮はその経路で入った。

## Reference（現状）

現在の挙動。evergreen でコードに追随して in-place 更新する。

- [`reference/architecture.ja.md`](reference/architecture.ja.md) —
  パッケージ構成、ターンループ、2 つの封じ込め境界、永続化、失敗時挙動の一覧
- [`reference/drill.ja.md`](reference/drill.ja.md) — 月次訓練: 勝手に腐るものは
  何か、それを捕まえる手順、初回実行の記録
- [`reference/promotion.ja.md`](reference/promotion.ja.md) — lab-series から
  cli-series へ移る際の確認可能な基準と現状

## ADR

ある時点の設計判断。承認後は immutable であり、判断を変えるときは新しい ADR で
supersede する（typo とリンク修正は例外）。

- [`ADR-0001`](adr/0001-sandbox-mechanism.ja.md) — sandbox-exec + MITL:
  意思決定の境界と封じ込めの境界を分けた 2 層
- [`ADR-0002`](adr/0002-tui.ja.md) — Bubble Tea inline 方式。素の
  スクロールバックとコピペを保つため alt-screen を却下
- [`ADR-0003`](adr/0003-bottom-pinned-layout.ja.md) — alt-screen 無しで
  入力欄を最下部に固定する方式
- [`ADR-0004`](adr/0004-auto-approve.ja.md) — 自動承認の二層ラダー:
  モデルが持ち上げられないルールの床 + モデルの判断
- [`ADR-0005`](adr/0005-session-resume.ja.md) — セッションログを resume の
  正本にする。プロジェクトとモデルは警告ではなく拒否
- [`ADR-0006`](adr/0006-context-compaction.ja.md) — ウィンドウで死ぬ代わりに
  古い側を要約する。fail safe であって fail small ではない
- [`ADR-0007`](adr/0007-input-during-a-turn.ja.md) — ターン実行中の入力は
  捨てずに予約する。自動送信は正常終了のときだけ
- [`ADR-0008`](adr/0008-per-tool-approval-policy.ja.md) — ファンクション単位の
  承認ポリシー。プロジェクトは自由に締められ、緩められるのは信頼した場合だけ
- [`ADR-0009`](adr/0009-settings-panel.ja.md) — 出所を見せる設定パネルと、
  コメントを守るための機械所有ポリシーファイル
- [`ADR-0010`](adr/0010-skills.ja.md) — Claude Code の skill 形式をそのまま
  読む。skill の内容は指示であり、封じ込めた読み取りで境界づける
  （設置場所の項は 0011 が supersede）
- [`ADR-0011`](adr/0011-skill-scope-separation.ja.md) — skill は gem-agent
  自身のディレクトリに置く。Claude Code との共有は symlink
- [`ADR-0012`](adr/0012-image-input.ja.md) — 画像入力: オペレータ添付は @
  （クリップボード含む）、モデル閲覧は view_image
- [`ADR-0013`](adr/0013-navigation-tools.ja.md) — list_tree と
  search_files: 方向づけと高速 grep。索引なし・依存なし
- [`ADR-0014`](adr/0014-context-economy-tools.ja.md) — 軽量モデルの
  summarize_file と、read_file の行窓読み
- [`ADR-0015`](adr/0015-edit-file-v2.ja.md) — edit_file v2: 一括アトミック
  編集・診断つき不一致・成功時の証拠
- [`ADR-0016`](adr/0016-file-info.ja.md) — file_info: 内容判定の種別・
  メタデータ・MD5/SHA1/SHA256 三点セット
- [`ADR-0017`](adr/0017-web-tools.ja.md) — グラウンディング検索と要約
  フェッチ。既定でエグレスゲート、SSRF は構造的に死ぬ
- [`ADR-0018`](adr/0018-context-caching.ja.md) — セッションスコープの隔離
  タグで implicit caching が効く。実測 0% → 81〜95%
- [`ADR-0019`](adr/0019-usage-accounting.ja.md) — カテゴリ別の利用会計と
  /usage。側呼び出しがフッターを踏み潰さなくなる

## History（履歴）

supersede された文書の凍結された audit trail。まだ空である（supersede されたものが
無い）。発生したら「何に置き換わったか」の注記を付けてここへ移す — 削除はしない。
supersede された文書の議論が、現在の設計がその形である理由の唯一の記録である
ことが多いからである。
