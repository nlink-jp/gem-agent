# ADR-0107: 同期フォルダは書き込みのルートにできる

| 項目 | 値 |
|-------|-----|
| Status | **Proposed**（2026-11-06）— 独立した設計レビューを反映済み（§4・§5・Consequences の残余）。§5 の測定待ち |
| Date | 2026-11-06 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | 運用者: 書き込むディレクトリとしてセッションに足した OneDrive のフォルダに書き込めなかった。macOS では File Provider のクライアントのフォルダと iCloud Drive が `~/Library` の下にあり、ADR-0099 §5 はそこにある書き込みのルートを断る |
| Amends | ADR-0099 §5（書き込みのルートに対する `~/Library` の拒否） |
| Relates to | ADR-0099 §1・§7・§10（書き込みのルートとは何か・プローブ・永続ファイル）、ADR-0101（`pathid`）、ADR-0102（プロジェクトはどこにあっても読める） |

## Context

ADR-0099 §5 は、`~/Library` の中にある書き込みのルートを断る。
`LaunchAgents` や `Application Support` などには、他のプログラムが
サンドボックスの外で実行するファイルがあるためである。規則は 1 つの木として
書かれている。

macOS は、運用者が同期している文書をその木の中に置く:

- Google Drive、Dropbox、OneDrive をはじめ File Provider を使うすべての
  プロバイダは、フォルダを `~/Library/CloudStorage/<プロバイダ>-<アカウント>/`
  に置く。プロバイダや運用者が作るリンク（`~/OneDrive`、`~/Google Drive`）は
  そこへ解決され、§5 は実パスで判定する。
- iCloud Drive は `~/Library/Mobile Documents/com~apple~CloudDocs/` である。

そのため、どう名指しても、これらの同期フォルダは書き込みのルートにできない
（フォルダをホームの直下、`~/Library` の外に置く同期クライアントは、もともと
断られていない）。
ADR-0102 は、これらの場所にあるプロジェクトを read レーンで読めるようにしたが、
§5 には触れておらず、その後も誰も見直していなかった。結果は筋が通らない:
Google Drive のフォルダをプロジェクトとして開けばファイルツールと write レーンが
書き込むのに、同じフォルダを書き込みのルートとして足すと断られる。

このマシンで測った（2026-11-06）:

| 対象 | 結果 |
|---|---|
| Google Drive（`~/Library/CloudStorage/GoogleDrive-…/共有ドライブ/…`）にあるプロジェクト、2026-11-05 のセッション | `write_file` 16 回と `edit_file` 22 回が「プロジェクト内の編集」として自動承認された。write レーンの `shell_exec` がプロジェクトで `mkdir -p` と `mv` を実行した。トランスクリプトに `Operation not permitted` は無い |
| `~/Library/Mobile Documents/x` の下の書き込みのルートに対する `RefuseRoot` | 断られる — 現在の `TestRefuseRoot` がこれを確かめている |
| `~/Library/CloudStorage/` | プロバイダのアカウントごとに 1 フォルダ（`Dropbox`、`GoogleDrive-<アカウント>`）と `.DS_Store` |
| `~/Library/Mobile Documents/` | 168 項目: `com~apple~CloudDocs`（iCloud Drive）と、アプリの iCloud コンテナ。その中に `com~apple~Automator`、`com~apple~ScriptEditor2`、`iCloud~com~apple~shortcuts~runtime`、`iCloud~is~workflow~my~workflows`、`iCloud~md~obsidian` がある |

1 行目は、カーネルとプロバイダが、サンドボックス内のプロセスに File Provider の
フォルダへの書き込みを許すことを示す。write レーンの許可は、プロジェクトにも
書き込みのルートにも同じ `(subpath …)` の規則である。最後の行は、`Mobile Documents`
の大半には §5 の理由が当てはまることを示す: アプリのコンテナには、そのアプリが
実行するもの — Automator のワークフロー、スクリプト、ショートカット、Obsidian の
保管庫のプラグイン — が入っている。

## Decision

### 1. 同期の場所 2 つを `~/Library` の拒否から外す

書き込みのルートが `~/Library` の中にあってよいのは、実パスが次のどちらかの
ときだけである:

