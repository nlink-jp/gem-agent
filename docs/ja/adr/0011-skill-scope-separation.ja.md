# ADR-0011: skill は gem-agent 自身のディレクトリに置き、共有は選択にする

| Field | Value |
|-------|-------|
| Status | **Accepted** — ADR-0010 の個人スコープ設置場所を supersede |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | v0.6.0 を確認したオペレータ:「.claude を直接見ると環境混在問題が発生しそう。MCP は分離しているのに skill は分離されていないのもよくない」 |

*ADR-0076 による修正: §3 の symlink は撤回。`~/.claude` は ADR-0073 以来
資格情報一覧にあり、Seatbelt は解決後のパスで照合するため、リンクした
skill のスクリプトは read/write レーンで失敗する。Claude Code 向けの
skill はグローバルディレクトリへコピーする。レーンが読めるディレクトリへの
symlink は従来どおり探索が辿る。*

## Context

ADR-0010 は個人スコープを `~/.claude/skills/` — Claude Code 自身の生きた
ディレクトリ — に直接向けた。オペレータはリリースから数時間で 2 つの問題を
突いた。

**環境混在。** `~/.claude/` は他ツールの実行環境である。そこに導入された
skill は *Claude Code のために*導入されたもので、その一部は Claude Code を
前提にしている: そのツール名、そのプラグイン、その実行文脈。gem-agent が
それらを黙って全部相続するということは、**主系の環境が変わるたびに副系の挙動が
変わる**ということであり、それはフォールバックが持たないために存在する結合で
ある。しかも Claude Code 側からアンインストールしない限り、gem-agent に別の
（より小さい、あるいは単に違う）skill セットを与える方法が無い。

**前例は既にあり、逆の答えを出していた。** MCP ではまったく同じ問いが逆に
決着している: グローバルスコープは `~/.config/gem-agent/mcp.json` — Claude
Code の*形式*、gem-agent の*場所* — であり、共有されるのはプロジェクト
スコープ（`<project>/.mcp.json`）だけ。リポジトリのファイルはプロジェクトの
環境であって、どちらのツールの環境でもないからである。ADR-0010 はこの対称性を
論拠なく破っていた。

効いている区別はこうである: **形式互換は drop-in。場所の共有は結合。**
ADR-0010 の価値は前者だったのに、後者まで買ってしまっていた。

## Decision

1. **グローバル skill スコープを `~/.config/gem-agent/skills/` に移す** —
   gem-agent 自身のディレクトリ、Claude Code の形式。MCP とまったく同じ
   配置である。skills-series の zip はそのまま unzip できる。gem-agent は
   もう `~/.claude/` を一切読まない。
2. **プロジェクトスコープは `<project>/.claude/skills/` のまま**変更しない。
   そこはリポジトリの環境であり、両エージェントが意図して共有する —
   `<project>/.mcp.json` や `CLAUDE.md` と同じ立場である。
3. **Claude Code との共有は、オペレータが張る symlink とする:**

   ```sh
   # 1 つだけ共有
   ln -s ~/.claude/skills/meeting-notes ~/.config/gem-agent/skills/meeting-notes
   # 全部共有（意図して）
   ln -s ~/.claude/skills ~/.config/gem-agent/skills
   ```

   探索は symlink を辿る（リンクされた skill ディレクトリは実体と同様に
   発見される）。ADR-0010 §4 の読み取り封じ込めは*解決後の*ディレクトリに
   適用されるので、リンクされた skill の補助ファイルは動き、境界も保たれる。
   全共有は 1 行で済む — v0.6.0 との違いは、**その 1 行をオペレータが書いた**
   ことである。
4. スコープ表記は MCP の語彙に合わせる: `[global]` と `[project]`。

## Consequences

- フォールバックの skill セットが、主系の暗黙コピーではなくオペレータの決定に
  なる。skill 単位でも丸ごとでも、2 つの環境を意図的に分岐できる —
  gem-agent 専用 skill、Claude Code 専用 skill、完全共有、どれも
  ファイルシステム操作 1 回である。
- v0.6.0 の挙動（本日リリース、既知の利用者 1 名）に依存していた場合は、
  全共有 symlink の 1 行で復元できる。
- 覚えるディレクトリが 1 つ増える。`/skills` の空状態出力が両方のパスと
  symlink のレシピを印字するので、知識は「欠けている瞬間」に配達される。
- ADR-0010 のセキュリティ上の論理 — 指示グレードの内容、封じ込めた読み取りで
  境界づけたラップ免除 — には触れない。本 ADR が動かすのは skill の
  **見つけ場所**であって、skill に**許されること**ではない。

## Alternatives considered

- **`~/.claude/skills` を追加スコープとして読み続ける** — 却下。それこそが
  混在であり、スコープが 1 つ増えただけである。opt-out の既定値は、
  フォールバックが主系の問題を静かに相続する経路である。
- **Claude Code のディレクトリを指す設定キー** — 却下。symlink が同じ仕事を
  スキーマ無しでこなし、設定の書き換えを生き延び、`ls` で見える。skill の
  発見を調べているオペレータが実際に見る場所は `ls` である。
- **導入時コピーのツール**（`gem-agent skills sync` コマンド）— 現時点では
  却下。コピーによる分岐は古いコピーを生む — まさにトランスクリプト/resume の
  設計が拒否した失敗様式である（ADR-0005）。symlink は単一の正を共有する。
  1 つの skill の**内容**を分岐させる実需要が現れたときだけ再検討する。

## References

- ADR-0010（skills。個人スコープの設置場所の項だけを supersede し、他は全て
  存続）
- skill をそこへ揃える対象になった MCP の 2 スコープ前例
  （`~/.config/gem-agent/mcp.json` + `<project>/.mcp.json`）
