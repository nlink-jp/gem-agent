# ADR-0073: shell_exec のケイパビリティレーンと、再発クラスを閉じるアーキテクチャテスト

| 項目 | 内容 |
|-------|-------|
| ステータス | **採択** |
| 日付 | 2026-09-05 |
| 拘束対象 | gem-agent |
| 決定者 | nlink-jp maintainers |
| 契機 | v0.67.0 以後のレビュー 9 パス（ADR-0072 §4）で所見 103 件。利用者が修正を対症的と判断 —「局所ケースにフォーカスしすぎ」— し、組織のコントリビューション規則どおり根本原因を特定して根源で直すよう求めた |
| 修正する ADR | ADR-0004（ルール層はシェルコマンドの Safe を決めない）、ADR-0001（read レーンのシェルコマンドは非変更系）、ADR-0070 §2（共有一覧を 3 つに一般化） |

## 背景 — 9 パスが実際に見つけたもの

ADR-0072 に記録した 103 件は 7 クラスに分かれる。うち 3 クラスで 48 件、
再発 16 件のうち 13 件を占め、その 3 つはいずれも局所パッチでは閉じない
単一の根本原因を持つ:

| クラス | 件数 | 再発 | 根本原因 |
|---|---|---|---|
| A. ルール層がコマンド文字列からシェルの意味を再導出 | 20 | 2 | *Safe* を文字列から導出していた — 綴り・引用・ラッパー・フラグ・サブコマンド・引数内スクリプトという無限の領域 |
| B. 字面パスを検査してから使う | 8 | 4 | 封じ込め API が *ハンドル* ではなく *パス*（名前）を返し、全消費者が名前を開き直した |
| C. 上限なし／無言の打ち切り I/O | 20 | 7 | 各所が独自に読んでいた。`more` を返さない上限は新しいバグ |
| D. 回転する状態を消費者がコピー保持 | 12 | 1 | v0.68.x で構造的に解消（`rotateWorkDir`・消費者一覧・それを歩くテスト） |
| E. 承認判定が複数箇所 | 4 | 0 | 3 箇所（`mustPrompt`・`decideAuto`・`gated`）が床を別々に実装し、それぞれ 1 つずつ欠いた |
| F. 形式パーサがファイルの保証しない構造を仮定 | 5 | 2 | 本当に局所 |
| G. その他（14 小分類） | 34 | 0 | 本当に局所 |

クラス A は決して収束しなかった。`internal/risk` は 1,100 行、うち約 700 行
（`classifyCommand` の Safe 導出・`readOnlyCommands`・`mutatingUse`・
`sedExecutes` とその補助・`persistentTokens`・`candidateSplit`・`shellUnquote`・
`gitReadOnlySegment`・`shellWords`・`redirectTarget`・`walksOutsideRoots`）が
bash と起動されるプログラムの振る舞いを推測するためにあった。パスごとに次の
綴りが見つかった。レビュワーが誤っていたのではなく、設計がそれを求めていた。

答えはカーネルが知っている。Seatbelt は `exec` で実バイナリを、`open` で
実パスを、`connect` でソケットを、`lookup` で Mach サービスを見る。決定前に
この機体（macOS 26・`sandbox-exec`）で実測:

| 副作用 | SBPL 規則 | 結果 |
|---|---|---|
| 許可ディレクトリ外への書込 | `(deny file-write*)` + subpath 許可 | 拒否（ADR-0001 のプロファイル） |
| `echo > AGENTS.md`・`mv tmp AGENTS.md`・`gsed -i … AGENTS.md`・`rm AGENTS.md` | 許可の後に `(deny file-write* (regex …AGENTS\.md$))` | **全て拒否**、ファイル無傷 |
| `git config user.name`（`config.lock` に書いて `config` へ rename） | `.git/config` と `config.lock` の deny | 拒否、config 無傷 |
| `echo x > .git/hooks/pre-commit` | `(deny file-write* (regex …\.git/(hooks\|info)…))` | 拒否 |
| `cat ~/.ssh/id_rsa`・`cat ~/.s*/id_rsa`・`cat .env` | `(deny file-read* (subpath ~/.ssh) (regex /\.env…))` | 拒否 — glob は助けにならない |
| `curl https://…` | `(deny network*)` | 拒否（DNS 含む） |
| `defaults write` | `(deny user-preference-write)` | 拒否 — **かつ `(deny file-write*)` だけでは止まらない**: 設定は `cfprefsd` が書くので ADR-0001 のプロファイルにはこの穴があり、正規表現層だけが覆っていた |
| `kill <他 pid>`・`pkill` | `(deny signal)(allow signal (target self) (target children))` | 拒否。自分の子と `wait` は動く |
| `sysctl -w` | `(deny sysctl-write)` | 拒否 |
| `pbcopy`・キーチェーン書込 | `(deny mach-lookup (global-name …))` | 拒否 |
| `osascript`・`open -a` | `(deny process-exec (literal /usr/bin/osascript) …)` | exec で拒否。Apple Events はこの macOS では `appleevent-send` も `mach-lookup` 全拒否も止められず、バイナリ一覧が効く規則。カーネルは実バイナリを見るので `/usr/bin/osascript`・`\osascript`・`OSASCRIPT` は同一 |
| `GOCACHE` を `/private/tmp` に置いた `go vet` | read レーン | 実行できる |

