// Package uitext holds the operator-facing UI strings in two complete
// catalogs, Japanese and English (ADR-0029). One struct field per
// string keeps the two languages covering the same surface: the
// completeness test fails on any field left empty in either catalog,
// which is the mechanism that stops the historical one-string-at-a-time
// language drift from ever re-accumulating.
//
// Deliberately NOT here (ADR-0029 §3): banner labels and "warning:"
// lines (grep-stable log output), cobra --help, model-facing text, and
// Go error chains.
package uitext

import "strings"

// Lang is a resolved UI language.
type Lang string

const (
	JA Lang = "ja"
	EN Lang = "en"
)

// Resolve turns the configured [tui].language value into a Lang.
// "auto" (and anything unrecognized — config validation rejects it
// earlier) follows the POSIX message-catalog convention: the first
// non-empty of LC_ALL, LC_MESSAGES, LANG decides, and only a "ja"
// prefix selects Japanese — "C" and "POSIX" mean English.
func Resolve(configured string, getenv func(string) string) Lang {
	switch configured {
	case "ja":
		return JA
	case "en":
		return EN
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(key); v != "" {
			if strings.HasPrefix(v, "ja") {
				return JA
			}
			return EN
		}
	}
	return EN
}

// For returns the catalog for lang. The pointer is shared and the
// catalogs are never mutated.
func For(lang Lang) *Messages {
	if lang == JA {
		return &ja
	}
	return &en
}

// Messages is one language's worth of interactive chrome. Every field
// must be non-empty in both catalogs (enforced by test); fields whose
// name ends in Fmt are fmt.Sprintf patterns and both catalogs must use
// the same verbs in the same order.
type Messages struct {
	// --- approval dialog (TUI) ---
	ApprovalTitleFmt string // dialog title: %s = tool name
	ApproveAllow     string // dialog answer: allow once
	ApproveDeny      string // dialog answer: deny
	ApproveAlways    string // dialog answer: allow for the session
	ApprovePersist   string // dialog answer: persist never-ask (ADR-0009 §5)
	ApprovalHint     string // key help under the dialog
	// ApprovalHiddenFmt warns that %d detail lines were clipped
	// (ADR-0021: never approve what you have not seen).
	ApprovalHiddenFmt string
	VerdictApproved   string
	VerdictDenied     string
	VerdictAlways     string
	VerdictPersist    string
	// AutoApprovedFmt echoes an unattended approval: tier, reason.
	AutoApprovedFmt string
	CtrlCHint       string // "(interrupt with Ctrl+C)" while running

	// --- input chrome (TUI) ---
	Placeholder  string // empty input box hint
	QueueRefused string // ! and / cannot be queued mid-turn (ADR-0021)
	QueuedPrefix string // prefix before an echoed queued message
	// QueueHandback explains a queued message returning unsent after a
	// failed or interrupted turn (ADR-0007).
	QueueHandback string
	Interrupted   string // "(interrupted)" marker
	ErrorPrefix   string // prefix before a turn/shell error
	Bye           string // parting word on quit

	// --- settings panel (TUI) ---
	SettingsHint string // key help in the /settings title row

	// --- running-status chrome (TUI, ADR-0033) ---
	StatusThinking     string
	StatusCompacting   string
	StatusInterrupting string
	StatusToolWait     string
	StatusRunningFmt   string // %s = tool name
	StatusShellFmt     string // %s = command (clipped)
	// HeartbeatFmt: elapsed, chunk count, seconds since last chunk.
	HeartbeatFmt string
	// StallFmt: seconds with no data — the connection may be dead.
	StallFmt string
	// RetryFmt: attempt, max, cause token (429/503/error), wait seconds.
	RetryFmt string
	// ThoughtPrefix marks a live thought-summary line.
	ThoughtPrefix string
	// InterruptStuckWarn: the second Ctrl+C while already
	// interrupting — the next one quits (ADR-0034 §3).
	InterruptStuckWarn string

	// --- slash command feedback (cmd) ---
	Help             string // the full /help text
	AutoOn           string
	AutoOff          string
	HistoryCleared   string
	NothingToCompact string
	// CompactedFmt reports a /compact: messages summarised, kept.
	CompactedFmt string
	// UnknownCommandFmt: %s = the input that matched no command.
	UnknownCommandFmt string
	MCPNone           string // /mcp with nothing connected
	// MCPToolsNote states the real gate honestly: MCP tools ARE
	// approval-gated, and in auto mode the risk review may pass
	// routine calls (review round 2 — the old text overstated).
	MCPToolsNote string

	// --- startup safety (ADR-0023, cmd) ---
	// TrustHeaderFmt opens the first-run prompt: project dir.
	TrustHeaderFmt string
	// TrustItem*Fmt describe what the project provides, naming what
	// each item implies (a server entry is a child process).
	TrustItemInstructionsFmt string // %s = file names
	TrustItemMCPFmt          string // %d = server count
	TrustItemSkillsFmt       string // %d = skill count
	TrustQuestion            string // the [y/N] question
	// TrustDeclinedFmt is the banner note after declining: policy path.
	TrustDeclinedFmt string
	TrustUndecided   string // non-interactive, undecided: ran bare
	// Broad-root gate (ADR-0023 §1). Reasons name what projectDir is.
	ReasonFSRoot        string
	ReasonHome          string
	ReasonHomeAncestor  string
	BroadRootPromptFmt  string // %s dir, %s reason
	BroadRootRefusedFmt string // non-interactive refusal: %s dir, %s reason
	BroadRootAbortFmt   string // declined: %s dir
}

