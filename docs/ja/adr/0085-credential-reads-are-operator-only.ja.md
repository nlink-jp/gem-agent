# ADR-0085: 資格情報のパスは read ツールでも操作者専用

| Field | Value |
|-------|-------|
| Status | **Accepted**（2026-09-13） — 実装済み・未リリース |
| Date | 2026-09-13 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | システムリスクレビュー 2026-09-13（gem-agent v0.77.3 / lagent v0.3.3）10 章と所見 R02: プロジェクト内の `read_file .env` は Safe で、その内容はモデルへ流れ transcript に残る。資格情報一覧を強制するのはレーン・write ツール・シェルの床であって read ツールではなく、「write ツールは守られている」を「read ツールも」と読み替えてはならない |
| Amends | ADR-0073 §3（一覧は 1 つ、強制者は 3 つ — いまは 4 つ） |
| Relates to | ADR-0072 §1.4 / §4.5（OperatorOnly は Block と同じ床）、ADR-0070 §3（Block は不可逆なものの床のまま）、ADR-0052（すべての skip は報告する）、ADR-0037（ファイル検索の子のゲートは拒否する）、ADR-0076（有限の一覧が機構のまま） |

## Context

`sandbox.CredentialFilters` / `sandbox.CredentialPath` は資格情報の所在の
唯一の一覧である（ADR-0073 §3）: 操作者のキーとトークンを持つホーム配下の
ディレクトリとファイル、そしてどこにあっても秘密である名前 — `.env` と
その変種（コミットされるテンプレート `.env.example` / `.sample` /
`.template` / `.dist` は再許可）、`id_rsa` 類、`credentials.json`、
`*service-account*.json`、`application_default_credentials.json`。強制者は
3 者: Seatbelt の read / write レーンはその配下の `file-read*` を拒み、read
レーンの `cat .env` はカーネルで失敗する。`risk.Classify` は一致するパスへの
`write_file` / `edit_file` を Block にする。それを名指すシェルコマンドは
全レーンで Block の床に当たる。

read ツール — `read_file`・`view_image`・`read_document`・`file_info`・
`summarize_file`、および walk の `search_files`・`list_tree`・`list_files` —
は一覧を参照しない。非変更系であり、`Tool.Mutating` は false、`risk.Classify`
はパスを見る前に Safe を返し、`gated()` は全モードで素通しする。よって `.env`
のあるプロジェクトでの `read_file .env` は、既定のゲートでも `--auto` でも
`"never"` ポリシーでも `-p` でも確認なしに走り、ファイルの内容はセッションの
残り全体でモデルの文脈に、そしてディスク上の transcript に残る。レビューは
それを平明に述べる（10 章）: 直接読取は根境界を検査し資格情報名は検査しない。
読んだ値は推論先と履歴へ流れる。書込の保護を読取の保護へ読み替えてはならない。
R02 は高と評価する。

この非対称は、ランタイムが既に知っている形をしている。シェルでは資格情報を
読めるのは operator レーンだけ — レーンが読めるからこそ、操作者が毎回承認する
レーンである（ADR-0073 §1）。file ツールにレーンは無い。「operator レーン」に
相当するのは操作者自身の答えである。2 つの判定を検討し、退けた:

- **Block。** Block は不可逆なものの床であり（ADR-0070 §3）、操作者の yes を
  得て秘密を読むのは正当な仕事である — 鍵のローテーション、環境ファイルの
  デバッグ、サービスアカウントファイルが何を許すかの確認。Block にすれば
  その仕事はシェルの operator レーンへ行くが、ファイルを読む場所として
  それは file ツールより悪い（ADR-0072 §4: `os.Root`、上限付き、実行なし）。
- **モデル層。** 読取を提案した当事者は、その内容が自分に届くべきかを
  裁けない: ADR-0020 §4 の「評価者が提案者」という反対理由を ADR-0072 §4.5
  は後続セッションが信頼するファイルに適用したが、操作者の秘密を持つ
  ファイルにもまったく同じく当てはまる。

## Decision

### 1. 資格情報パスへの read ツールは Review・操作者専用

`risk.Classify` は名指しの read ツール 5 つを非変更系のショートカットより先に
判定する: `path` — `file_info` では `paths` のいずれかの要素 — が資格情報の
規則に一致すれば、判定は `OperatorOnly` 付きの `Review`。規則は write ツール
自身のもの: `sandbox.CredentialPath` の上の `hasCredentialPath`、`.env.example`
/ `.sample` / `.template` / `.dist` の再許可を含み、名前は一覧の他と同じく
ケースを畳む。第 2 の一覧は存在しない。read ツールは `internal/sandbox` の
一覧を読む。

判定は実パス上で行う。`Agent.decide` は read ツールについても `write_file` /
`edit_file` と同じく（ADR-0072 最終レビュー R2）`path` と `paths` を
`Registry.RealPath` で解決する: `.env` を指す `notes.txt` という名のリンクは
`.env` の読取である。解決できない綴り — 根の外、壊れたリンク — は綴りの
まま判定するので、write ツールと同様、いずれにせよ試行は操作者の前に出る。

