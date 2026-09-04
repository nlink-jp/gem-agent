# ADR-0070: ロードされた skill は自分のディレクトリを名乗り、ルール層の「書ける場所」は sandbox のものにする

| 項目 | 値 |
|------|----|
| ステータス | **Accepted** |
| 日付 | 2026-09-04 |
| 対象 | gem-agent |
| 意思決定者 | nlink-jp メンテナ |
| きっかけ | セッション `20260904-225330`: モデルは `load_skill` で `incident-research` をロードし `references/` と `schema.json` を読んだあと、「skill のスクリプト配置場所を確認する」という目的で `find / -name "validate.py" 2>/dev/null \| grep incident-research` を実行した。ルール層は事実でない理由（`2>/dev/null` を書込可ルート外へのリダイレクトと読んだ）で Block し、オペレータが承認し、走査は 65 秒後に Ctrl+C で kill された |
| 修正対象 | ADR-0010 §2 / §4（skill ロードが開示するもの）; ADR-0004（ルール層の Safe 集合と「書ける場所」の定義） |

## 背景

その skill は Claude Code の契約に沿って書かれていた。`SKILL.md` は
「`SKILL_DIR` はこの SKILL.md を含むディレクトリ」と定め、スクリプトを
使う手順はすべて `python3 SKILL_DIR/scripts/validate.py …` である。
skills-series の 6 skill はすべて同じ一文を使う。Claude Code はこれを
満たしている: Skill ツールの結果は `Base directory for this skill: <path>`
の 1 行で始まり、transcript にはその絶対パスでスクリプトを起動した記録が
残っている。

ADR-0010 は形式をそのまま読み、progressive disclosure を選んだ: system
prompt には skill ごとに説明 1 行、本文は `load_skill(name)` か
`/skill <name>`、補助ファイルは `load_skill(name, file)`。その §4 は
`scripts/` を `shell_exec` で sandbox と承認ゲートの下に走らせると
言っている。だがこの設計のどこにも、skill が *どこにあるか* をモデルに
伝える箇所がない: prompt の行にも、ロード結果（`Skill "x" (global scope)
— the user's instructions …`）にも、`/skill` の展開文にも。
`load_skill(name, file)` はスクリプトを読めるが、実行はできない。
プロジェクト skill はプロジェクトルートから `.claude/skills/<name>/scripts/…`
が相対で解決できるため偶然動いていた。`~/.config/gem-agent/skills` 配下の
グローバル skill にはどんな相対パスも届かない。探索は、与えられなかった
事実を前にしたモデルの合理的な一手だった。

この探索が好奇心の類ではなく重大事だった理由:

- sandbox が拒むのは書き込みだけである。読み取りは設計上
  `(allow default)`（ADR-0001）なので、`find /` は全マウントを歩く —
  このセッションを走らせたマシンでは SMB 共有 5 つとリモート Time
  Machine 共有 2 つを含む — 上限は 120 秒の shell timeout だけだった。
- `find` と `grep` はルール層の read-only 集合にある。`2>/dev/null` が
  無ければこのコマンドは Safe だった: 自動承認モードではプロンプトなしで
  走る。オペレータに届いたプロンプトは偶然である: リダイレクト規則は
  `/dev` がプロファイル上で書けることを知らない。`buildExecFn` は
  `TMPDIR`・`/private/tmp`・`/dev` を許可し、ルール層はプロジェクトと
  作業ディレクトリを知っている。「シェルが書ける場所」の定義がパッケージ
  ごとに 2 つあり、オペレータには間違った方が見えた。

## 決定

### 1. ロードされた skill は Claude Code の言葉で自分のディレクトリを名乗る

`load_skill(name)` の結果と `/skill <name>` が注入するターンは、本文の前に

```
Base directory for this skill: <dir>
```

の行を持つ — Claude Code の一文をそのまま。skill ファイルはこの一文に
対して書かれており、最も安い互換は同じ一文だからだ。`<dir>` は skill が
発見されたシンボリックリンク解決済みディレクトリで、`Skill.Body` と
`Skill.File` が読み取りを閉じ込めるのと同じ境界（ADR-0010 §4）。
読み取りで既に届く範囲以外は何も新たに開示しない。ツール説明は、結果が
`shell_exec` でスクリプトを走らせるためのディレクトリを名乗ると言う。
system prompt の行は変えない: 場所は本文と一緒にあるべきもので、ロード
するかを決める索引に置くものではない（progressive disclosure は保つ）。

スクリプトの実行は引き続き `shell_exec` である: sandbox の下で、ゲートを
通り、他のコマンドと同じく分類される。この決定が足すのは事実であって
権限ではない。