// BroadReason maps a broadRoot key ("root", "home", "home-ancestor")
// to its localized description.
func (m *Messages) BroadReason(key string) string {
	switch key {
	case "root":
		return m.ReasonFSRoot
	case "home":
		return m.ReasonHome
	case "home-ancestor":
		return m.ReasonHomeAncestor
	}
	return key
}

var en = Messages{
	ApprovalTitleFmt:  "approval required: %s",
	ApproveAllow:      "allow (y)",
	ApproveDeny:       "deny (n)",
	ApproveAlways:     "always allow (a)",
	ApprovePersist:    "never ask again (p)",
	ApprovalHint:      "←→/Tab select · Enter confirm · y/n/a direct · Esc denies",
	ApprovalHiddenFmt: "⚠ +%d lines hidden — do not approve without seeing all of it (deny, then inspect)",
	VerdictApproved:   "approved",
	VerdictDenied:     "denied",
	VerdictAlways:     "approved (always this session)",
	VerdictPersist:    "approved (and this tool will not ask again)",
	AutoApprovedFmt:   "  ↳ auto-approved (%s): %s",
	CtrlCHint:         "  (Ctrl+C interrupts)",

	Placeholder:   "message…  Enter send · Ctrl+J newline · /help · !shell",
	QueueRefused:  "⚠ ! and / commands cannot run mid-turn — interrupt with Ctrl+C first (your input is preserved)",
	QueuedPrefix:  "⏎ queued: ",
	QueueHandback: "⚠ the queued message was not sent — the turn did not finish. It is back in the input box",
	Interrupted:   "(interrupted)",
	ErrorPrefix:   "✗ error: ",
	Bye:           "bye",

	SettingsHint: "  ↑↓ select · ←→/Enter change · s scope · Esc close",

	StatusThinking:     "thinking…",
	StatusCompacting:   "compacting the conversation…",
	StatusInterrupting: "interrupting…",
	StatusToolWait:     "waiting for the tool…",
	StatusRunningFmt:   "running %s",
	StatusShellFmt:     "shell: %s",
	HeartbeatFmt:       "%s · %d chunks · last %ds",
	StallFmt:           "no data for %ds — the connection may be stalled (Ctrl+C interrupts)",
	RetryFmt:           "retry %d/%d (%s) — waiting %ds",
	ThoughtPrefix:      "✦ ",
	InterruptStuckWarn: "⚠ the tool is not responding to cancellation — one more Ctrl+C quits gem-agent (the transcript up to this call is already saved)",

	Help: `commands:
  /help    show this help
  /tools   list available tools
  /mcp     show connected MCP servers
  /auto    toggle auto-approve (shift+tab does the same, and works mid-run)
  /compact summarise the older half of the conversation to free context
  /settings show every setting with where it came from; edit policy + toggles
  /usage   token accounting: main loop, cache hit rate, side-calls, web tools
  /memory  list persisted memories (global + this project); saves are approval-gated
  /skills  list installed skills (Claude Code format, read as-is)
  /skill <name> [args]  invoke a skill directly
  /clear   reset the conversation history
  /quit    exit (Ctrl+D also works; /exit is an alias)
auto-approve: safe changes run unattended; destructive, out-of-project,
  credential-touching, or uncertain calls still ask (two-tier review)
completion:
  Tab completes /commands (and skill names after "/skill "), and
  @-references below
file references:
  @<path>       attach a project file or directory to the message (Tab completes)
  @<img>.png    attach an image — absolute and ~ paths work for images
                (@~/Desktop/shot.png), because you typed them yourself
  @clipboard    attach the clipboard image (Cmd+Ctrl+Shift+4, then this)
shell:
  !<command>  run it directly (sandboxed, no approval; output is shared with the model)
keys:
  Enter send · ↑↓ history · Ctrl+C interrupt/clear · Ctrl+D quit
  You can keep typing while a turn runs: Enter queues the text as the
    next message, sent when the turn ends cleanly (after a failure or
    interrupt it returns to the input box unsent)
    ※ ! and / commands cannot be queued — interrupt with Ctrl+C first
  newline (multi-line input): Ctrl+J, or end the line with \ and press Enter
    ※ Option+Enter works only in terminals that send Option as Meta
      (by default it produces the same bytes as plain Enter, and sends)
  a multi-line paste becomes one message as-is
  approval dialog: ←→/Tab select · Enter confirm (y/n/a also work)
mutating tools prompt for approval: y = once, a = always this session
  (Block-tier calls and always-policy tools keep asking — 'a' never
   covers the dangerous cases, only the routine ones)
`,
	AutoOn:            "auto-approve: ON — safe changes run unattended; risky ones still ask\n",
	AutoOff:           "auto-approve: OFF — every change asks\n",
	HistoryCleared:    "history cleared — the next message starts a fresh conversation\n",
	NothingToCompact:  "nothing to compact yet — the conversation is short enough that a summary would lose more than it saves",
	CompactedFmt:      "compacted %d earlier messages into a summary; %d kept verbatim. Detail from the summarised part is now second-hand",
	UnknownCommandFmt: "unknown command %q — /help lists commands\n",
	MCPNone:           "no MCP servers connected — define them in ~/.config/gem-agent/mcp.json (global) or the project's .mcp.json (project; wins name collisions)\n",
	MCPToolsNote:      "MCP tools appear in /tools as mcp__<server>__<tool>; they are approval-gated (in auto-approve mode, the risk review may run routine calls unattended, and 'a' covers a tool for the session)\n",

	TrustHeaderFmt:           "\nnew project: %s\nthis project provides:\n",
	TrustItemInstructionsFmt: "%s (injected as instructions)",
	TrustItemMCPFmt:          ".mcp.json (%d server(s) — each starts a child process)",
	TrustItemSkillsFmt:       ".claude/skills/ (%d entr(y/ies) — loaded as your instructions)",
	TrustQuestion:            "trust this project? These files will be treated as YOUR instructions and its MCP servers will run. [y/N]: ",
	TrustDeclinedFmt:         "project trust: declined — the project's instruction files, .mcp.json, and skills are not loaded (edit %s to re-ask)",
	TrustUndecided:           "project trust: undecided (non-interactive) — the project's instruction files, .mcp.json, and skills are not loaded; run interactively once to decide",
	ReasonFSRoot:             "the filesystem root",
	ReasonHome:               "your home directory",
	ReasonHomeAncestor:       "an ancestor of your home directory",
	BroadRootPromptFmt:       "\n⚠ %s is %s.\nFile tools and sandboxed shell writes would span this ENTIRE tree.\nstart anyway? [y/N]: ",
	BroadRootRefusedFmt:      "refusing to start in %s (%s): file tools and shell writes would span this entire tree; run interactively to confirm, or start in a project directory",
	BroadRootAbortFmt:        "not starting in %s — cd into a project directory first",
}