- **`~/Library/CloudStorage/` の厳密に内側** — プロバイダのアカウントのフォルダ、
  またはその下のどこか;
- **`~/Library/Mobile Documents/com~apple~CloudDocs/`** またはその下のどこか —
  iCloud Drive そのもの。

`~/Library` の下のそれ以外は、§5 のとおり書き込みには断ったままで、断るときは
その理由を示す。特に:

- `~/Library/CloudStorage/` そのもの — すべてのプロバイダのすべてのアカウントと、
  運用者が後から足すプロバイダを覆う;
- `~/Library/Mobile Documents/` そのものと、その中のアプリのコンテナすべて。

読み取りのルートには影響しない: §5 は `~/Library` の下の読み取りのルートを
もともと断っていない。

### 2. カーネルの名前で、パスの区切りごとに判定する

例外は §5 と同じ場所で、`CanonicalDir` が返す名前で判定する。打った大文字小文字の
違い（`~/library/cloudstorage/…`）やリンク（`~/OneDrive`）は、それが指すフォルダ
として判定される。包含は、解決済みのホームから作った 2 つの起点に対する
`pathid.Within`（ADR-0101）で判定する。したがって `~/Library/CloudStorageX/…` や
`~/Library/Mobile Documents/com~apple~CloudDocsX/…` はその内側ではない。

### 3. 書き込みのルートについて、ほかは何も変えない

同期フォルダの書き込みのルートは、ADR-0099 が定める書き込みのルートである:

- write と operator のレーンが書き込み、read レーンが読む — read レーンは
  到達範囲のために `~/Library` を拒否しているが、それでも読む（§1）;
- その中でも資格情報のブロックが効く（§7）;
- 永続ファイルの名前（`AGENTS.md`、`.git/hooks` など）は write レーンに拒否され、
  セッションをまたいで見張られる（§10）;
- 起動時と付与時に、ルートごとのプローブがその中で走る（§7）;
- モデルは `request_directory` で要求でき、運用者は他のディレクトリと同じく
  答える（§4）。

### 4. 自分を消せなかったプローブはそう言う

ルートごとのプローブは、プローブ用のディレクトリを消し、失敗は無視する
（`verifyAddedRoot`）。同期フォルダでは、消し残しは手元にとどまらない:
運用者の役割が「作れるが消せない」共有ドライブでは、`.env` と `plain` を持つ
`.gem-agent-root-probe-*` ディレクトリが残り、ドライブを共有している全員へ
同期され、しかも何も報告されない。プローブは、消せなかったプローブのファイルや
ディレクトリをパスで、起動時または付与時の注記に出す。プロジェクトのプローブ
（`VerifyWriteLane`）も同じやり方でファイルを消しているので、同じ報告を出す。

### 5. 採択前に測る

前提を測ったのは、Google Drive にあるプロジェクトだけである。この ADR を
採択する前に、運用者が名指した Dropbox・Google Drive・iCloud Drive のフォルダを
書き込みのルートとして足し、write レーンがそこでファイルを書き、名前を変え、
消せることを確かめる。OneDrive はこのマシンに無い。同じ File Provider の
経路なので別には測らない。結果はここに記録する。

### 6. テストと文書

- `TestRefuseRoot` に次の行を足す: プロバイダのアカウントのフォルダの中の
  フォルダと、アカウントのフォルダそのもの（書き込みで受け入れる）;
  `~/Library/CloudStorage` そのもの（断る）; iCloud Drive の中のフォルダと
  iCloud Drive そのもの（受け入れる）; `Mobile Documents` そのものとアプリの
  コンテナ（断る）; 2 つの接頭辞の隣人（断る）; 大文字小文字の違いと、同期
  フォルダへのリンク（実パスで判定）; `LaunchAgents`（引き続き断る）。
  例外の実体はこれらの行だけである: レーンのプロファイルは書き込みのルートに
  `~/Library` の規則を持たないので、偽の `Library/CloudStorage` の下での
  サンドボックスのテストは、この ADR の有無にかかわらず通る。だから足さない。
- 消し残しの報告（§4）に、プローブ用のディレクトリを消せない場合のテストを
  足す。
