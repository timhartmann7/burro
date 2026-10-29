//go:build integration

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	v1       = "1.0.0"
	v2       = "1.0.1"
	appDir   = "burro/app"
	tomlHead = "host = \"vps1\"\npath = \"~/burro/app\"\n"
	hostTOML = tomlHead + "\n[pipelines]\ndefault = [\"test\"]\n\n[steps.test]\nrun = \"true\"\n"
)

// world is one server, a client HOME that reaches it and a project repository
// that is the working folder.
type world struct {
	t    *testing.T
	srv  *testutil.Server
	env  []string
	repo string
	bins map[string][]byte
}

func newWorld(t *testing.T, files map[string]string) *world {
	t.Helper()
	return newWorldOn(t, testutil.StartServer(t), files)
}

// newWorldOn is newWorld on the server srv.
func newWorldOn(t *testing.T, srv *testutil.Server, files map[string]string) *world {
	t.Helper()
	all := map[string]string{tomlFile: hostTOML}
	for k, v := range files {
		all[k] = v
	}
	w := &world{
		t:    t,
		srv:  srv,
		env:  []string{"HOME=" + srv.Home(), "PATH=" + os.Getenv("PATH")},
		repo: testutil.GitRepo(t, all),
		bins: map[string][]byte{},
	}
	t.Chdir(w.repo)
	return w
}

// builtBinaries builds the server binary of one version on demand.
type builtBinaries struct {
	w       *world
	version string
}

func (b builtBinaries) ForMachine(machine string) ([]byte, error) {
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[machine]
	if arch == "" {
		return nil, fmt.Errorf("test server reports machine %q", machine)
	}
	key := b.version + "/" + arch
	if b.w.bins[key] == nil {
		b.w.bins[key] = testutil.BuildServerBurro(b.w.t, b.version, arch)
	}
	return b.w.bins[key], nil
}