var ja = Messages{
	ApprovalTitleFmt:  "承認が必要です: %s",
	ApproveAllow:      "許可 (y)",
	ApproveDeny:       "拒否 (n)",
	ApproveAlways:     "常に許可 (a)",
	ApprovePersist:    "今後聞かない (p)",
	ApprovalHint:      "←→/Tab 選択 · Enter 決定 · y/n/a 直接指定 · Esc 拒否",
	ApprovalHiddenFmt: "⚠ +%d 行が省略されています — 全体を見るまで承認しないでください（拒否して確認できます）",
	VerdictApproved:   "許可しました",
	VerdictDenied:     "拒否しました",
	VerdictAlways:     "許可しました（このセッション中は常に）",
	VerdictPersist:    "許可しました（このツールは今後確認しません）",
	AutoApprovedFmt:   "  ↳ 自動承認 (%s): %s",
	CtrlCHint:         "  (Ctrl+C で中断)",

	Placeholder:   "message…  Enter 送信 · Ctrl+J 改行 · /help · !shell",
	QueueRefused:  "⚠ ! と / のコマンドは実行中には送れません — Ctrl+C で中断してから実行してください（入力は残っています）",
	QueuedPrefix:  "⏎ 予約: ",
	QueueHandback: "⚠ 予約したメッセージは送信されませんでした — ターンが正常に終了しなかったため、入力欄に戻しています",
	Interrupted:   "（中断）",
	ErrorPrefix:   "✗ エラー: ",
	Bye:           "bye",

	SettingsHint: "  ↑↓ 選択 · ←→/Enter 変更 · s 保存先 · Esc 閉じる",

	StatusThinking:     "thinking…",
	StatusCompacting:   "会話を圧縮中…",
	StatusInterrupting: "中断中…",
	StatusToolWait:     "ツールの完了待ち…",
	StatusRunningFmt:   "実行中 %s",
	StatusShellFmt:     "shell: %s",
	HeartbeatFmt:       "%s · %d chunks · last %ds",
	StallFmt:           "%d 秒間データなし — 接続が失速している可能性（Ctrl+C で中断できます）",
	RetryFmt:           "リトライ %d/%d (%s) — %d 秒待機",
	ThoughtPrefix:      "✦ ",
	InterruptStuckWarn: "⚠ ツールがキャンセルに応答していません — もう一度 Ctrl+C で gem-agent を終了します（この呼び出しまでの transcript は保存済みです）",

	Help: `コマンド:
  /help    このヘルプを表示
  /tools   利用可能なツールの一覧
  /mcp     接続中の MCP サーバーを表示
  /auto    auto-approve を切替（shift+tab でも可・実行中も有効）
  /compact 会話の古い半分を要約してコンテキストを空ける
  /settings 全設定を出所つきで表示; ポリシーとトグルを編集
  /usage   トークン集計: メインループ・キャッシュ命中率・サイドコール・Web ツール
  /memory  永続メモリの一覧（グローバル + このプロジェクト）; 保存は承認制
  /skills  インストール済みスキルの一覧（Claude Code 形式をそのまま読む）
  /skill <name> [args]  スキルを直接起動
  /clear   会話履歴をリセット
  /quit    終了（Ctrl+D でも可・/exit も同じ）
auto-approve: 安全な変更は無人で実行します。破壊的・プロジェクト外・
  認証情報に触れる・判定不能の呼び出しは引き続き確認します（二段レビュー）
補完:
  Tab で /コマンドを補完（"/skill " の後はスキル名も）。下記の
  @参照も補完されます
ファイル参照:
  @<path>       プロジェクト内のファイル/ディレクトリを添付（Tab 補完）
  @<img>.png    画像を添付 — 画像は絶対パスと ~ も使えます
                （@~/Desktop/shot.png）。自分で入力したパスだからです
  @clipboard    クリップボードの画像を添付（Cmd+Ctrl+Shift+4 のあとに）
シェル:
  !<command>  直接実行（サンドボックス内・承認なし; 出力はモデルと共有されます）
キー:
  Enter 送信 · ↑↓ 履歴 · Ctrl+C 中断/クリア · Ctrl+D 終了
  実行中も入力できます: Enter で次のメッセージとして予約され、ターンが正常に
    終わった時点で送信されます（失敗・中断時は未送信のまま入力欄へ戻ります）
    ※ ! と / のコマンドは予約できません — Ctrl+C で中断してから実行します
  改行（複数行入力）: Ctrl+J もしくは 行末に \ を置いて Enter
    ※ Option+Enter は「Option を Meta として送る」設定の端末でのみ有効
      （既定では通常の Enter と同じバイトになり送信されます）
  複数行ペーストはそのまま 1 メッセージになります
  承認ダイアログ: ←→/Tab で選択 · Enter 決定（y/n/a も可）
変更系ツールは承認を求めます: y = 今回のみ, a = このセッション中は常に許可
  （Block 段の呼び出しと always ポリシーのツールは常に確認します — 'a' が
   カバーするのは日常的な操作だけで、危険な操作には効きません）
`,
	AutoOn:            "auto-approve: ON — 安全な変更は無人で実行します。危険なものは引き続き確認します\n",
	AutoOff:           "auto-approve: OFF — すべての変更で確認します\n",
	HistoryCleared:    "履歴をクリアしました — 次のメッセージから新しい会話が始まります\n",
	NothingToCompact:  "まだ /compact の対象がありません — 会話が短く、要約すると失う情報のほうが多くなります",
	CompactedFmt:      "古いメッセージ %d 件を要約に畳みました; %d 件はそのまま保持。要約された部分の詳細は伝聞になります",
	UnknownCommandFmt: "未知のコマンド %q — /help に一覧があります\n",
	MCPNone:           "MCP サーバー未接続 — ~/.config/gem-agent/mcp.json（グローバル）またはプロジェクトの .mcp.json（プロジェクト側が名前衝突で優先）で定義します\n",
	MCPToolsNote:      "MCP ツールは /tools に mcp__<server>__<tool> として表示され、承認ゲートの対象です（auto-approve モードではリスクレビューが日常的な呼び出しを無人実行することがあり、'a' はそのツールをセッション中カバーします）\n",

	TrustHeaderFmt:           "\n新しいプロジェクト: %s\nこのプロジェクトの提供物:\n",
	TrustItemInstructionsFmt: "%s（instructions として注入されます）",
	TrustItemMCPFmt:          ".mcp.json（サーバー %d 件 — それぞれ子プロセスを起動します）",
	TrustItemSkillsFmt:       ".claude/skills/（%d 件 — あなたへの指示として読み込まれます）",
	TrustQuestion:            "このプロジェクトを信用しますか？ これらのファイルはあなたへの指示として扱われ、MCP サーバーが起動します。 [y/N]: ",
	TrustDeclinedFmt:         "project trust: 拒否 — このプロジェクトの instruction ファイル・.mcp.json・skills は読み込まれません（再確認するには %s を編集）",
	TrustUndecided:           "project trust: 未決定（非対話） — このプロジェクトの instruction ファイル・.mcp.json・skills は読み込まれません。対話モードで一度起動して決定してください",
	ReasonFSRoot:             "ファイルシステムのルート",
	ReasonHome:               "ホームディレクトリ",
	ReasonHomeAncestor:       "ホームディレクトリの祖先",
	BroadRootPromptFmt:       "\n⚠ %s は%sです。\nファイルツールとサンドボックス内シェルの書き込みが、このツリー全体に及びます。\nこのまま起動しますか？ [y/N]: ",
	BroadRootRefusedFmt:      "%s（%s）では起動を拒否します: ファイルツールとシェル書き込みがツリー全体に及びます。対話モードで確認するか、プロジェクトディレクトリで起動してください",
	BroadRootAbortFmt:        "%s では起動しません — まずプロジェクトディレクトリに cd してください",
}