- ADR-0099 の見出しの下に「Amended by ADR-0107」の注記を入れる。
  `config.example.toml`、`configuration.md`、`approval.md`（両言語）で、
  `~/Library` の拒否を書いている箇所に例外を書く。AGENTS.md と CHANGELOG.md を
  更新する。

## Consequences

- Google Drive、Dropbox、OneDrive、iCloud Drive、その他 File Provider の
  フォルダを、`--add-dir`、`[directories].write`、`/add-dir`、付与した要求で
  足せる。
- モデルがそこに書いたものはプロバイダが同期する: 運用者の他の機器に届き、
  共有ドライブや共有フォルダなら共有している他の人にも届く。それはフォルダを
  名指した運用者の選択であり、クラウドストレージにあるプロジェクトですでに
  そうなっているのと同じである。
- **受け入れる残余: 同期フォルダにも、サンドボックスの外で実行されるものが
  入りうる。** §5 の理由は `~/Library` だけのものではない。Google Drive にある
  Obsidian の保管庫はプラグインを持ち、Dropbox へのリンクにした `~/bin`
  （運用者が使ってきた配置）は、同期フォルダの実行ファイルを `PATH` に載せる。
  それらを含むアカウントのフォルダを足せば、それらを write レーンに渡すことになり、
  運用者が承認するパスは、運用者が知っているリンクではなくプロバイダのパスである。
  これは受け入れる: フォルダを名指すのは運用者であり、`~/Library` の外の書き込みの
  ルートと同じである（ADR-0099 は同じ理由で `~/bin` を受け入れた）。足すフォルダが
  狭いほど、含むものは少ない。この拒否が守るのは macOS とアプリが実行する場所で
  あって、何かが実行されうるすべての場所ではない。
- 起動時のプローブは、書き込みのルートごとにプローブ用のディレクトリを作って
  消す（ADR-0099 §7）。クラウドストレージにあるプロジェクトで、プロジェクトの
  プローブがすでにそうしているのと同じである。その間にプロバイダがアップロード
  するかはプロバイダの挙動で、測っていない。中身は運用者のデータではなく、
  消し残しは報告する（§4）。
- プロバイダが読み取り専用にしているフォルダ（Google Drive のアカウントの
  フォルダそのものは `r-x`）は、この規則ではなくプローブが、プローブの
  「cannot create a probe」で断る。
- アプリの iCloud コンテナ（たとえば iCloud にある Obsidian の保管庫）は、
  引き続き書き込みのルートにできない。プロジェクトとして開くことは今もできる。
  追加のルートとして書き込む必要が出たら、それは新しい判断であり、ここでの例外を
  広げることではない。

## Alternatives considered

- **書き込みのルートに対する `~/Library` の拒否をやめる。** 却下: `LaunchAgents`、
  `Application Support`、`Mobile Documents` の中のアプリのコンテナについて、
  §5 の理由は変わらない。
- **`~/Library/Mobile Documents` 全体を例外にする。** 却下: 測ったところ、
  項目の大半はアプリのコンテナで、いくつかはアプリが実行するもの —
  Automator のワークフロー、スクリプト、ショートカット、保管庫のプラグイン —
  を持つ。
- **`~/Library/CloudStorage` そのものを許す。** 却下: これを必要とする同期
  フォルダは 1 つも無く、すべてのアカウントと後から足すプロバイダまで付与する
  ことになる。ADR-0102 はそういうプロジェクトを読み取りでは受け入れたが、
  書き込みの付与は同じ重さではない。
- **どのディレクトリが File Provider のドメインかを macOS に問う。** 却下:
  File Provider フレームワーク（cgo）が要り、macOS がドメインを置くディレクトリが
  すでに答えていることを答えるだけである。ドメインを `~/Library` の別の場所に置く
  プロバイダは断られたままで、安全側に倒れる。
- **拒否を外す運用者の設定。** 却下: スイッチ 1 つで、運用者が望んだフォルダと
  一緒に `LaunchAgents` も開いてしまう。

## References

- `internal/sandbox/roots.go`（`RefuseRoot`）、`internal/sandbox/roots_test.go`
- ADR-0099 §5; ADR-0102（同じ場所の read レーン側）