// burro runs one command in process, as client version, and reports it the
// way cmd/burro does.
func (w *world) burro(version string, args ...string) testutil.Result {
	w.t.Helper()
	var stdout, stderr strings.Builder
	code := w.burroTo(version, &stdout, &stderr, args...)
	return testutil.Result{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// burroTo is burro with the writers of the caller's choice.
func (w *world) burroTo(version string, stdout, stderr io.Writer, args ...string) int {
	a := New(strings.NewReader(""), stdout, stderr, w.env, version)
	a.binaries = builtBinaries{w: w, version: version}
	return output.Report(stderr, a.Run(args))
}

func (w *world) must(version string, args ...string) string {
	w.t.Helper()
	r := w.burro(version, args...)
	if r.Code != 0 {
		w.t.Fatalf("burro %q: exit %d\nstdout:\n%s\nstderr:\n%s", args, r.Code, r.Stdout, r.Stderr)
	}
	return r.Stdout
}

func (w *world) addHost(version string) string {
	w.t.Helper()
	return w.must(version, argHost, argAdd, "vps1", w.srv.Address(), "-i", w.srv.Key())
}

func (w *world) port() string {
	_, port, _ := strings.Cut(w.srv.Address(), ":")
	return port
}

func (w *world) hostsFile() string {
	data, err := os.ReadFile(filepath.Join(w.srv.Home(), ".ssh", "config.d", "burro"))
	if err != nil && !os.IsNotExist(err) {
		w.t.Fatal(err)
	}
	return string(data)
}

// closeMasters ends burro's shared connections, so the next call makes a
// fresh one and checks the host key again.
func (w *world) closeMasters() {
	sockets, _ := filepath.Glob(filepath.Join(w.srv.Home(), ".burro", "cm", "*"))
	for _, socket := range sockets {
		_ = exec.CommandContext(context.Background(), "ssh", "-o", "ControlPath="+socket, "-O", "exit", "x").Run()
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// acceptance: S1-01
func TestExecArguments(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	w.must(v1, argSync)
	got := w.must(v1, argExec, "--", "printf", `%s\n`, "a b", "$HOME", "\u00e9 \u00fc \U0001F642", `x"y`, "it's")
	if want := "a b\n$HOME\n\u00e9 \u00fc \U0001F642\nx\"y\nit's\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := w.must(v1, argExec, "pwd"); got != "/home/burro/"+appDir+"\n" {
		t.Fatalf("pwd = %q", got)
	}
	if got := w.must(v1, argExec, argJSON, "--", "echo", argJSON); got != `{"output":"--json\n","hidden":0,"log":".burro/exec/e3.log","exit":0}`+"\n" {
		t.Fatalf("json = %q", got)
	}
}

// acceptance: S1-02
func TestExecFile(t *testing.T) {
	script := "#!/usr/bin/env python3\n" +
		"# Quotes of every kind: ' \" ` ''' \"\"\" $(x) ${y} \\\n" +
		"import sys\n" +
		"print('single', \"double\", '''triple single''', \"\"\"triple \"double\" \"\"\", '`back`', \"it's\")\n" +
		"print(repr(sys.argv[1:]))\n"
	w := newWorld(t, map[string]string{"tools/check.py": script, "plain.sh": "echo \"$# [$1]\"\n"})
	w.addHost(v1)
	w.must(v1, argSync)
	got := w.must(v1, argExec, "--file", "tools/check.py", "two words")
	if want := "single double triple single triple \"double\"  `back` it's\n['two words']\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := w.must(v1, argExec, "--file", "plain.sh", "two words"); got != "1 [two words]\n" {
		t.Fatalf("no shebang: %q", got)
	}
	r := w.burro(v1, argExec, "--file", "../outside.sh")
	if r.Code != 4 {
		t.Fatalf("outside script: exit %d %s", r.Code, r.Stderr)
	}
}

// acceptance: S1-03
func TestUnreachableHost(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "burro-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	w := &world{t: t, env: []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, bins: map[string][]byte{}}
	start := time.Now()
	r := w.burro(v1, argHost, argAdd, "dead", "root@192.0.2.1")
	took := time.Since(start)
	if r.Code != 2 || took > 12*time.Second || !strings.Contains(r.Stderr, "\nfix: ") {
		t.Fatalf("exit %d after %s: %s", r.Code, took, r.Stderr)
	}
	if data, err := os.ReadFile(filepath.Join(home, ".ssh", "config.d", "burro")); err == nil && strings.Contains(string(data), "dead") {
		t.Fatal("block of an unreachable host kept")
	}
}

// acceptance: S1-04
func TestChangedHostKey(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	w.closeMasters()

	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.CommandContext(context.Background(), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub := strings.Fields(readFile(t, other+".pub"))
	wrong := fmt.Sprintf("[127.0.0.1]:%s %s %s\n", w.port(), pub[0], pub[1])
	writeFile(t, w.srv.KnownHosts(), wrong)

	for _, args := range [][]string{{argDoctor}, {argExec, "true"}, {argHost, argAdd, "vps2", w.srv.Address(), "-i", w.srv.Key()}} {
		r := w.burro(v1, args...)
		if r.Code != 2 || !strings.Contains(r.Stderr, "host key changed") || !strings.Contains(r.Stderr, "fix: ") {
			t.Fatalf("burro %q: exit %d %s", args, r.Code, r.Stderr)
		}
		if readFile(t, w.srv.KnownHosts()) != wrong {
			t.Fatal("known_hosts changed")
		}
	}
	if strings.Contains(w.hostsFile(), "vps2") {
		t.Fatal("block kept after a changed key")
	}
}

// acceptance: S1-05 S1-06
func TestUnknownHostKey(t *testing.T) {
	w := newWorld(t, nil)
	writeFile(t, w.srv.KnownHosts(), "")
	r := w.burro(v1, argHost, argAdd, "vps1", w.srv.Address(), "-i", w.srv.Key())
	wantErr := fmt.Sprintf("burro: host vps1: host key not in known_hosts\n"+
		"fix: ssh -i ~/.ssh/id_ed25519 -p %s burro@127.0.0.1 once by hand, check the fingerprint, then rerun\n", w.port())
	if r.Code != 2 || r.Stderr != wantErr || r.Stdout != "" {
		t.Fatalf("exit %d stderr %q, want %q", r.Code, r.Stderr, wantErr)
	}
	if strings.Contains(w.hostsFile(), "burro-host vps1") {
		t.Fatalf("block kept:\n%s", w.hostsFile())
	}
	if readFile(t, w.srv.KnownHosts()) != "" {
		t.Fatal("known_hosts changed")
	}

	// The user checks the fingerprint and accepts the key by hand.
	writeFile(t, w.srv.KnownHosts(), w.srv.HostKeyLine())
	w.addHost(v1)
	r = w.burro(v1, argHost, argAdd, "vps1", w.srv.Address(), "-i", w.srv.Key())
	if r.Code != 4 || !strings.Contains(r.Stderr, "already exists") {
		t.Fatalf("second add: exit %d %s", r.Code, r.Stderr)
	}
	w.must(v1, argHost, argAdd, "vps1", w.srv.Address(), "-i", w.srv.Key(), "--force")
	config := readFile(t, filepath.Join(w.srv.Home(), ".ssh", "config"))
	if strings.Count(config, "Include config.d/burro") != 1 {
		t.Fatalf("config:\n%s", config)
	}
}

// acceptance: S1-07
func TestReuseAlias(t *testing.T) {
	w := newWorld(t, nil)
	alias := w.srv.Alias()
	config := readFile(t, filepath.Join(w.srv.Home(), ".ssh", "config"))
	r := w.burro(v1, argHost, argAdd, alias, w.srv.Address())
	if r.Code != 4 || !strings.Contains(r.Stderr, "fix: reuse it with burro host add "+alias+" "+alias+", or pick another name\n") {
		t.Fatalf("exit %d %s", r.Code, r.Stderr)
	}
	if readFile(t, filepath.Join(w.srv.Home(), ".ssh", "config")) != config || w.hostsFile() != "" {
		t.Fatal("a refused add changed the ssh files")
	}

	out := w.must(v1, argHost, argAdd, alias, alias)
	if !strings.Contains(out, "burro   ok    "+v1+"\n") {
		t.Fatalf("doctor after add:\n%s", out)
	}
	if got := w.srv.Exec(t, ".burro/bin/burro", "--version"); !strings.HasPrefix(got, "burro "+v1+" linux/") {
		t.Fatalf("server binary: %q", got)
	}
	list := w.must(v1, argHost, "ls")
	if want := fmt.Sprintf("%s   burro@127.0.0.1:%s   %s\n", alias, w.port(), v1); list != want {
		t.Fatalf("host ls = %q, want %q", list, want)
	}
}

// listing returns path and content of every file under dir on the server,
// leaving out .burro.
func (w *world) serverFiles(dir string) map[string]string {
	w.t.Helper()
	out := w.srv.Exec(w.t, "sh", "-c", `cd "$1" && find . -path ./.burro -prune -o -type f -print0`, "sh", dir)
	files := map[string]string{}
	for _, name := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		files[strings.TrimPrefix(name, "./")] = w.srv.Exec(w.t, "cat", dir+"/"+name)
	}
	return files
}

// acceptance: S1-09
func TestSyncReflectsChanges(t *testing.T) {
	w := newWorld(t, map[string]string{
		gitignore:      ignoreLogs,
		"keep.txt":     "keep",
		"modify.txt":   "before",
		"delete.txt":   "bye",
		"old name.txt": "moved",
	})
	w.addHost(v1)
	out := w.must(v1, argSync)
	if !regexp.MustCompile(`^tree [0-9a-f]{7} · 6 files changed · \d+\.\ds\n$`).MatchString(out) {
		t.Fatalf("first sync: %q", out)
	}
	writeFile(t, filepath.Join(w.repo, "modify.txt"), "after")
	writeFile(t, filepath.Join(w.repo, "new uncommitted.txt"), "new")
	unicode := "file \u00e9 \U0001F642.txt"
	writeFile(t, filepath.Join(w.repo, "dir with space", unicode), "unicode")
	writeFile(t, filepath.Join(w.repo, "debug.log"), "ignored")
	if err := os.Remove(filepath.Join(w.repo, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, w.repo, "mv", "old name.txt", "new name.txt")

	out = w.must(v1, argSync, argJSON)
	hash := w.srv.Exec(t, "cat", appDir+"/.burro/tree")
	if len(hash) != 40 || !strings.HasPrefix(out, `{"tree":"`+hash[:7]+`","changed":6,"seconds":`) {
		t.Fatalf("sync json %q, tree file %q", out, hash)
	}
	want := map[string]string{
		gitignore:             ignoreLogs,
		"keep.txt":            "keep",
		"modify.txt":          "after",
		"new name.txt":        "moved",
		"new uncommitted.txt": "new",
		"dir with space/file \u00e9 \U0001F642.txt": "unicode",
		tomlFile: strings.TrimSuffix(hostTOML, "\n"),
	}
	got := w.serverFiles(appDir)
	if len(got) != len(want) {
		t.Fatalf("server has %v, want %v", got, want)
	}
	for name, content := range want {
		if strings.TrimSuffix(got[name], "\n") != strings.TrimSuffix(content, "\n") {
			t.Errorf("%s: server has %q, want %q", name, got[name], content)
		}
	}
	if out := w.must(v1, argSync); !strings.Contains(out, " · 0 files changed · ") {
		t.Fatalf("repeat sync: %q", out)
	}
}

// acceptance: S1-10
func TestSyncLeavesServerFilesAlone(t *testing.T) {
	w := newWorld(t, map[string]string{gitignore: "node_modules/\n", "index.js": "x"})
	writeFile(t, filepath.Join(w.repo, ".burro", "local-only"), "local")
	w.addHost(v1)
	w.must(v1, argSync)
	w.srv.Exec(t, "sh", "-c", `mkdir -p burro/app/node_modules/pkg && cd burro/app/node_modules/pkg && seq -f 'f%g' 1 50000 | xargs touch`)
	w.srv.Exec(t, "sh", "-c", `echo server > burro/app/.burro/server-only`)

	w.must(v1, argSync)
	if n := w.srv.Exec(t, "sh", "-c", "find burro/app/node_modules -type f | wc -l"); strings.TrimSpace(n) != "50000" {
		t.Fatalf("node_modules has %s files after sync", n)
	}
	if out := w.srv.Exec(t, "sh", "-c", "ls -A burro/app; ls -A burro/app/.burro"); strings.Contains(out, ".git\n") ||
		strings.Contains(out, "local-only") || !strings.Contains(out, "server-only") {
		t.Fatalf("server folder:\n%s", out)
	}
}

// acceptance: S1-12
func TestRepeatSyncIsFast(t *testing.T) {
	files := map[string]string{gitignore: ignoreLogs}
	for i := range 10000 {
		files[fmt.Sprintf("src/d%02d/f%05d.txt", i%100, i)] = fmt.Sprintf("file %d\n", i)
	}
	w := newWorld(t, files)
	w.addHost(v1)
	w.must(v1, argSync)
	w.srv.SetLatency(t, 40*time.Millisecond)
	start := time.Now()
	out := w.must(v1, argSync)
	took := time.Since(start)
	t.Logf("repeat sync of 10,000 files with 80 ms round trip: %s", took)
	if took > 2*time.Second || !strings.Contains(out, " · 0 files changed · ") {
		t.Fatalf("repeat sync took %s: %q", took, out)
	}
}

// acceptance: S1-13
func TestExecCut(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	w.must(v1, argSync)
	r := w.burro(v1, argExec, argMaxLines, "5", "--", "sh", "-c", "seq 1 50; exit 7")
	want := "1\n2\n3\n4\n5\n+45 lines hidden · full output: burro exec -- tail -n 45 .burro/exec/e1.log\nexit 7\n"
	if r.Code != 1 || r.Stdout != want || r.Stderr != "" {
		t.Fatalf("exit %d stdout %q stderr %q", r.Code, r.Stdout, r.Stderr)
	}
	_, hint, _ := strings.Cut(strings.Split(r.Stdout, "\n")[5], "full output: burro ")
	rest := w.must(v1, strings.Fields(hint)...)
	var hidden strings.Builder
	for i := 6; i <= 50; i++ {
		fmt.Fprintf(&hidden, "%d\n", i)
	}
	if rest != hidden.String() {
		t.Fatalf("hint printed %q", rest)
	}
	if r := w.burro(v1, argExec, "--", "sh", "-c", "echo out; exit 1"); r.Code != 1 || r.Stdout != "out\n" {
		t.Fatalf("exit 1: %d %q", r.Code, r.Stdout)
	}
}

// acceptance: S1-14
func TestTerminalModes(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v2)
	bin := testutil.BuildBurro(t, "-ldflags", "-X main.version="+v2)
	tests := []struct {
		name  string
		env   []string
		args  []string
		color bool
	}{
		{name: "override beats the agent variable", env: []string{"CLAUDECODE=1"}, args: []string{argDoctor, argMode, humanMode}, color: true},
		{name: "agent variable on a terminal", env: []string{"CLAUDECODE=1"}, args: []string{argDoctor}},
		{name: "plain terminal", args: []string{argDoctor}, color: true},
		{name: "NO_COLOR on a terminal", env: []string{"NO_COLOR=1"}, args: []string{argDoctor}},
		{name: "BURRO_MODE=agent on a terminal", env: []string{"BURRO_MODE=agent"}, args: []string{argDoctor}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := testutil.RunBurroTTY(t, bin, w.repo, append(append([]string{}, w.env...), tt.env...), tt.args...)
			if !strings.Contains(testutil.StripANSI(out), "burro   ok    "+v2) {
				t.Fatalf("doctor on a terminal:\n%q", out)
			}
			if colored := strings.Contains(out, "\x1b[32mok\x1b[0m"); colored != tt.color {
				t.Fatalf("color %v, want %v:\n%q", colored, tt.color, out)
			}
		})
	}
	// The same binary through a pipe is in agent mode.
	if r := testutil.RunBurroIn(t, bin, w.repo, w.env, argDoctor); r.Code != 0 || testutil.HasANSI(r.Stdout+r.Stderr) {
		t.Fatalf("doctor through a pipe: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
}

var roundTrip = regexp.MustCompile(`\d+ms round trip`)

// freeDisk is the disk line's number, which moves while other tests write.
var freeDisk = regexp.MustCompile(`\d+\.\d GB free`)

// acceptance: S1-15
func TestModesPrintSameText(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	w.must(v1, argSync)
	calls := []struct{ cmd, rest []string }{
		{cmd: []string{argDoctor}},
		{cmd: []string{argHost, "ls"}},
		{cmd: []string{argExec}, rest: []string{"echo", "hi"}},
		{cmd: []string{argSync}},
		{cmd: []string{argExec}, rest: []string{"sh", "-c", "exit 3"}},
	}
	for _, c := range calls {
		// Flags go before the command's own arguments, which exec passes on.
		in := func(m string) []string { return slices.Concat(c.cmd, []string{argMode, m}, c.rest) }
		args := in(agentMode)
		agent := w.burro(v1, args...)
		human := w.burro(v1, in(humanMode)...)
		w.env = append(w.env, "NO_COLOR=1")
		noColor := w.burro(v1, in(humanMode)...)
		w.env = w.env[:len(w.env)-1]
		for name, r := range map[string]testutil.Result{agentMode: agent, "NO_COLOR": noColor} {
			if testutil.HasANSI(r.Stdout + r.Stderr) {
				t.Errorf("%q in %s mode printed ANSI codes: %q %q", args, name, r.Stdout, r.Stderr)
			}
		}
		mask := func(s string) string {
			s = roundTrip.ReplaceAllString(testutil.StripANSI(s), "Nms round trip")
			s = freeDisk.ReplaceAllString(s, "N GB free")
			return regexp.MustCompile(`\d+\.\ds\n`).ReplaceAllString(s, "N.Ns\n")
		}
		if mask(human.Stdout) != mask(agent.Stdout) || human.Code != agent.Code {
			t.Errorf("%q: human and agent differ:\n%q\n%q", args, human.Stdout, agent.Stdout)
		}
	}
	if r := w.burro(v1, argDoctor, argMode, humanMode); !testutil.HasANSI(r.Stdout) {
		t.Errorf("human doctor without color: %q", r.Stdout)
	}
	if r := w.burro(v1, argDoctor, "nosuch", argMode, humanMode); r.Code != 4 || testutil.HasANSI(r.Stderr) {
		t.Errorf("error in human mode: %d %q", r.Code, r.Stderr)
	}
}

// acceptance: S1-16
func TestDoctor(t *testing.T) {
	w := newWorld(t, nil)
	out := w.addHost(v1)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	names := []string{"ssh", "rsync", "bash", "docker", "disk", "burro"}
	if len(lines) != len(names) {
		t.Fatalf("doctor:\n%s", out)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, fmt.Sprintf("%-8sok    ", names[i])) {
			t.Errorf("line %d: %q", i, l)
		}
	}
	if r := w.burro(v1, argDoctor, argJSON); r.Code != 0 || !strings.HasPrefix(r.Stdout, `{"checks":[{"name":"ssh","ok":true,`) {
		t.Fatalf("doctor --json: %d %q", r.Code, r.Stdout)
	}

	w.srv.Root(t, "sh", "-c", "rm -f $(command -v rsync)")
	r := w.burro(v1, argDoctor, "vps1")
	if r.Code != 2 || r.Stderr != "" {
		t.Fatalf("doctor without rsync: exit %d stderr %q", r.Code, r.Stderr)
	}
	if want := "rsync   FAIL  not found · install rsync on the server, for example apt install rsync\n"; !strings.Contains(r.Stdout, want) {
		t.Fatalf("doctor without rsync:\n%s", r.Stdout)
	}
	if strings.Count(r.Stdout, "FAIL") != 1 {
		t.Fatalf("other checks failed too:\n%s", r.Stdout)
	}
}

// acceptance: S1-17
func TestServerBinaryFollowsClient(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	if out := w.must(v1, argHost, "ls"); !strings.HasSuffix(out, "   "+v1+"\n") {
		t.Fatalf("host ls: %q", out)
	}
	w.must(v2, argDoctor)
	if out := w.must(v2, argHost, "ls"); !strings.HasSuffix(out, "   "+v2+"\n") {
		t.Fatalf("host ls after doctor: %q", out)
	}
	w.must(v1, argSync)
	w.must(v2, argExec, "true")
	if got := w.srv.Exec(t, ".burro/bin/burro", "--version"); !strings.HasPrefix(got, "burro "+v2+" ") {
		t.Fatalf("server binary %q", got)
	}
	if mode := w.srv.Exec(t, "stat", "-c", "%a", ".burro/bin", ".burro/bin/burro"); mode != "700\n700" {
		t.Fatalf("modes %q", mode)
	}
	w.srv.Exec(t, "rm", ".burro/bin/burro")
	if out := w.must(v2, argHost, "ls"); !strings.HasSuffix(out, "   not installed\n") {
		t.Fatalf("host ls without binary: %q", out)
	}
	if out := w.must(v2, argHost, "ls", argJSON); !strings.Contains(out, `"version":"not installed"`) {
		t.Fatalf("host ls json: %q", out)
	}
}

func TestMissingServerBinary(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	w.must(v1, argSync)
	var stdout, stderr strings.Builder
	a := New(strings.NewReader(""), &stdout, io.Discard, w.env, v2)
	a.binaries = missingBinaries{}
	if code := output.Report(&stderr, a.Run([]string{argExec, "true"})); code != 2 ||
		!strings.Contains(stderr.String(), "no server binary") || !strings.Contains(stderr.String(), "\nfix: ") {
		t.Fatalf("exit %d %q", code, stderr.String())
	}
	stdout.Reset()
	if code := output.Report(&stderr, a.Run([]string{argDoctor})); code != 2 ||
		!strings.Contains(stdout.String(), "burro   FAIL  host vps1: no server binary") {
		t.Fatalf("doctor: exit %d %q", code, stdout.String())
	}
}

type missingBinaries struct{}

func (missingBinaries) ForMachine(string) ([]byte, error) {
	return nil, output.NewError(output.ExitServer, "no server binary in this build", "install a release build")
}

func TestExecWithoutSync(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	r := w.burro(v1, argExec, "true")
	if r.Code != 2 || r.Stderr != "burro: host vps1: project folder ~/burro/app does not exist on the server\nfix: run burro sync first\n" {
		t.Fatalf("exit %d %q", r.Code, r.Stderr)
	}
}

func TestBrokenExecutor(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	w.must(v1, argSync)
	// A server binary that claims the right version but answers nonsense.
	w.srv.Exec(t, "sh", "-c", `printf '#!/bin/sh\nif [ "$1" = --version ]; then echo "burro `+v1+` linux/x"; else echo nonsense; fi\n' > .burro/bin/burro`)
	r := w.burro(v1, argExec, "true")
	if r.Code != 2 || !strings.Contains(r.Stderr, "broken reply") {
		t.Fatalf("exit %d %q", r.Code, r.Stderr)
	}
	w.srv.Exec(t, "sh", "-c", `printf '#!/bin/sh\nif [ "$1" = --version ]; then echo "burro `+v1+` linux/x"; else exit 3; fi\n' > .burro/bin/burro`)
	r = w.burro(v1, argExec, "true")
	if r.Code != 2 || !strings.Contains(r.Stderr, "failed with exit 3") {
		t.Fatalf("exit %d %q", r.Code, r.Stderr)
	}
}

func TestSyncTimeouts(t *testing.T) {
	w := newWorld(t, nil)
	w.addHost(v1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store, err := (&App{env: w.env}).store()
	if err != nil {
		t.Fatal(err)
	}
	h, err := store.Lookup("vps1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Sync(ctx, w.repo, appDir, ""); err == nil {
		t.Fatal("sync with a canceled context passed")
	}
}
