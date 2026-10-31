package hosts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/output"
)

func fixLine(t *testing.T, err error) (msg, fix string) {
	t.Helper()
	var coded *output.Error
	if !errors.As(err, &coded) || coded.Code() != output.ExitServer {
		t.Fatalf("got %v, want exit 2", err)
	}
	return err.Error(), coded.Fix()
}

func TestSSHErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	key := filepath.Join(s.home, ".ssh", "id_ed25519")
	writeTestFile(t, key, "k", 0o600)
	h, _, err := s.Add(ctx, "vps1", "root@203.0.113.10", key, false)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := s.Add(ctx, "vps2", "root@203.0.113.11:2222", "", false)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		host   *Host
		stderr string
		msg    string
		fix    string
	}{
		{
			name: "unknown key", host: h, stderr: "No ED25519 host key is known for 203.0.113.10 and you have requested strict checking.\nHost key verification failed.\n",
			msg: "host vps1: host key not in known_hosts",
			fix: "ssh -i ~/.ssh/id_ed25519 root@203.0.113.10 once by hand, check the fingerprint, then rerun",
		},
		{
			name: "changed key", host: h2, stderr: "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\nHost key verification failed.\n",
			msg: "host vps2: host key changed",
			fix: "if the server was reinstalled, run ssh-keygen -R '[203.0.113.11]:2222', then ssh -p 2222 root@203.0.113.11 once by hand and check the fingerprint",
		},
		{
			name: "timeout", host: h, stderr: "ssh: connect to host 203.0.113.10 port 22: Operation timed out\n",
			msg: "host vps1: 203.0.113.10 did not answer",
			fix: "check that the server is up and the address is right, then rerun",
		},
		{
			name: "other", host: h2, stderr: "debug\nroot@203.0.113.11: Permission denied (publickey).\n",
			msg: "host vps2: ssh failed: root@203.0.113.11: Permission denied (publickey).",
			fix: "run ssh -p 2222 root@203.0.113.11 by hand to see what fails",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, fix := fixLine(t, tt.host.sshError(ctx, tt.stderr))
			if msg != tt.msg || fix != tt.fix {
				t.Fatalf("got\n%s\nfix: %s\nwant\n%s\nfix: %s", msg, fix, tt.msg, tt.fix)
			}
		})
	}
}

func TestDropped(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	h, _, err := s.Add(ctx, "vps1", "root@203.0.113.10", "", false)
	if err != nil {
		t.Fatal(err)
	}
	exited := func(code string) error {
		return exec.CommandContext(ctx, "sh", "-c", "exit "+code).Run()
	}
	for _, tt := range []struct {
		stderr, code string
		dropped      bool
	}{
		{stderr: "Timeout, server 203.0.113.10 not responding.\n", code: "255", dropped: true},
		{stderr: "client_loop: send disconnect: Broken pipe\n", code: "255", dropped: true},
		{stderr: "Read from remote host 203.0.113.10: Connection reset by peer\n", code: "255", dropped: true},
		{stderr: "ssh: connect to host 203.0.113.10 port 22: Operation timed out\n", code: "255", dropped: true},
		{stderr: "", code: "255", dropped: true},
		{stderr: "ssh: connect to host 203.0.113.10 port 22: Host is down\n", code: "255", dropped: true},
		// Setup problems fail the same way every time.
		{stderr: "Host key verification failed.\n", code: "255"},
		{stderr: "root@203.0.113.10: Permission denied (publickey).\n", code: "255"},
		// The remote command's own exit is no ssh failure at all.
		{stderr: "Connection reset", code: "3"},
	} {
		err := h.failure(ctx, sshTool, exited(tt.code), tt.stderr)
		fixLine(t, err)
		if Dropped(err) != tt.dropped {
			t.Errorf("exit %s %q: dropped %v, want %v", tt.code, tt.stderr, Dropped(err), tt.dropped)
		}
	}
	late, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.failure(late, sshTool, exited("255"), ""); Dropped(err) {
		t.Errorf("a call past its own deadline counts as dropped: %v", err)
	}
}

