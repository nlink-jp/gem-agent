# drop-in 統合: 指示ファイル・MCP・スキル

gem-agent の中核要件は drop-in 互換です: プロジェクトが他の
エージェントのために既に持っているファイルを、プロジェクト単位の
追加設定ゼロで読みます。プロジェクトの提供物はすべて 1 回きりの
信用ゲートの内側です（[承認 — 起動時の安全機構](approval.ja.md)
参照）。

## プロジェクト指示ファイル

リポジトリが既に持っている指示ファイルを、各ディレクトリで次の順に
読みます:

| ファイル | 慣例 |
|---|---|
| `AGENTS.md` | ベンダー横断の標準 |
| `AGENT.md` | その単数形 |
| `CLAUDE.md` | Claude Code |
| `GEMINI.md` | Gemini CLI |

収集順は `~/.config/gem-agent/`（全プロジェクト共通の自分用ルール）→
親ディレクトリを外側から → プロジェクト自身。これにより**ワーク
スペース共通ルールが配下の兄弟リポジトリすべてに効き**、最も近い
ファイルが最後に読まれる（＝最も具体的なものとして扱われる）形に
なります。内容が同一のファイルは 1 回だけ注入されます。読み込んだ
ファイルは起動バナーに一覧表示されます。

親ディレクトリの遡上は**ホームディレクトリで停止**します。指示
ファイルは「データ」ではなく「指示」として従う対象なので、`/tmp` の
ような自分の管理外の場所から拾わないためです。

プロジェクト自身のファイルは、プロジェクトを信頼した後（ADR-0023）、
かつ内容が信頼した時点と一致している間だけ読み込まれます: `git pull` で
`AGENTS.md` が変われば、使う前にもう一度確認します（ADR-0074）。信頼
プロンプト・ピン・`gem-agent trust` は [approval — 起動時の安全](approval.ja.md)
を参照。

## MCP サーバー

サーバー定義は 2 つのスコープから読み込みます。どちらも Claude Code
の `.mcp.json` 形式（stdio トランスポート、`${VAR}` と `${VAR:-default}` の展開対応）なので、
エントリをそのまま移動できます:

| スコープ | パス | 用途 |
|---|---|---|
| グローバル | `~/.config/gem-agent/mcp.json` | 全プロジェクトで使うサーバー |
| プロジェクト | `<project>/.mcp.json` | そのリポジトリ固有のサーバー |

どちらも任意です。両者はマージされ、**名前が衝突した場合は
プロジェクト側が優先**されます。`/mcp` で接続中のサーバーがスコープ
付きで一覧表示されます。

```json
{
  "mcpServers": {
    "tor-exit": { "command": "tor-exit-lookup", "args": ["mcp"] }
  }
}
```

ツールは `mcp__<server>__<tool>` として現れ、承認ゲート付きです
（ツール別に緩和可 — [承認](approval.ja.md)参照）。タイムアウトした
呼び出しはサーバー子プロセスを kill（MCP にキャンセルは無い）、次回
呼び出しで遅延再起動します。

**`/mcp reload`**（ADR-0039）はセッション途中で全体を再接続します —
完全再起動・設定再読込・新しいツールリスト — 会話は失いません。
変になったサーバーの回復手段であり、`mcp.json` に足したサーバーを
実行中セッションへ参加させる方法でもあります。trust 判定は起動時の
ものを再利用し内容のピンを再照合します（非信頼プロジェクトの `.mcp.json` は
ロードされないまま。信頼後に変わったものは外して名指しし、`gem-agent trust
--accept` か次の対話起動で再信用。trust 自体の付与・撤回には今も再起動）、
セッション承認 allowlist は
ツール名がキーなので生き残ります。コマンドラインの `--mcp off` は
1 回の実行だけ MCP を丸ごとスキップします — `-p` パイプラインが
通常求めるものです。