`OperatorOnly` がモードごとに既に意味するものが、ゲートに何も足さずにこれらの
読取にも成り立つ — `gated()` は `Mutating || Floor()` を通し、`decideAuto` は
`OperatorOnly` でモデル層の前に返る（ADR-0072 §4.5、§4.9）。各行はテストが
固定する:

| モード | 資格情報の読取 |
|---|---|
| 既定のゲート | 確認する。`read_file` に対する以前の `a` は答えない |
| `--auto` | 確認する。モデル層は一切参照されない |
| `"never"` ポリシー・`--allow read_file` | 確認する。床は持ち上がらない |
| `-p` | 拒否。理由は stderr へ |
| ファイル検索の子（ADR-0037） | そのゲートが拒否する。子は拒否を報告し、操作者は見えない会話について尋ねられない |

`search_files`・`list_tree`・`list_files` の walk は判定対象のツール一覧に
入れない: walk は多数のファイルにまたがる 1 コールであり、確認はしない
（下記）。`@` 添付は変えない: `@.env` は操作者が打ったものであり、それが
操作者の yes である。

### 2. walk は資格情報名のエントリを飛ばし、その旨を告げる

`search_files` は資格情報名のファイルを決して読まず、資格情報名の
ディレクトリへ決して降りない。`list_tree` と `list_files` はそれらを列挙
しない。各ツールは差し止めたものを件数で報告する — `[N credential-named
entries skipped — reading one needs the operator's approval]` — ADR-0052 が
すべての skip に与えた形であり、ignore の集計や上限と並ぶ。

確認ではなく skip: 一覧の中でファイルごとに確認するのは誰にも下せない
判断であり、立ち止まって尋ねる walk はモデルが走らせなくなる walk である。
内容だけでなく名前も差し止める: walk はモデルの方向づけであり、`config.yaml`
の隣に `.env` を名指す一覧は、確認を要する読取を毎ラウンド差し出すことに
なる。件数行があればモデルは何かが差し止められたことと理由を知り、読ませ
たい操作者が名指しする。規則は同じ `sandbox.CredentialPath` をエントリの
実パスに当てる — プロジェクトの `.claude/`（スキル）は `~/.claude`
（トークン）ではなく、プロファイルが既に区別しているとおり。

### 3. 一覧は 1 つ、強制者は 4 つ

どの一覧にも何も足さない。read ツールの判定と walk の skip は
`sandbox.CredentialPath` を読む。ADR-0073 §3 の「強制者は 3 つ」— プロファイル
ビルダー・write ツールの判定・シェルの Block 床 — は 4 つになり、同節の修正が
それを述べる。`internal/sandbox` に加えた資格情報の所在は、1 回の編集で、
無確認レーンに拒まれ、write ツールで Block、シェルで Block 床、read ツールで
操作者専用、walk から差し止めになる。

## Consequences

- **操作者のいるモードでは資格情報の読取ごとに確認 1 回、いないモードでは
  拒否。** `AGENTS.md` と同じく、評価者が提案者でないことの代価である。
  `.env` を繰り返し読む必要のあるセッションはそのたびに確認を払う — `a` は
  設計上定着せず、`"never"` も持ち上げない。
- **変わらないもの。** コミットされるテンプレートは従来どおり読める。write
  側（Block）は不変。シェルのレーンは不変。`@` 添付は不変。モデル層はどちらの
  側でも資格情報パスを決して見ない。
- **対象外。** 一覧が名指さないファイルの中の秘密 — `~/.config/<tool>/config.toml`
  のトークン、`notes.md` の中のパスワード — は従来どおり読める。一覧は有限で
  名指しであり、DLP ではない（ADR-0076 §1。レビューも 10 章でそう述べる）。
  そのクラスへの対処はレビューのもの: エージェントが読む作業コピーに実秘密を
  置かない。
- **無人実行の拒否文は「read-only の file ツール」を承認なしで走るものとして
  名指したまま。** この 1 例を除く全読取について真であり、`[denied: …]` 行は
  判定の理由を運ぶので、one-shot 実行のログはこの例外がなぜ拒まれたかを言う。
- **テスト。** ルール層のコーパス（各 read ツールの `.env`・`.env.local`・
  `id_rsa`・`credentials.json`・ホーム基準の `~/.ssh/id_rsa`・資格情報パスを
  1 つ含む `file_info` のバッチ・ケースの畳み込み。`.env.example`・`.envrc`・
  通常ファイルは Safe のまま。walk はどのパスでも Safe のまま）。エージェントの
  ゲート（`read_file .env` は must-prompt としてゲートに届き、`read_file
  notes.txt` はゲートを通らない。`.env` への symlink は指す先で判定される。
  `"never"` ポリシーでも確認は残る。`--auto` はモデル層を参照せず確認する。
  無人実行は拒否し、内容は履歴に入らない）。walk（`.env` と `.ssh/`
  ディレクトリは件数付きで差し止め、`.env.example` は検索・列挙される）。