## 決定

### 1. shell_exec はカーネルが強制する 3 レーンのどれかで走る

| レーン | プロファイル | 決める者 | いつ |
|---|---|---|---|
| **read** | プロジェクトと作業ディレクトリへの書込なし（scratch とデバイス sink は残る）。`(deny network*)`、`(deny user-preference-write)`、`(deny sysctl-write)`、自分と子以外への `(deny signal)`、`(deny lsopen)`、ペーストボードとキーチェーンサービスへの `(deny mach-lookup)`、IPC 系プログラムの `(deny process-exec)`（`sandbox.DefaultDenyExec`: osascript・open・launchctl・defaults・security・pbcopy・shortcuts・automator・scutil・networksetup・systemsetup。`[sandbox].read_lane_deny_exec` で追加）、資格情報一覧の `(deny file-read*)` | 誰も — 檻が決定。コールは**構成上非変更系**で `read_file` と同格、どのモードでも確認なしで走る | 既定レーン |
| **write** | ADR-0001 のプロファイル（プロジェクト・作業ディレクトリ・scratch）**に加えて**永続ファイル（`.git/hooks`・`.git/info`・`.git/config`・`AGENTS.md`・`CLAUDE.md`・`AGENT.md`・`GEMINI.md`・`.mcp.json`・`.gem-agent.toml`・`.claude/`、プロジェクト配下の任意の深さ）への `(deny file-write*)`。資格情報の読取は引き続き拒否 | 従来どおり ADR-0004 のラダー（Block 床 → モデル層 → 人間）、既定モードでは人間 | モデルが `access: "write"` を宣言 |
| **operator** | ADR-0001 のプロファイルそのまま: 永続ファイル書込可、資格情報読取可 | 操作者のみ — モデル層・セッションの `a`・`--allow` が決して持ち上げない OperatorOnly 床 | モデルが `access: "operator"` を宣言。操作者が打った `!command` 経路はここで走る |

ツールは引数 `access` を 1 つ得る（欠落は `read`）。これは**ゲート入力ではない**:
`read` の宣言は檻を狭めるだけ、`write`・`operator` の宣言は審査を増やすだけ。
偽の宣言は何も得ない — ADR-0047 が宣言に求める性質（表示するが信頼しない）
そのもの。read レーンが拒んだコマンドは exit status と、求めるべき広い
レーンを名指しする 1 行を持って返る。欠落は罰しない — 最も狭い檻で走る。

read レーンが既定モードで無確認に走るのは、書かれた ADR-0001 の契約
（「全 `shell_exec` が確認」）の緩和であり、利用者の明示的な選択: カーネルは
確認ダイアログより強い保証で、`ls` の確認こそがセッションを auto モードへ
押しやった摩擦だった。

### 2. ルール層はシェルについて何も決めない。昇格だけができる

`internal/risk` がシェルコマンドに残すのは **Block 床**のみ — `sudo`・`rm -rf`・
`git push`・`curl | sh`・fork bomb・資格情報パス・`osascript … with
administrator privileges`、全レーンで。パターンは助言的で寛容。見逃した綴りの
コストは、檻がいずれ捕まえる*確認の欠落*であって*檻の欠落*ではない。
引数が構造化パスである file ツールについては層は正確なまま: パスの位置と
ファイルの種類が判定を決める（ADR-0072 §1.4 のとおり）。