ガバナンスと監査証跡を付けたい場合は
[mcp-guardian](https://github.com/nlink-jp/mcp-guardian) を経由させ
ます — guardian 自体が stdio MCP サーバーなので、opt-in は
`.mcp.json` のエントリ 1 つで済みます:

```json
{
  "mcpServers": {
    "guarded": { "command": "mcp-guardian", "args": ["--profile", "myserver"] }
  }
}
```

### 大きな結果

ツール結果は応答 1 つの予算（ツール出力と同じ 200 KB 上限）の中でモデルに
渡されます。収まらないテキストブロックはセッション作業ディレクトリに丸ごと
保存され、先頭とパスに置き換わります（`… [N bytes — too large to hold inline,
so the whole result is saved. Read it, or narrow the call and ask again:
read_file <path>]`）。予算を超えたブロックはまとめて 1 ファイルに保存され
1 行で告げられます（`[N more text block(s), M bytes — past the response
budget, saved whole …]`）。画像などのバイナリブロックは保存して指し示す
（`use view_image on that path`）だけでインラインには入れません。予算を超えた
ものは保存も個別掲載もせず、件数を 1 行で示します。作業ディレクトリが無ければ
損失を明示します。

## スキル（ADR-0010・ADR-0011）

gem-agent は **Claude Code の skill 形式をそのまま読みます** — ただし
自前の設置場所から。MCP とまったく同じ配置です（形式互換は drop-in、
場所の共有は結合）:

| スコープ | パス | |
|---|---|---|
| グローバル | `~/.config/gem-agent/skills/<name>/SKILL.md` | gem-agent 自身の置き場 |
| プロジェクト | `<project>/.claude/skills/<name>/SKILL.md` | Claude Code と共有 |

`~/.claude/` は読みません — あれは Claude Code の生きた環境であり、
暗黙に相続すると主系の環境が変わるたびに副系の挙動が変わります。
**共有は自分で張る symlink** で、skill 単位でも丸ごとでも可能です
（探索はリンクを辿ります）:

```sh
ln -s ~/.claude/skills/meeting-notes ~/.config/gem-agent/skills/meeting-notes
ln -s ~/.claude/skills ~/.config/gem-agent/skills   # 全部共有
```

frontmatter は最小限（`name` / `description` / `argument-hint`）だけ
読み、`allowed-tools` は無視します — gem-agent には自前の承認モデルが
あり、他所の権限付与を黙って尊重するとそれをバイパスするからです。
名前衝突は MCP と同じくプロジェクト側が勝ち、そう告げます。

skill は progressive disclosure です: 各 skill はシステムプロンプトに
説明 1 行を載せるだけで、本文は使うときに読み込まれます —

- **モデル**がタスクと説明の合致を見て `load_skill(name)` を呼ぶ。
  skill 自身の `references/`・`scripts/` は `load_skill(name, file)`
  で読める
- **利用者**は `/skill <name> [args]` で直接起動（本文はターンに直接
  注入 — 余分なモデル往復なし。名前は Tab 補完）。`/skills` が一覧

**`/skills reload`**（ADR-0039）はセッション途中で探索をやり直します
— セッション実行中にインストールした skill が再起動なしで使える
ようになり、システムプロンプトの skill 節も追随するので、モデルは
次ラウンドから新しいセットを見ます。

skill の内容は非信頼データとしてラップせず、**指示として**扱います —
利用者自身が導入したファイルであり、`AGENTS.md` と同じ信頼階層だから
です。この例外には境界があります: `load_skill` は発見済み skill の
ディレクトリ内しか読めません（シンボリックリンクも解決して検査）。
`scripts/` を `shell_exec` で走らせる場合も sandbox と承認ゲートは
他と同様に掛かります。

ロードされた skill は自分のディレクトリを名乗ります（ADR-0070）:
`load_skill(name)` の結果と `/skill <name>` のターンは Claude Code と
同じ行 `Base directory for this skill: <dir>` で始まります — シンボリック
リンク解決済みの skill ディレクトリで、読み取りが閉じ込められる境界と
同じものです。Claude Code の契約（「`SKILL_DIR` はこの SKILL.md を含む
ディレクトリ」、続いて `python3 SKILL_DIR/scripts/…`）で書かれた
`SKILL.md` は、プロジェクトからだけでなくグローバル skill ディレクトリ
からも辿れます。この行が無いとグローバル skill のスクリプトにはモデルの
知るどのパスも届かず、モデルは探しに行きます。
