package mode_test

import (
	"testing"

	"github.com/timhartmann7/burro/internal/mode"
)

const (
	agentMode  = "agent"
	humanMode  = "human"
	varClaude  = "CLAUDECODE"
	varMode    = "BURRO_MODE"
	varNoColor = "NO_COLOR"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// acceptance: S1-14
func TestModeRules(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		vars     map[string]string
		terminal bool
		color    bool
	}{
		{name: "rule 1: --mode human beats an agent variable", flag: humanMode, vars: map[string]string{varClaude: "1"}, terminal: true, color: true},
		{name: "rule 1: --mode human off a terminal", flag: humanMode, color: true},
		{name: "rule 1: --mode agent on a terminal", flag: agentMode, terminal: true},
		{name: "rule 1: --mode agent beats BURRO_MODE", flag: agentMode, vars: map[string]string{varMode: humanMode}, terminal: true},
		{name: "rule 2: BURRO_MODE=human beats an agent variable", vars: map[string]string{varMode: humanMode, "CODEX_SANDBOX": "seatbelt"}, color: true},
		{name: "rule 2: BURRO_MODE=agent on a terminal", vars: map[string]string{varMode: agentMode}, terminal: true},
		{name: "rule 2: other BURRO_MODE values fall through", vars: map[string]string{varMode: "robot"}, terminal: true, color: true},
		{name: "rule 3: AGENT", vars: map[string]string{"AGENT": "amp"}, terminal: true},
		{name: "rule 3: AI_AGENT", vars: map[string]string{"AI_AGENT": "1"}, terminal: true},
		{name: "rule 3: CLAUDECODE", vars: map[string]string{varClaude: "1"}, terminal: true},
		{name: "rule 3: CODEX_SANDBOX", vars: map[string]string{"CODEX_SANDBOX": "seatbelt"}, terminal: true},
		{name: "rule 3: CURSOR_AGENT", vars: map[string]string{"CURSOR_AGENT": "1"}, terminal: true},
		{name: "rule 3: an empty agent variable doesn't count", vars: map[string]string{varClaude: ""}, terminal: true, color: true},
		{name: "rule 4: stdout is not a terminal"},
		{name: "rule 5: a terminal and nothing else", terminal: true, color: true},
		{name: "human with NO_COLOR", flag: humanMode, vars: map[string]string{varNoColor: "1"}},
		{name: "human with TERM=dumb", terminal: true, vars: map[string]string{"TERM": "dumb"}},
		{name: "human with an empty NO_COLOR", terminal: true, vars: map[string]string{varNoColor: ""}, color: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := mode.New(tt.flag, env(tt.vars), tt.terminal)
			if err != nil {
				t.Fatal(err)
			}
			want := "ok"
			if tt.color {
				want = "\x1b[32mok\x1b[0m"
			}
			if got := m.OK("ok"); got != want {
				t.Fatalf("OK(ok) = %q, want %q", got, want)
			}
		})
	}
}

func TestModeColors(t *testing.T) {
	m, err := mode.New(humanMode, env(nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Fail("FAIL"); got != "\x1b[31mFAIL\x1b[0m" {
		t.Fatalf("Fail = %q", got)
	}
	agent, err := mode.New(agentMode, env(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.Fail("FAIL"); got != "FAIL" {
		t.Fatalf("agent Fail = %q", got)
	}
	if got := m.Skip("skip"); got != "\x1b[90mskip\x1b[0m" {
		t.Fatalf("Skip = %q", got)
	}
	if got := agent.Skip("skip"); got != "skip" {
		t.Fatalf("agent Skip = %q", got)
	}
}

// Without color a person still reads the call, so progress stays on.
func TestModeHuman(t *testing.T) {
	tests := []struct {
		flag     string
		vars     map[string]string
		terminal bool
		human    bool
	}{
		{flag: humanMode, vars: map[string]string{varNoColor: "1"}, human: true},
		{vars: map[string]string{"TERM": "dumb"}, terminal: true, human: true},
		{vars: map[string]string{varClaude: "1"}, terminal: true},
		{flag: agentMode, terminal: true},
		{},
	}
	for _, tt := range tests {
		m, err := mode.New(tt.flag, env(tt.vars), tt.terminal)
		if err != nil {
			t.Fatal(err)
		}
		if m.Human() != tt.human {
			t.Errorf("New(%q, %v, %v).Human() = %v", tt.flag, tt.vars, tt.terminal, m.Human())
		}
	}
}

func TestModeBadFlag(t *testing.T) {
	for _, flag := range []string{"Human", "robot", " agent", "agent\n"} {
		if _, err := mode.New(flag, env(nil), true); err == nil {
			t.Errorf("New(%q) accepted a bad mode", flag)
		}
	}
}