### 2. 「書ける場所」の定義は 1 つ

`sandbox.ScratchDirs()` がプロファイルの許可する解決済み scratch ルート
（`TMPDIR`・`/private/tmp`・`/dev`）を返し、`buildExecFn` とルール層の
両方がそれを読む。scratch ルートへのリダイレクトは「プロジェクト外」では
ない。`/dev/null` はシンクであり、そこへのリダイレクトはコマンドを Safe
から外さない。オペレータに見せる理由は事実になり、リストが 1 つなので
Seatbelt の実際の挙動から乖離しようがない。

### 3. ルート外の走査は Safe ではなく Review

木を歩く read-only コマンド — `find`・`fd`・`du`・`rg`、および再帰
フラグ付きの `grep` — の起点が `/`・`~`・またはプロジェクト/作業/
scratch ルートの外の絶対パスであれば、「walks the filesystem outside the
project and session work directories」を理由に Review へ落ちる。
read-only は無害ではない: sandbox の読み取り側は意図的に開いているため、
走査のコストはプロジェクトではなくマウントで決まる。モデル層は狭い
走査（`find ~/.config/gem-agent/skills -name validate.py`）を承認できる
し、手動モードでは何も変わらない — すでに全部が尋ねる。

Block にはしない。Block は取り消せないものの床であり、走査は何も壊さない。
それはコストであり、コストを量るためにモデル層がある。

## 検討した代替案

- **system prompt の行に各 skill のパスを載せる** — 却下: ロードされない
  かもしれない skill の分までキャッシュされる接頭辞に N 本のパスが入り、
  しかもディレクトリは本文と一緒でなければ役に立たない。
- **`shell_exec` の環境に `SKILL_DIR` をエクスポートする** — 却下: 1
  セッションで複数の skill がロードされうるのでどれを指すのか決まらない。
  環境変数はプロセス全体に及び（ADR-0068 が環境経由で 1 消費者を操作する
  ことに立てた反対と同じ）、transcript に見えない。結果内の一文こそ skill
  作者が書いた対象そのものである。
- **skills-series 側を gem-agent のグローバルパスを綴る形に変える** —
  却下: skill を 1 ランタイムの配置に結合し、Claude Code 側の経路を壊す。
  drop-in 互換は gem-agent の義務（ADR-0010 §1）であって skill の義務では
  ない。
- **「skill を探索するな」とモデルに言う** — 却下: 否定形の指示は過剰
  一般化し、第 3 の経路を発明させる。原因は事実の欠落であり、修正は事実の
  供給である。
- **リダイレクト規則で `/dev/null` だけ特別扱いし、リストは 2 つのまま** —
  却下: 2 つのリストはまた乖離する。リダイレクト規則の仕事は sandbox が
  何を拒むかを説明することであり、だから sandbox のリストを読まねばならない。
- **`find /` を Block にする** — 却下、§3。
- **プロジェクト外の read-only コマンドをすべて Review にする** — 却下:
  `cat /etc/hosts` や `ls ~/Downloads` は単発の読み取りである。コストは
  場所ではなく走査にある。

## 帰結

- 自前のスクリプトを走らせる Claude Code 向け skill が、プロジェクトから
  だけでなく gem-agent のグローバルディレクトリからも動く。skills-series
  の 6 skill が最初の受益者で、skill 側は何も変わらない。
- transcript に skill のディレクトリが載る。グローバル skill なら
  オペレータのホーム配下のパスである — プロジェクトパスは既に全セッション
  記録に載っている。
- Safe なコマンドが `2>/dev/null` や `>/dev/null` を伴えるようになる。
  走れるものは何も広がらない: プロファイルは元からその書き込みを許して
  いた。分類が追いついただけである。
- 自動承認モードでは、ルート外の走査に以前は無かったモデル 1 往復の
  コストが掛かる。手動モードでは何も変わらない。
- `internal/risk` が関数 1 つのために `internal/sandbox` を import する。
  scratch ルートはパッケージ初期化時に 1 回解決され、`Classify` 自体は
  I/O を持たず、引数とその固定リストの純関数のままである。

## 参照

- ADR-0001（sandbox: 書き込みは拒否・読み取りは開放 — 走査がコストである理由）
- ADR-0004（自動承認の梯子; Safe 集合と Block 床）
- ADR-0010 §1 / §2 / §4（drop-in 形式、progressive disclosure、`shell_exec` でのスクリプト実行）
- ADR-0058（第 2 の書込可ルートとしてのセッション作業ディレクトリ）
- ADR-0068（環境はプロセス全体に及ぶ — ここで再利用した反対）
