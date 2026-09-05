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
	ApprovalTitleFmt  string // dialog title: %s = tool name
	ApproveAllow      string // dialog answer: allow once
	ApproveDeny       string // dialog answer: deny
	ApproveDenyReason string // dialog answer: deny with a typed reason (ADR-0060)
	ApproveAlways     string // dialog answer: allow for the session
	ApprovePersist    string // dialog answer: persist never-ask (ADR-0009 §5)
	ApprovalHint      string // key help under the dialog
	// Reason field (ADR-0060): the label above the input, its
	// placeholder, and the key help while it is open.
	ApprovalReasonPrompt      string
	ApprovalReasonPlaceholder string
	ApprovalReasonHint        string
	// ApprovalHiddenFmt warns that %d detail lines were clipped
	// (ADR-0021: never approve what you have not seen).
	ApprovalHiddenFmt string
	// PurposePrefix marks the model's declared purpose (ADR-0047), and
	// PurposeNone stands in its place when the model declared none —
	// "it did not say" and "there is nothing to say" must not look the
	// same on an approval prompt.
	PurposePrefix   string
	PurposeNone     string
	VerdictApproved string
	VerdictDenied   string
	// VerdictDeniedReasonFmt echoes a reasoned denial: %s = the reason
	// (clipped for display; the transcript keeps it whole).
	VerdictDeniedReasonFmt string
	VerdictAlways          string
	VerdictPersist         string
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
	SettingsHint            string // key help in the /settings title row
	SettingsTitle           string // panel title
	SettingsMoreAboveFmt    string // "… %d more above" scroll marker
	SettingsMoreBelowFmt    string // "… %d more below" scroll marker
	SettingsImmutable       string // a read-only row was activated
	SettingsTooShort        string // the terminal cannot hold the panel
	SettingsSavedTo         string // label before the policy scope
	SettingsScopeGlobal     string // the global policy file
	SettingsScopeProjectFmt string // the project policy file (%s = project dir)
	SettingsUnavailable     string // /settings in a mode without the panel
	NoOutput                string // a shell command printed nothing

	// --- running-status chrome (TUI, ADR-0033) ---
	StatusThinking     string
	StatusCompacting   string
	StatusInterrupting string
	StatusToolWait     string
	StatusRunningFmt   string // %s = tool name
	StatusShellFmt     string // %s = command (clipped)
	// HeartbeatFmt: elapsed, chunk count, seconds since last chunk.
	HeartbeatFmt string
	// StallFmt: seconds with no data — the connection may be dead. It
	// does NOT name Ctrl+C: CtrlCHint renders right after it, and the
	// duplicate pushed the real hint off an 80-column terminal.
	StallFmt string
	// RetryFmt: attempt, max, cause token (429/503/error), wait seconds.
	RetryFmt string
	// ThoughtPrefix marks a live thought-summary line.
	ThoughtPrefix string
	// InterruptStuckWarn: the second Ctrl+C while already
	// interrupting — the next one quits (ADR-0034 §3).
	InterruptStuckWarn string
	// AskTitleFmt / AskHint: the ask_user dialog (ADR-0036).
	AskTitleFmt string // %s = the model's question
	AskHint     string
	// AskHiddenFmt discloses %d wrapped question lines the box could
	// not show (review round 3 — never answer what you have not read).
	AskHiddenFmt string
	// Round-limit intervention (ADR-0040): the dialog question, the
	// review verdict shown as evidence, and the two answers.
	RoundLimitAskFmt        string // %d rounds used, %d hard cap, %s verdict
	RoundLoopAskFmt         string // %s repeated call, %s verdict
	RoundVerdictProgressFmt string // %s = reviewer's reason
	RoundVerdictStuckFmt    string // %s = reviewer's reason
	RoundVerdictErrFmt      string // %s = review error
	RoundContinue           string
	RoundStop               string

	// --- /riskbook (ADR-0050) ---
	// RiskbookStatusLearning is the running-status line while the
	// summary model drafts.
	RiskbookStatusLearning string
	// RiskbookNoDataFmt: %d sessions scanned, no gate decisions found.
	// Saying how much was read distinguishes "nothing yet" from
	// "nothing looked at".
	RiskbookNoDataFmt string
	// RiskbookScannedFmt: %d sessions, %d gate decisions — drafting.
	RiskbookScannedFmt string
	// RiskbookUnreadableFmt: %d transcripts skipped as unreadable.
	RiskbookUnreadableFmt string
	// RiskbookPartialFmt: the session listing was cut at %d files.
	RiskbookPartialFmt string
	// RiskbookDraftHeader precedes the full draft — everything below it
	// is byte-for-byte what would be stored.
	RiskbookDraftHeader string
	// RiskbookAskSave is the review question; Accept/Discard the answers.
	RiskbookAskSave string
	RiskbookAccept  string
	RiskbookDiscard string
	// RiskbookSavedFmt: %s = the project layer's path.
	RiskbookSavedFmt  string
	RiskbookDiscarded string
	// RiskbookStopped: interrupted or declined; nothing was stored.
	RiskbookStopped string
	// RiskbookProvenanceFmt heads a stored draft: date, sessions,
	// decisions — the document says what it was built from.
	RiskbookProvenanceFmt string
	// RiskbookShowBaseFmt / RiskbookShowProjectFmt head the layers in
	// /riskbook show: %s = path — the path IS the provenance; labels
	// restating what the operator already knows are noise (the
	// status-output-is-not-documentation rule). ShowNoneFmt names where
	// the base would be read from: an empty state is the one place
	// teaching belongs, because it is where the operator actually asks
	// "so what do I do?".
	RiskbookShowBaseFmt    string
	RiskbookShowProjectFmt string
	RiskbookShowNoneFmt    string
	RiskbookReloaded       string
	// RiskbookClearedFmt: %s = the removed project layer's path.
	RiskbookClearedFmt string
	RiskbookClearNone  string
	RiskbookUsage      string

	// --- exit summary (cmd) ---
	// Printed once, on the way out — the last thing in the scrollback
	// answers "how do I get back to this?". Skipped when there was no
	// conversation: a resume hint for an empty session would be wrong.
	// ExitSessionFmt: %s = session id (twice).
	ExitSessionFmt string
	// ExitUsageFmt: rounds, prompt tokens, output tokens.
	ExitUsageFmt string
	// ExitAbandonedFmt: %d = tool calls the ADR-0065 floor abandoned
	// that are still running at exit — their effect may land after
	// the process is gone, so the operator hears it.
	ExitAbandonedFmt string
	// ExitFlushing precedes the bounded audit-event flush on the way
	// out (ADR-0065 §4): a silent wait reads as a hang.
	ExitFlushing string

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
	// Integration reload results (ADR-0039).
	MCPDisabled       string // /mcp reload while [mcp].enabled=false / --mcp off
	MCPReloadedFmt    string // fmt: servers (int), tools (int)
	SkillsReloadedFmt string // fmt: skill count (int)

	// --- startup safety (ADR-0023, cmd) ---
	// TrustHeaderFmt opens the first-run prompt: project dir.
	TrustHeaderFmt string
	// TrustItem*Fmt describe what the project provides, naming what
	// each item implies (a server entry is a child process).
	TrustItemInstructionsFmt string // %s = file names
	TrustItemMCPFmt          string // %d = server count
	TrustItemSkillsFmt       string // %d = skill count
	TrustQuestion            string // the [y/N] question
	// Content pins (ADR-0074).
	PinRecordedFmt         string // %d = files pinned, %s = their names
	PinNonePending         string // no pins yet, non-interactive: loaded as before
	PinChangedFmt          string // %s = described change ("AGENTS.md changed (12 bytes)")
	PinQuestion            string // the [y/N] question for one changed file
	PinNotLoadedFmt        string // %s = described change — not loaded
	PinAcceptedFmt         string // %s = name
	PinRemovedFmt          string // %s = name — gone since trusted; pin kept
	PinPendingFmt          string // %s = described changes after an operator command
	PinStaleWriteFmt       string // %s = name — had drifted before the approved write; not re-pinned
	PersistentSinceLastFmt string // %s = comma list — changed since the previous session
	PersistentSessionFmt   string // %s = comma list — added/changed by this session
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
	ApprovalTitleFmt:          "approval required: %s",
	ApproveAllow:              "allow (y)",
	ApproveDeny:               "deny (n)",
	ApproveDenyReason:         "deny with reason (N)",
	ApproveAlways:             "always allow (a)",
	ApprovePersist:            "never ask again (p)",
	ApprovalHint:              "←→/Tab select · Enter confirm · y/n/N/a direct · Esc denies",
	ApprovalReasonPrompt:      "deny reason:",
	ApprovalReasonPlaceholder: "why this call should not run, or what to do instead…",
	ApprovalReasonHint:        "Enter send · empty Enter denies without a reason · Esc back",
	ApprovalHiddenFmt:         "⚠ +%d lines hidden — do not approve without seeing all of it (deny, then inspect)",
	PurposePrefix:             "↪ ",
	PurposeNone:               "(no purpose declared)",
	VerdictApproved:           "approved",
	VerdictDenied:             "denied",
	VerdictDeniedReasonFmt:    "denied — reason: %s",
	VerdictAlways:             "approved (always this session)",
	VerdictPersist:            "approved (and this tool will not ask again)",
	AutoApprovedFmt:           "  ↳ auto-approved (%s): %s",
	CtrlCHint:                 "  (Ctrl+C interrupts)",

	Placeholder:   "message…  Enter send · Ctrl+J newline · /help · !shell",
	QueueRefused:  "⚠ ! and / commands cannot run mid-turn — interrupt with Ctrl+C first (your input is preserved)",
	QueuedPrefix:  "⏎ queued: ",
	QueueHandback: "⚠ the queued message was not sent — the turn did not finish. It is back in the input box",
	Interrupted:   "(interrupted)",
	ErrorPrefix:   "✗ error: ",
	Bye:           "bye",

	SettingsHint:            "  ↑↓ select · ←→/Enter change · s scope · Esc close",
	SettingsTitle:           "settings",
	SettingsMoreAboveFmt:    "  … %d more above",
	SettingsMoreBelowFmt:    "  … %d more below",
	SettingsImmutable:       "this setting cannot change mid-session — edit the config file and restart",
	SettingsTooShort:        "  terminal too short — resize, or edit the config file directly",
	SettingsSavedTo:         "  policy changes are saved to: ",
	SettingsScopeGlobal:     "global (~/.config/gem-agent/policy.toml)",
	SettingsScopeProjectFmt: "this project only — %s",
	SettingsUnavailable:     "✗ settings are unavailable in this mode",
	NoOutput:                "(no output)",

	StatusThinking:          "thinking…",
	StatusCompacting:        "compacting the conversation…",
	StatusInterrupting:      "interrupting…",
	StatusToolWait:          "waiting for the tool…",
	StatusRunningFmt:        "running %s",
	StatusShellFmt:          "shell: %s",
	HeartbeatFmt:            "%s · %d chunks · last %ds",
	StallFmt:                "no data for %ds — the stream may be stalled",
	RetryFmt:                "retry %d/%d (%s) — waiting %ds",
	ThoughtPrefix:           "✦ ",
	InterruptStuckWarn:      "⚠ the tool is not responding to cancellation — one more Ctrl+C quits gem-agent (the transcript up to this call is already saved)",
	AskTitleFmt:             "question: %s",
	AskHint:                 "←→/Tab select · 1-9 pick directly · Enter confirm · Esc declines",
	AskHiddenFmt:            "⚠ +%d lines of the question hidden — Esc to decline and ask for a shorter question, or enlarge the terminal",
	RoundLimitAskFmt:        "round limit reached: %d rounds used (hard cap %d) — %s. Continue?",
	RoundLoopAskFmt:         "possible loop: the same call keeps repeating (%s) — %s. Continue?",
	RoundVerdictProgressFmt: "progress review: progressing (%s)",
	RoundVerdictStuckFmt:    "progress review: possibly stuck (%s)",
	RoundVerdictErrFmt:      "progress review unavailable (%s)",
	RoundContinue:           "continue",
	RoundStop:               "stop here",

	RiskbookStatusLearning: "drafting project risk rules from your decision record…",
	RiskbookNoDataFmt:      "read %d sessions — no gate decisions recorded yet. The rulebook learns from your own answers at the approval gate; you can also write ~/.config/gem-agent/risk-rules.md by hand.",
	RiskbookScannedFmt:     "read %d sessions / %d gate decisions — drafting…",
	RiskbookUnreadableFmt:  "%d transcripts could not be read and were skipped",
	RiskbookPartialFmt:     "more than %d session files — only the first were scanned",
	RiskbookDraftHeader:    "proposed project risk rules — review every line; this exact text is what would be stored:",
	RiskbookAskSave:        "Save these project risk rules? They will inform every auto-mode risk review in this project.",
	RiskbookAccept:         "save",
	RiskbookDiscard:        "discard",
	RiskbookSavedFmt:       "saved to %s — in force now",
	RiskbookDiscarded:      "discarded — nothing was stored",
	RiskbookStopped:        "stopped — nothing was stored",
	RiskbookProvenanceFmt:  "(learned %s from %d sessions / %d gate decisions — operator-reviewed)",
	RiskbookShowBaseFmt:    "base rules — %s:",
	RiskbookShowProjectFmt: "project rules — %s:",
	RiskbookShowNoneFmt:    "no risk rules in force. Write %s by hand, or run /riskbook learn to draft project rules from your decision record.",
	RiskbookReloaded:       "risk rules reloaded from disk",
	RiskbookClearedFmt:     "project risk rules removed (%s)",
	RiskbookClearNone:      "no project risk rules to remove",
	RiskbookUsage:          "usage: /riskbook [show|learn|reload|clear]",
	ExitSessionFmt:         "session %s — resume: gem-agent -c (or --resume %s)",
	ExitUsageFmt:           "%d rounds · prompt %s · output %s",
	ExitAbandonedFmt:       "%d abandoned tool call(s) still running — an effect may still land after this exit",
	ExitFlushing:           "sending audit events… (up to 3s)",

	Help: `commands:
  /help      show this help
  /tools     list tools and each one's current approval gate
  /mcp       list connected MCP servers (/mcp reload reconnects)
  /auto      toggle auto-approve (shift+tab, works mid-run)
  /compact   summarise the older half of the conversation
  /settings  view and edit settings, with provenance
  /riskbook  view the risk rules; /riskbook learn drafts them from your answers
  /usage     token statement for this session
  /memory    list persisted memories
  /skills    list installed skills (/skills reload re-discovers)
  /skill <name> [args]   invoke a skill directly
  /version   version and platform
  /clear     reset the conversation
  /quit      exit (/exit and Ctrl+D too)

attach:
  @<path>      a project file or directory (Tab completes)
  @<image>     images may also use absolute or ~ paths (@~/Desktop/shot.png)
  @clipboard   the clipboard image

shell:
  !<command>   run directly — sandboxed, no approval, output shared with the model

keys:
  Enter send · up/down history · Ctrl+C interrupt/clear · Ctrl+D quit
  Ctrl+J or a trailing \ inserts a newline; a multi-line paste stays one message
  typing during a turn queues the text (! and / cannot be queued)
  approval dialog: arrows/Tab select · Enter confirm · y/n/N/a direct (N = deny with a reason)
`,
	AutoOn:            "auto-approve: ON — safe changes run unattended; risky ones still ask\n",
	AutoOff:           "auto-approve: OFF — every change asks\n",
	HistoryCleared:    "history cleared — the next message starts a fresh conversation\n",
	NothingToCompact:  "nothing to compact yet — the conversation is short enough that a summary would lose more than it saves",
	CompactedFmt:      "compacted %d earlier messages into a summary; %d kept verbatim. Detail from the summarised part is now second-hand",
	UnknownCommandFmt: "unknown command %q — /help lists commands\n",
	MCPNone:           "no MCP servers connected — define them in ~/.config/gem-agent/mcp.json (global) or the project's .mcp.json (project; wins name collisions)\n",
	MCPDisabled:       "MCP is disabled for this session ([mcp].enabled=false or --mcp off) — restart to enable it\n",
	MCPReloadedFmt:    "mcp reloaded: %d server(s), %d tool(s)\n",
	SkillsReloadedFmt: "skills reloaded: %d found\n",

	TrustHeaderFmt:           "\nnew project: %s\nthis project provides:\n",
	TrustItemInstructionsFmt: "%s (injected as instructions)",
	TrustItemMCPFmt:          ".mcp.json (%d server(s) — each starts a child process)",
	TrustItemSkillsFmt:       ".claude/skills/ (%d entr(y/ies) — loaded as your instructions)",
	TrustQuestion:            "trust this project? These files will be treated as YOUR instructions and its MCP servers will run. [y/N]: ",
	PinRecordedFmt:           "project trust: pinned %d file(s): %s",
	PinNonePending:           "project trust: no pins recorded yet — start interactively once, or run `gem-agent trust --accept`",
	PinChangedFmt:            "\n%s since you trusted it.",
	PinQuestion:              "trust the new content? [y/N]: ",
	PinNotLoadedFmt:          "project trust: %s since you trusted it — not loaded (`gem-agent trust --accept`, or an interactive start, re-trusts)",
	PinAcceptedFmt:           "project trust: %s re-trusted",
	PinRemovedFmt:            "project trust: %s was removed since you trusted it",
	PinPendingFmt:            "project trust: %s since you trusted it — not re-trusted; the next interactive start asks (`gem-agent trust --accept` re-trusts)",
	PinStaleWriteFmt:         "project trust: %s had changed before this write — not re-trusted; the next interactive start asks",
	PersistentSinceLastFmt:   "note: changed since your previous session: %s",
	PersistentSessionFmt:     "note: this session added or changed: %s",
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
	ApprovalTitleFmt:          "承認が必要です: %s",
	ApproveAllow:              "許可 (y)",
	ApproveDeny:               "拒否 (n)",
	ApproveDenyReason:         "理由を添えて拒否 (N)",
	ApproveAlways:             "常に許可 (a)",
	ApprovePersist:            "今後聞かない (p)",
	ApprovalHint:              "←→/Tab 選択 · Enter 決定 · y/n/N/a 直接指定 · Esc 拒否",
	ApprovalReasonPrompt:      "拒否理由:",
	ApprovalReasonPlaceholder: "拒否する理由や、代わりにすべきこと…",
	ApprovalReasonHint:        "Enter 送信 · 空 Enter は理由なし拒否 · Esc で戻る",
	ApprovalHiddenFmt:         "⚠ +%d 行が省略されています — 全体を見るまで承認しないでください（拒否して確認できます）",
	PurposePrefix:             "↪ ",
	PurposeNone:               "（理由の申告なし）",
	VerdictApproved:           "許可しました",
	VerdictDenied:             "拒否しました",
	VerdictDeniedReasonFmt:    "拒否しました — 理由: %s",
	VerdictAlways:             "許可しました（このセッション中は常に）",
	VerdictPersist:            "許可しました（このツールは今後確認しません）",
	AutoApprovedFmt:           "  ↳ 自動承認 (%s): %s",
	CtrlCHint:                 "  (Ctrl+C で中断)",

	Placeholder:   "message…  Enter 送信 · Ctrl+J 改行 · /help · !shell",
	QueueRefused:  "⚠ ! と / のコマンドは実行中には送れません — Ctrl+C で中断してから実行してください（入力は残っています）",
	QueuedPrefix:  "⏎ 予約: ",
	QueueHandback: "⚠ 予約したメッセージは送信されませんでした — ターンが正常に終了しなかったため、入力欄に戻しています",
	Interrupted:   "（中断）",
	ErrorPrefix:   "✗ エラー: ",
	Bye:           "bye",

	SettingsHint:            "  ↑↓ 選択 · ←→/Enter 変更 · s 保存先 · Esc 閉じる",
	SettingsTitle:           "設定",
	SettingsMoreAboveFmt:    "  … 上に %d 件",
	SettingsMoreBelowFmt:    "  … 下に %d 件",
	SettingsImmutable:       "この設定はセッション中に変更できません — 設定ファイルを編集して再起動してください",
	SettingsTooShort:        "  端末の高さが足りません — 広げるか、設定ファイルを直接編集してください",
	SettingsSavedTo:         "  ポリシーの保存先: ",
	SettingsScopeGlobal:     "グローバル (~/.config/gem-agent/policy.toml)",
	SettingsScopeProjectFmt: "このプロジェクトのみ — %s",
	SettingsUnavailable:     "✗ このモードでは設定パネルを使えません",
	NoOutput:                "(出力なし)",

	StatusThinking:          "thinking…",
	StatusCompacting:        "会話を圧縮中…",
	StatusInterrupting:      "中断中…",
	StatusToolWait:          "ツールの完了待ち…",
	StatusRunningFmt:        "実行中 %s",
	StatusShellFmt:          "shell: %s",
	HeartbeatFmt:            "%s · %d chunks · last %ds",
	StallFmt:                "%d 秒間データなし — 接続が失速している可能性",
	RetryFmt:                "リトライ %d/%d (%s) — %d 秒待機",
	ThoughtPrefix:           "✦ ",
	InterruptStuckWarn:      "⚠ ツールがキャンセルに応答していません — もう一度 Ctrl+C で gem-agent を終了します（この呼び出しまでの transcript は保存済みです）",
	AskTitleFmt:             "質問: %s",
	AskHint:                 "←→/Tab 選択 · 1-9 で即決定 · Enter 決定 · Esc 回答しない",
	AskHiddenFmt:            "⚠ 質問の +%d 行が非表示 — Esc で辞退して短い質問を求めるか、端末を広げてください",
	RoundLimitAskFmt:        "ラウンド上限に到達: %d ラウンド消費（絶対上限 %d）— %s。続行しますか？",
	RoundLoopAskFmt:         "ループの疑い: 同一コールが反復しています（%s）— %s。続行しますか？",
	RoundVerdictProgressFmt: "進捗レビュー: 前進中（%s）",
	RoundVerdictStuckFmt:    "進捗レビュー: 停滞の疑い（%s）",
	RoundVerdictErrFmt:      "進捗レビュー不能（%s）",
	RoundContinue:           "続行",
	RoundStop:               "ここで停止",

	RiskbookStatusLearning: "判断記録からプロジェクトのリスクルールを起草しています…",
	RiskbookNoDataFmt:      "%d セッションを読みました — 記録されたゲート判断はまだありません。ルールブックは承認ゲートでのあなた自身の回答から学びます。~/.config/gem-agent/risk-rules.md を手で書くこともできます。",
	RiskbookScannedFmt:     "%d セッション / %d 件のゲート判断を読みました — 起草中…",
	RiskbookUnreadableFmt:  "%d 件の記録は読めなかったため飛ばしました",
	RiskbookPartialFmt:     "セッションファイルが %d 件を超えるため、先頭分だけを走査しました",
	RiskbookDraftHeader:    "プロジェクトリスクルールの提案 — 全行を確認してください。保存されるのはこのテキストそのものです:",
	RiskbookAskSave:        "このプロジェクトリスクルールを保存しますか？ このプロジェクトの auto モードの全リスク評価が参照するようになります。",
	RiskbookAccept:         "保存",
	RiskbookDiscard:        "破棄",
	RiskbookSavedFmt:       "%s に保存しました — いま有効です",
	RiskbookDiscarded:      "破棄しました — 何も保存されていません",
	RiskbookStopped:        "中止しました — 何も保存されていません",
	RiskbookProvenanceFmt:  "（%s に %d セッション / %d 件のゲート判断から学習 — オペレータレビュー済み）",
	RiskbookShowBaseFmt:    "ベースルール — %s:",
	RiskbookShowProjectFmt: "プロジェクトルール — %s:",
	RiskbookShowNoneFmt:    "有効なリスクルールはありません。%s を手で書くか、/riskbook learn で判断記録から起草できます。",
	RiskbookReloaded:       "リスクルールをディスクから読み直しました",
	RiskbookClearedFmt:     "プロジェクトリスクルールを削除しました（%s）",
	RiskbookClearNone:      "削除するプロジェクトリスクルールはありません",
	RiskbookUsage:          "使い方: /riskbook [show|learn|reload|clear]",
	ExitSessionFmt:         "セッション %s — 再開: gem-agent -c（または --resume %s）",
	ExitUsageFmt:           "%d ラウンド · prompt %s · output %s",
	ExitAbandonedFmt:       "放棄したツール呼び出し %d 件がまだ実行中 — 終了後に効果が及ぶことがあります",
	ExitFlushing:           "監査イベントを送信中…（最大 3 秒）",

	Help: `コマンド:
  /help      このヘルプ
  /tools     ツール一覧と各ツールの現在の承認ゲート
  /mcp       接続中の MCP サーバー一覧（/mcp reload で再接続）
  /auto      auto-approve 切替（shift+tab でも可・実行中も有効）
  /compact   会話の古い半分を要約
  /settings  設定の表示と編集（出所つき）
  /riskbook  リスクルールの表示。/riskbook learn は回答記録から起草
  /usage     このセッションのトークン明細
  /memory    永続メモリの一覧
  /skills    インストール済みスキル一覧（/skills reload で再探索）
  /skill <name> [args]   スキルを直接起動
  /version   バージョンとプラットフォーム
  /clear     会話履歴をリセット
  /quit      終了（/exit・Ctrl+D でも可）

添付:
  @<パス>      プロジェクト内のファイル/ディレクトリ（Tab 補完）
  @<画像>      画像は絶対パス・~ パスも可（@~/Desktop/shot.png）
  @clipboard   クリップボードの画像

シェル:
  !<コマンド>   直接実行 — sandbox 下・承認なし・出力はモデルと共有

キー:
  Enter 送信 · ↑↓ 履歴 · Ctrl+C 中断/クリア · Ctrl+D 終了
  改行は Ctrl+J か行末 \ + Enter。複数行ペーストは 1 メッセージのまま
  実行中の入力は次メッセージとして予約（! と / は予約不可）
  承認ダイアログ: ←→/Tab 選択 · Enter 決定 · y/n/N/a 直接（N = 理由を添えて拒否）
`,
	AutoOn:            "auto-approve: ON — 安全な変更は無人で実行します。危険なものは引き続き確認します\n",
	AutoOff:           "auto-approve: OFF — すべての変更で確認します\n",
	HistoryCleared:    "履歴をクリアしました — 次のメッセージから新しい会話が始まります\n",
	NothingToCompact:  "まだ /compact の対象がありません — 会話が短く、要約すると失う情報のほうが多くなります",
	CompactedFmt:      "古いメッセージ %d 件を要約に畳みました; %d 件はそのまま保持。要約された部分の詳細は伝聞になります",
	UnknownCommandFmt: "未知のコマンド %q — /help に一覧があります\n",
	MCPNone:           "MCP サーバー未接続 — ~/.config/gem-agent/mcp.json（グローバル）またはプロジェクトの .mcp.json（プロジェクト側が名前衝突で優先）で定義します\n",
	MCPDisabled:       "MCP はこのセッションでは無効です（[mcp].enabled=false または --mcp off）— 有効化するには再起動してください\n",
	MCPReloadedFmt:    "MCP を再接続しました: %d サーバー・%d ツール\n",
	SkillsReloadedFmt: "skill を再読込しました: %d 件\n",

	TrustHeaderFmt:           "\n新しいプロジェクト: %s\nこのプロジェクトの提供物:\n",
	TrustItemInstructionsFmt: "%s（instructions として注入されます）",
	TrustItemMCPFmt:          ".mcp.json（サーバー %d 件 — それぞれ子プロセスを起動します）",
	TrustItemSkillsFmt:       ".claude/skills/（%d 件 — あなたへの指示として読み込まれます）",
	TrustQuestion:            "このプロジェクトを信用しますか？ これらのファイルはあなたへの指示として扱われ、MCP サーバーが起動します。 [y/N]: ",
	PinRecordedFmt:           "project trust: %d 件をピン留めしました: %s",
	PinNonePending:           "project trust: ピンは未記録です — 一度対話モードで起動するか `gem-agent trust --accept` を実行してください",
	PinChangedFmt:            "\n%s — 信用した時点から変わっています。",
	PinQuestion:              "新しい内容を信用しますか？ [y/N]: ",
	PinNotLoadedFmt:          "project trust: %s — 信用した時点から変わっています。読み込みません（`gem-agent trust --accept` か対話起動で再信用）",
	PinAcceptedFmt:           "project trust: %s を再信用しました",
	PinRemovedFmt:            "project trust: %s は信用した時点から削除されています",
	PinPendingFmt:            "project trust: %s — 信用した時点から変わっています。再信用していません。次の対話起動で確認します（`gem-agent trust --accept` でも可）",
	PinStaleWriteFmt:         "project trust: %s はこの書込の前から変わっていました — 再信用していません。次の対話起動で確認します",
	PersistentSinceLastFmt:   "note: 前回のセッション以降に変更: %s",
	PersistentSessionFmt:     "note: このセッションが追加・変更: %s",
	TrustDeclinedFmt:         "project trust: 拒否 — このプロジェクトの instruction ファイル・.mcp.json・skills は読み込まれません（再確認するには %s を編集）",
	TrustUndecided:           "project trust: 未決定（非対話） — このプロジェクトの instruction ファイル・.mcp.json・skills は読み込まれません。対話モードで一度起動して決定してください",
	ReasonFSRoot:             "ファイルシステムのルート",
	ReasonHome:               "ホームディレクトリ",
	ReasonHomeAncestor:       "ホームディレクトリの祖先",
	BroadRootPromptFmt:       "\n⚠ %s は%sです。\nファイルツールとサンドボックス内シェルの書き込みが、このツリー全体に及びます。\nこのまま起動しますか？ [y/N]: ",
	BroadRootRefusedFmt:      "%s（%s）では起動を拒否します: ファイルツールとシェル書き込みがツリー全体に及びます。対話モードで確認するか、プロジェクトディレクトリで起動してください",
	BroadRootAbortFmt:        "%s では起動しません — まずプロジェクトディレクトリに cd してください",
}
