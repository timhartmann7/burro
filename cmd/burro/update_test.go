package main_test

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/testutil"
)

const humanEnv = "BURRO_MODE=human"

// blackHole accepts connections and never answers, like a proxy on a dead
// network. It counts the connections.
func blackHole(t *testing.T) (addr string, accepts *atomic.Int32) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepts = &atomic.Int32{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String(), accepts
}

// acceptance: S4-10 S4-08
func TestVersionCheckNetwork(t *testing.T) {
	bin := testutil.BuildBurro(t, "-ldflags", "-X main.version=0.1.0")
	// The first start of a fresh binary can take a second on its own, as
	// macOS scans it; the timings below are about the network only.
	testutil.RunBurro(t, bin, "--version")
	proxy, accepts := blackHole(t)
	base := []string{"PATH=" + os.Getenv("PATH"), "HTTPS_PROXY=http://" + proxy}
	run := func(home string, extra ...string) (testutil.Result, time.Duration) {
		t.Helper()
		env := append(append([]string{"HOME=" + home}, base...), extra...)
		start := time.Now()
		r := testutil.RunBurroIn(t, bin, home, env, "host", "ls")
		return r, time.Since(start)
	}
	for _, extra := range [][]string{
		nil, // stdout is not a terminal: agent mode
		{"BURRO_MODE=agent"},
		{"CLAUDECODE=1"},
		{humanEnv, "CI=true"},
		{humanEnv, "DO_NOT_TRACK=1"},
		{humanEnv, "BURRO_NO_UPDATE_CHECK=1"},
	} {
		r, took := run(t.TempDir(), extra...)
		if r.Code != 0 || r.Stderr != "" || accepts.Load() != 0 || took > time.Second {
			t.Fatalf("env %q: exit %d stderr %q, %d proxy connections, took %s", extra, r.Code, r.Stderr, accepts.Load(), took)
		}
	}
	// A human gets the check: it reaches the dead proxy, gives up within
	// 2 seconds and says nothing.
	home := t.TempDir()
	r, took := run(home, humanEnv)
	if r.Code != 0 || r.Stderr != "" || accepts.Load() != 1 || took > 3*time.Second {
		t.Fatalf("human: exit %d stderr %q, %d proxy connections, took %s", r.Code, r.Stderr, accepts.Load(), took)
	}
	// The failed attempt counts for the day.
	if r, took := run(home, humanEnv); r.Code != 0 || accepts.Load() != 1 || took > time.Second {
		t.Fatalf("second human call: exit %d, %d proxy connections, took %s", r.Code, accepts.Load(), took)
	}
}
