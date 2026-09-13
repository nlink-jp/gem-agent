# ADR-0088: 呼び出す側が work dir を告げる — 全 tools/call に `_meta` を付ける

| Field | Value |
|-------|-------|
| Status | **Accepted**（2026-09-13） |
| Date | 2026-09-13 |
| Binds | gem-agent |
| Decision makers | nlink-jp メンテナ |
| Triggered by | 組織 ADR-021（ファイル渡し MCP サーバーの work dir 契約）が確定し、フリート 10 サーバーが `work_dir` を必須で取るようになったこと。契約は呼び出し側にも義務を置く |
| Relates to | ADR-0058（大きな MCP 結果を work dir へ退避する intake）、ADR-0071（子へ渡す 3 つの export）、ADR-0087（自分のものだけを隠す） |

## Context（背景）

組織 ADR-021 が決めたのは「ファイルを産む MCP サーバーは、出力先を**呼び出しごとの
引数** `work_dir` で受け取る」である。4 ランタイムの実測で、MCP の `roots` も環境変数も
半数には届かず、呼び出しごとの引数だけが共通の経路だと分かったためである。

その契約には第 2 の経路がある —— リクエストの `_meta["jp.nlink/work_dir"]`。これは
**自分たちが書いたランタイムのためのもの**で、スキーマを知らなくても全ての
`tools/call` に付けられる。モデルが引数を書き忘れても、サーバーは使える出力先を
得られる。

このランタイムには既に `GEMAGENT_WORK_DIR` の export があるが（ADR-0071）、それは
子プロセスの環境変数であって、**プロトコル上の値ではない**。サーバーがそれを読むには
登録行に `${GEMAGENT_WORK_DIR}` を書く運用が要り、同居ランタイムでは未定義変数が
無言で空文字に展開される。`_meta` はその運用を不要にする。

## Decision（決定）

1. `mcp.Client` はセッションの work dir を保持し、**全ての `tools/call` の
   `params._meta` に `jp.nlink/work_dir` を付ける**。
2. **work dir が無いセッションでは何も付けない。** 空の hint は無いより悪い ——
   サーバーがそれを答えとして受け取ってしまう。
3. 値は `NewStdio` の引数として渡す。グローバルにもセッターにもしない ——
   クライアントが生きている間変わらない値であり、呼び出し地点で見えるべきである。
4. **モデルが書いた引数が常に勝つ。** `_meta` はサーバー側で「引数が無かったとき」
   にだけ読まれる（ADR-021 §2 の解決順）。ランタイムは既定を供給するのであって、
   呼び出し側の意図を上書きしない。

## Consequences（結果）

- 自前サーバー 10 本が、モデルが `work_dir` を書かなくても本セッションの work dir に
  書くようになる。Claude Code / Codex は引数を書く必要があり、そこは変わらない
- chrome-pilot の登録行から `--workspace-root ${GEMAGENT_WORK_DIR}` が不要になる。
  これは gem-agent と lagent の `mcp.json` を 2 実体に分けていた唯一の差分だった
- `_meta` を知らないサーバー（github、obsidian 等）は無視する。MCP の `_meta` は
  そのための領域である

## References

- 組織 ADR-021（work dir 契約 §2・§9）、ADR-0058（intake）、ADR-0071（3 つの export）
