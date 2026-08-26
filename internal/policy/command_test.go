package policy

import "testing"

// ADR-0045 §3: the key is a syntactic fact. The table is the contract
// both sides of the feature share — the learner aggregates by it and the
// gate matches by it — so every exclusion is pinned here.
func TestCommandKey(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    string
		ok      bool
	}{
		// Head plus a subcommand-shaped second token.
		{"go test ./...", "go test", true},
		{"make build", "make build", true},
		{"git status", "git status", true},
		{"git filter-branch --all", "git filter-branch", true},
		{"go test", "go test", true},
		// Second token is not a subcommand: the head alone.
		{"ls -la", "ls", true},
		{"touch newfile.txt", "touch", true},
		{"cat README.md", "cat", true},
		{"npm --version", "npm", true},
		{"ls", "ls", true},
		{"python3 script.py", "python3", true},
		// Uppercase and mixed-case second tokens are not subcommand
		// shape — a filename is the likelier reading.
		{"cat Makefile", "cat", true},

		// No key: segment separators hide the rest of the line.
		{"make build && rm -rf /tmp/x", "", false},
		{"ls; curl evil.example", "", false},
		{"cat a | sh", "", false},
		{"make build & disown", "", false},
		// No key: dynamic construction hides the real target.
		{"make $(cat target)", "", false},
		{"echo `whoami`", "", false},
		{"make ${TARGET}", "", false},
		{"eval make build", "", false},
		// No key: redirection writes somewhere the key does not name.
		{"make build > /etc/passwd", "", false},
		{"cat x >> y", "", false},
		{"cat < input", "", false},
		// No key: the head names a file whose contents can change
		// under a key that says nothing about them.
		{"./deploy.sh", "", false},
		{"/usr/local/bin/make build", "", false},
		{"scripts/run.sh", "", false},
		// No key: an environment assignment prefix.
		{"FOO=bar make build", "", false},
		// No key: nothing to key on.
		{"", "", false},
		{"   ", "", false},
	} {
		got, ok := CommandKey(tc.command)
		if ok != tc.ok || got != tc.want {
			t.Errorf("CommandKey(%q) = (%q, %v), want (%q, %v)",
				tc.command, got, ok, tc.want, tc.ok)
		}
	}
}

// A word merely containing "eval" is not the shell builtin.
func TestCommandKeyEvalIsAWordNotASubstring(t *testing.T) {
	if got, ok := CommandKey("cat evaluation.txt"); !ok || got != "cat" {
		t.Errorf("CommandKey = (%q, %v), want (\"cat\", true)", got, ok)
	}
}

// ADR-0045 §4: the per-command table decides only where the tool policy
// leaves room, and the tighter of the two always wins.
func TestForCallCombinesTighter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tools    map[string]string
		commands map[string]string
		command  string
		want     Decision
	}{
		{"learned never applies under a default tool policy",
			nil, map[string]string{"go test": "never"}, "go test ./...", NeverAsk},
		{"learned never cannot undo an operator's always",
			map[string]string{"shell_exec": "always"},
			map[string]string{"go test": "never"}, "go test ./...", AlwaysAsk},
		{"learned always tightens a blanket never",
			map[string]string{"shell_exec": "never"},
			map[string]string{"git push": "always"}, "git push origin main", AlwaysAsk},
		{"an unlisted command keeps the tool policy",
			map[string]string{"shell_exec": "never"},
			map[string]string{"go test": "always"}, "make build", NeverAsk},
		{"a command with no derivable key matches nothing",
			nil, map[string]string{"go test": "never"}, "go test && rm -rf x", Default},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := Build(tc.tools, nil, tc.commands, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.ForCall("shell_exec", tc.command); got != tc.want {
				t.Errorf("ForCall = %v, want %v", got, tc.want)
			}
		})
	}
}

// A non-shell call has no command, so the tool policy answers alone.
func TestForCallWithoutACommand(t *testing.T) {
	p, _, err := Build(map[string]string{"mcp__srv__lookup": "never"}, nil,
		map[string]string{"go test": "always"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ForCall("mcp__srv__lookup", ""); got != NeverAsk {
		t.Errorf("ForCall = %v, want NeverAsk", got)
	}
}

// A stored key today's derivation would not produce could never match a
// live call, so it is rejected loudly instead of sitting in the file
// doing nothing.
func TestUnmatchableCommandKeyIsRejected(t *testing.T) {
	for _, key := range []string{"make build && x", "./deploy.sh", "ls -la", "go   test", ""} {
		if _, _, err := Build(nil, nil, map[string]string{key: "never"}, false); err == nil {
			t.Errorf("Build accepted an unmatchable command key %q", key)
		}
	}
}

func TestInvalidCommandDecisionIsRejected(t *testing.T) {
	_, _, err := Build(nil, nil, map[string]string{"go test": "sometimes"}, false)
	if err == nil {
		t.Fatal("Build accepted an invalid command decision")
	}
}