テストごと削除: コマンド文字列からの Safe 導出とそれに仕える全補助 — 約 700
行。`sandbox.Available()` が失敗する場合（`--no-sandbox`・入れ子 Seatbelt）は
read レーンが無い: 全シェルコールは write レーンのコールとして確認になり、
バナーがそう告げる。

### 3. 一覧は 1 つ、強制者は 3 つ

`sandbox.ScratchDirs()` は既にプロファイルとルール層が共有する唯一の一覧だった
（ADR-0070 §2）。同型の一覧を 2 つ `internal/sandbox` に置き、プロファイル
ビルダーと file ツールの判定の両方が読む: `PersistentFiles` / `PersistentFile`
（後続セッションが信頼するもの）と `CredentialFilters` / `CredentialPath`
（operator レーン以外が読めないもの）。カーネルが拒むものとツールが拒むものの
不一致は、レビューではなく構成上あり得ない。

### 4. アーキテクチャテストがクラス B・C・E を閉じる

`internal/archtest` が非テスト全ファイルの AST を歩き、次で失敗する:

- **B** — モデル・プロジェクト由来のパスを受けるパッケージ（`tools`・`mention`・
  `skills`・`instructions`・`ignore`・`mediastore`・`docext`・`hooks`）で、理由付き
  許可リスト外の `os.Open/OpenFile/ReadFile/ReadDir/Stat/Lstat/Create/WriteFile/
  Readlink/Remove/Rename`（許可: 封じ込め検査の祖先探索、操作者自身の絶対参照、
  skills ルート自体、ルート無し既定リーダー）。使われなくなった許可も失敗。
- **C** — `internal` と `cmd` の全域で、`internal/bounded` の外の `io.ReadAll`・
  `os.ReadFile`・`os.ReadDir`・`bufio.NewScanner`・`.CombinedOutput()`・
  `.Output()`。`bounded` はバイト列と共に `more` の事実を返す唯一の原始関数
  パッケージ。従来のコピー（tools・mention・skills・ignore・instructions・
  docext・config・mcp・memory・riskbook・statedir・telemetry・クリップボード取込・
  パイプ stdin・セッション一覧）は全てこれを呼び、セッション一覧と riskbook
  走査は飲み込んでいた打ち切りを報告する。
- **E** — `risk.Classify` を呼ぶのはただ 1 関数 `Agent.decide`。その `Decision`
  （変更系か・判定・床）を `gated`・allowlist 床・auto ラダーが読む。

## 影響

- クラス A はクラスとして終わる。新しいシェル綴りは穴を開けられず、最悪でも
  Block 床の確認を 1 つ逃すだけ。
- 正規表現層が覆っていた穴 2 つが本当に閉じる: `defaults write`（ファイル
  書込ではない）と、正規表現に無い綴りで届く全 IPC 系プログラム。
- ネットワークやキャッシュディレクトリを要するコマンドは read レーンで 1 度
  失敗し、write レーンで再発行される — 初回に往復 1 回。モデルにはどのレーンか
  伝える。こうしたコマンドは以前も Safe ではなかった（`curl`・`go test` は
  read-only 一覧に無い）ので、消える確認は利用者が選んだ read レーンの分だけ。
- `access` 引数は承認ボックスとトランスクリプトに見える。
- `TestLaneEnforcement` は実 `sandbox-exec` で 3 プロファイルを上記プローブの
  17 綴りに当てる。ADR-0001 の強制テストと同じく、荷重を受けるテスト。

## 教訓

- **判定を無限の領域から有限の領域へ移す。** コマンド文字列は無限、カーネル
  操作は短い文書化された一覧。レビュワーが綴りを見つけ続けるなら、修正は
  次の綴りではない。
- **宣言は檻を選べるが許可は選べない。** レーン引数を信頼して安全なのは、
  誤信のコストが操作者ではなく宣言者に落ちるから。
- **2 箇所で強制する規則は 1 つの一覧を 2 回読む。** ADR-0070 §2 の scratch
  一覧が型で、永続・資格情報の一覧がそれに従う。
- **クラスはクラスを名指しするテストで閉じる。** 4 度目のインスタンス修正
  ではなく。`internal/archtest` が B・C・E のそれ。
