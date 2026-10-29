package report

import "testing"

func TestParseStat(t *testing.T) {
	tests := []struct {
		in   string
		want proc
		ok   bool
	}{
		{in: "42 (sleep) S 1 42 42 0 -1", want: proc{pid: 42, group: 42, session: 42}, ok: true},
		{in: "7 (a) b (c)) Z 1 5 6 0", want: proc{pid: 7, group: 5, session: 6, zombie: true}, ok: true},
		{in: "", ok: false},
		{in: "x (sleep) S 1 2 3", ok: false},
		{in: "1 (sleep) S 1", ok: false},
		{in: "1 (sleep) S 1 x 3", ok: false},
		{in: "1 sleep) S 1 2 3", ok: false},
	}
	for _, tt := range tests {
		got, ok := parseStat([]byte(tt.in))
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseStat(%q) = %+v, %v", tt.in, got, ok)
		}
	}
}