func TestRunRefused(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// Port 1 on loopback refuses at once; nothing listens there.
	h, _, err := s.Add(ctx, "local", "u@127.0.0.1:1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Run(ctx, nil, "true")
	if msg, _ := fixLine(t, err); !strings.Contains(msg, "host local: ssh failed") {
		t.Fatalf("msg = %q", msg)
	}
	_, err = h.Sync(ctx, &Tree{Root: t.TempDir()}, "x")
	if msg, _ := fixLine(t, err); !strings.HasPrefix(msg, "sync: host local:") {
		t.Fatalf("msg = %q", msg)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = h.Run(canceled, nil, "true")
	if msg, _ := fixLine(t, err); !strings.Contains(msg, "did not finish in time") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestCheckRsync(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	bin := t.TempDir()
	fake := filepath.Join(bin, "rsync")
	t.Setenv("PATH", bin)
	tests := []struct {
		script string
		ok     bool
	}{
		{script: "#!/bin/sh\necho 'rsync  version 3.4.1  protocol version 32'\n", ok: true},
		{script: "#!/bin/sh\necho 'openrsync: protocol version 29'\n"},
		{script: "#!/bin/sh\nexit 1\n"},
	}
	for _, tt := range tests {
		writeTestFile(t, fake, tt.script, 0o700)
		err := s.CheckRsync(ctx)
		if tt.ok != (err == nil) {
			t.Errorf("%q: got %v", tt.script, err)
		}
		if err != nil {
			fixLine(t, err)
		}
	}
}

func TestItemCounter(t *testing.T) {
	out := "*deleting   gone.txt\n*deleting   old dir/\n.d..t...... ./\n>f+++++++++ new file.txt\n" +
		"cd+++++++++ sub/\n>f.st...... sub/changed.go\ncL+++++++++ link -> target\n.f...p..... mode-only\n"
	for _, chunk := range []int{1, 7, len(out)} {
		c := &itemCounter{}
		for rest := out; rest != ""; {
			n := min(chunk, len(rest))
			if _, err := c.Write([]byte(rest[:n])); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		if c.n != 5 {
			t.Errorf("chunks of %d: counted %d, want 5", chunk, c.n)
		}
	}
}

func TestCapped(t *testing.T) {
	c := &capped{max: 4}
	for _, p := range []string{"ab", "cd", "ef"} {
		if n, err := c.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatal(n, err)
		}
	}
	if c.String() != "abcd" || !c.over {
		t.Fatalf("got %q over=%v", c.String(), c.over)
	}
}

func TestPrepareFails(t *testing.T) {
	s := newTestStore(t)
	// A file where the control folder should be.
	writeTestFile(t, filepath.Join(s.home, ".burro"), "x", 0o600)
	h := &Host{store: s, name: "x"}
	if _, err := h.Run(context.Background(), nil, "true"); err == nil {
		t.Fatal("Run passed without a control folder")
	}
	if _, err := h.Sync(context.Background(), &Tree{Root: "/"}, "x"); err == nil {
		t.Fatal("Sync passed without a control folder")
	}
	if err := os.Remove(filepath.Join(s.home, ".burro")); err != nil {
		t.Fatal(err)
	}
}

func TestRsyncPattern(t *testing.T) {
	for _, tc := range []struct{ path, suffix, want string }{
		{"debug.log", "", "/debug.log"},
		{"node_modules/", "", "/node_modules/"},
		{"a b /c ", "", "/a b /c "},
		// Without a wildcard rsync takes a backslash as is.
		{`d\e`, "", `/d\e`},
		{"a*b?[1]", "", `/a\*b\?\[1]`},
		{`d\e*`, "", `/d\\e\*`},
		{"odd\nname\r.log", "", "/odd[[:cntrl:]]name[[:cntrl:]].log"},
		{"build/", "**", "/build/**"},
		{`b\[x]/`, "**", `/b\\\[x]/**`},
	} {
		if got := rsyncPattern(tc.path, tc.suffix); got != tc.want {
			t.Errorf("rsyncPattern(%q, %q) = %q, want %q", tc.path, tc.suffix, got, tc.want)
		}
	}
}
