package hosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/timhartmann7/burro/internal/output"
)

const (
	// maxReply caps what one ssh call may print. Executor replies stay under
	// 1 MiB; anything bigger means something is wrong on the server.
	maxReply = 1 << 20
	// maxStderr keeps enough of ssh's stderr to classify a failure.
	maxStderr = 64 << 10
	// sshFailed is the exit status ssh uses for its own errors.
	sshFailed = 255
	// waitDelay bounds how long a finished process may hold its pipes open.
	waitDelay = 5 * time.Second
	sshTool   = "ssh"
)

// Host is one server from the hosts file.
type Host struct {
	store   *Store
	name    string
	alias   bool
	address string
	key     string
}

// Name is the host name, which is also its ssh alias.
func (h *Host) Name() string {
	return h.name
}

// Address is user@host[:port]. For an alias it is what ssh resolves the
// alias to, or the alias itself when ssh can't resolve it.
func (h *Host) Address(ctx context.Context) string {
	if !h.alias {
		return h.address
	}
	u, host, port, err := h.resolved(ctx)
	if err != nil {
		return h.name
	}
	return joinAddress(u, host, port)
}

// Run runs argv on the host and returns its stdout. stdin may be nil. A
// non-zero exit status is an error with exit code 2.
func (h *Host) Run(ctx context.Context, stdin io.Reader, argv ...string) ([]byte, error) {
	stdout := &capped{max: maxReply}
	if err := h.Stream(ctx, stdin, stdout, argv...); err != nil {
		return nil, err
	}
	if stdout.over {
		return nil, output.NewError(output.ExitServer,
			fmt.Sprintf("host %s: reply is over %d bytes", h.name, maxReply),
			"run burro doctor "+h.name)
	}
	return stdout.buf, nil
}

// Stream runs argv on the host and writes its stdout to w as it arrives.
// Errors are those of Run.
func (h *Host) Stream(ctx context.Context, stdin io.Reader, w io.Writer, argv ...string) error {
	if err := h.store.prepare(); err != nil {
		return err
	}
	stderr := &capped{max: maxStderr}
	cmd := h.store.command(ctx, sshTool, append(h.store.sshOptions(), h.name, commandLine(argv)))
	cmd.Stdin = stdin
	cmd.Stdout = w
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return h.failure(ctx, sshTool, err, stderr.String())
	}
	return nil
}

// failure turns a failed ssh or rsync call into an error with exit code 2
// and a fix line for the cause.
func (h *Host) failure(ctx context.Context, tool string, err error, stderr string) error {
	if ctx.Err() != nil {
		return output.NewError(output.ExitServer,
			fmt.Sprintf("host %s: %s did not finish in time", h.name, tool),
			"run burro doctor "+h.name)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return output.NewError(output.ExitServer, fmt.Sprintf("%s: %v", tool, err),
			"install OpenSSH and rsync on this machine")
	}
	code := exit.ExitCode()
	// rsync reports a failed ssh as 255 or as a broken protocol stream.
	if code == sshFailed || (tool == "rsync" && code == 12) {
		failed := h.sshError(ctx, stderr)
		// ssh names every setup problem; a session cut off from the other
		// end may end without a word.
		dropped := strings.TrimSpace(stderr) == ""
		for _, mark := range droppedMarks() {
			dropped = dropped || strings.Contains(stderr, mark)
		}
		if dropped {
			return &droppedError{failed}
		}
		return failed
	}
	what := tool
	if tool == sshTool {
		what = "remote command"
	}
	return output.NewError(output.ExitServer,
		fmt.Sprintf("host %s: %s failed with exit %d: %s", h.name, what, code, lastLine(stderr)),
		"run burro doctor "+h.name)
}

// droppedMarks are what ssh prints when the network, not the setup, failed:
// a connection that broke or a server that didn't answer for a while.
func droppedMarks() []string {
	return []string{
		"timed out", "not responding", "Connection reset", "Connection closed",
		"closed by remote host", "Broken pipe", "No route to host", "Network is unreachable",
		"Host is down", "Connection refused",
	}
}

// droppedError is an ssh failure that trying again later may get past.
type droppedError struct {
	err *output.Error
}

func (d *droppedError) Error() string { return d.err.Error() }

func (d *droppedError) Unwrap() error { return d.err }

// Dropped tells whether err is a connection that broke or a server that
// didn't answer, as opposed to a setup problem such as a host key or a
// key that ssh refused, which no retry fixes.
func Dropped(err error) bool {
	var d *droppedError
	return errors.As(err, &d)
}

// sshError explains why ssh failed. Fix lines name the address, because the
// user runs them by hand.
func (h *Host) sshError(ctx context.Context, stderr string) *output.Error {
	u, host, port, err := h.resolved(ctx)
	if err != nil {
		u, host, port = "", h.name, ""
	}
	manual := h.manual(u, host, port)
	switch {
	case strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		target := host
		if port != "" && port != "22" {
			target = fmt.Sprintf("'[%s]:%s'", host, port)
		}
		return output.NewError(output.ExitServer, fmt.Sprintf("host %s: host key changed", h.name),
			fmt.Sprintf("if the server was reinstalled, run ssh-keygen -R %s, then %s once by hand and check the fingerprint", target, manual))
	case strings.Contains(stderr, "Host key verification failed"), strings.Contains(stderr, "host key is known"):
		return output.NewError(output.ExitServer, fmt.Sprintf("host %s: host key not in known_hosts", h.name),
			manual+" once by hand, check the fingerprint, then rerun")
	case strings.Contains(stderr, "timed out"), strings.Contains(stderr, "No route to host"),
		strings.Contains(stderr, "Network is unreachable"):
		return output.NewError(output.ExitServer, fmt.Sprintf("host %s: %s did not answer", h.name, host),
			"check that the server is up and the address is right, then rerun")
	default:
		return output.NewError(output.ExitServer, fmt.Sprintf("host %s: ssh failed: %s", h.name, lastLine(stderr)),
			"run "+manual+" by hand to see what fails")
	}
}

// manual is the ssh command a user types to reach the host by address.
func (h *Host) manual(u, host, port string) string {
	cmd := sshTool
	if h.key != "" {
		key := Quote(h.key)
		if rel, ok := strings.CutPrefix(h.key, h.store.home+string(filepath.Separator)); ok {
			// ~ must stay outside the quotes to expand.
			key = "~/" + Quote(rel)
		}
		cmd += " -i " + key
	}
	if port != "" && port != "22" {
		cmd += " -p " + port
	}
	if u != "" {
		host = u + "@" + host
	}
	return cmd + " " + host
}

// resolved asks ssh what the host name resolves to.
func (h *Host) resolved(ctx context.Context) (u, host, port string, err error) {
	if !h.alias {
		u, host, port = h.storedParts()
		return u, host, port, nil
	}
	lines, err := h.store.resolve(ctx, h.name)
	if err != nil {
		return "", "", "", err
	}
	for _, l := range lines {
		key, value, _ := strings.Cut(l, " ")
		switch key {
		case "user":
			u = value
		case "hostname":
			host = value
		case "port":
			port = value
		}
	}
	return u, host, port, nil
}

func (h *Host) storedParts() (u, host, port string) {
	u, addr, _ := strings.Cut(h.address, "@")
	if rest, ok := strings.CutPrefix(addr, "["); ok {
		host, rest, _ = strings.Cut(rest, "]")
		return u, host, strings.TrimPrefix(rest, ":")
	}
	host, port, _ = strings.Cut(addr, ":")
	return u, host, port
}

// Tree is what Sync copies from this machine.
type Tree struct {
	// Root is the local top folder.
	Root string
	// Exclude is .git/info/exclude, or empty.
	Exclude string
	// Ignored are the paths git leaves out of the tree, folders with a
	// trailing slash. They are not sent and stay on the server.
	Ignored []string
	// Kept are ignored folders that hold tracked files. The tracked files
	// are sent; everything else in them stays on the server.
	Kept []string
}

// Sync copies the tree to dir on the host with rsync and returns how many
// files it changed. Git's own lists decide what is sent, because rsync reads
// .gitignore differently: no negation, no tracked files that match a rule.
// .gitignore and .git/info/exclude still protect what the server built.
func (h *Host) Sync(ctx context.Context, t *Tree, dir string) (int, error) {
	if err := h.store.prepare(); err != nil {
		return 0, err
	}
	// Incremental recursion waits a round trip per batch of folders, which
	// doubles a no-change sync of a large tree on a distant server.
	// Temp files go to .burro/tmp, which --delete never touches, so two
	// syncs at once can't delete each other's files.
	args := []string{
		"-az", "--no-inc-recursive", "--delete", "--itemize-changes", "--temp-dir=.burro/tmp",
		"--filter=merge -", "--filter=:-r .gitignore",
	}
	if t.Exclude != "" {
		args = append(args, "--filter=merge,-r "+t.Exclude)
	}
	args = append(args, "--exclude=/.git/", "--exclude=/.burro/",
		"-e", commandLine(append([]string{sshTool}, h.store.sshOptions()...)),
		t.Root+"/", h.name+":"+dir+"/")
	var rules strings.Builder
	for _, path := range t.Ignored {
		rules.WriteString("- " + rsyncPattern(path, "") + "\n")
	}
	for _, folder := range t.Kept {
		rules.WriteString("P " + rsyncPattern(folder, "**") + "\n")
	}
	counter := &itemCounter{}
	stderr := &capped{max: maxStderr}
	cmd := h.store.command(ctx, "rsync", args)
	cmd.Stdin = strings.NewReader(rules.String())
	cmd.Stdout = counter
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("sync: %w", h.failure(ctx, "rsync", err, stderr.String()))
	}
	return counter.n, nil
}

// Pull copies files, paths relative to dir on the host, into the local
// folder to. The names travel NUL-separated on stdin, so any file name
// arrives as itself, and rsync sends nothing but them.
func (h *Host) Pull(ctx context.Context, dir string, files []string, to string) error {
	if err := h.store.prepare(); err != nil {
		return err
	}
	args := []string{
		"-a", "--from0", "--files-from=-",
		"-e", commandLine(append([]string{sshTool}, h.store.sshOptions()...)),
		h.name + ":" + dir + "/", to + "/",
	}
	stderr := &capped{max: maxStderr}
	cmd := h.store.command(ctx, "rsync", args)
	cmd.Stdin = strings.NewReader(strings.Join(files, "\x00"))
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pull: %w", h.failure(ctx, "rsync", err, stderr.String()))
	}
	return nil
}

// rsyncPattern matches path, relative to the transfer root, and nothing
// else, followed by suffix. rsync reads backslashes as escapes only in a
// pattern with wildcards, and a newline or carriage return would end the
// rule, so those match as a control character class.
func rsyncPattern(path, suffix string) string {
	if !strings.ContainsAny(path+suffix, "*?[\n\r") {
		return "/" + path + suffix
	}
	escape := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, "\n", "[[:cntrl:]]", "\r", "[[:cntrl:]]")
	return "/" + escape.Replace(path) + suffix
}

// CheckRsync fails when the local rsync can't keep ignored files on the
// server. openrsync, the rsync of recent macOS, ignores the .gitignore rules
// on the receiving side and deletes them.
func (s *Store) CheckRsync(ctx context.Context) error {
	out, err := s.command(ctx, "rsync", []string{"--version"}).Output()
	if err != nil {
		return output.NewError(output.ExitServer, fmt.Sprintf("rsync on this machine: %v", err),
			"install rsync, for example brew install rsync")
	}
	if strings.Contains(firstLine(string(out)), "openrsync") {
		return output.NewError(output.ExitServer,
			"rsync on this machine is openrsync, which deletes ignored files on the server",
			"install rsync with brew install rsync, then rerun")
	}
	return nil
}

// resolve returns the config ssh applies to name, one "key value" per line.
func (s *Store) resolve(ctx context.Context, name string) ([]string, error) {
	out, err := s.command(ctx, sshTool, append(s.configOption(), "-G", name)).Output()
	if err != nil {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("ssh -G %s: %v", name, err),
			"check your ssh config with ssh -G "+name)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}

// command prepares ssh or rsync with the store's environment. Arguments go
// as a slice, never through a local shell.
func (s *Store) command(ctx context.Context, tool string, args []string) *exec.Cmd {
	//nolint:gosec // G204: tool is ssh or rsync and args is an argument slice; remote words pass through commandLine.
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = s.env
	cmd.WaitDelay = waitDelay
	return cmd
}

// sshOptions go on every ssh call, never into the user's config.
func (s *Store) sshOptions() []string {
	return append(s.configOption(),
		"-o", "ControlMaster=auto",
		"-o", "ControlPath="+filepath.Join(s.controlDir(), "%C"),
		"-o", "ControlPersist=10m",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=10",
		// A connection that stops answering ends within 30 seconds instead
		// of holding a call until its own deadline.
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
	)
}

func (s *Store) configOption() []string {
	if s.userConfig == "" {
		return nil
	}
	if _, err := os.Stat(s.userConfig); err != nil {
		// Without a config file there is nothing to read, and the passwd
		// home must not be read instead.
		return []string{"-F", os.DevNull}
	}
	return []string{"-F", s.userConfig}
}

func (s *Store) controlDir() string {
	return filepath.Join(s.home, ".burro", "cm")
}

// prepare creates the folder for control sockets.
func (s *Store) prepare() error {
	if err := os.MkdirAll(s.controlDir(), 0o700); err != nil {
		return fileError("create", s.controlDir(), err)
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// capped keeps the first max bytes written to it and notes whether more came.
type capped struct {
	max  int
	buf  []byte
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - len(c.buf)
	if len(p) > room {
		c.over = true
		c.buf = append(c.buf, p[:max(room, 0)]...)
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *capped) String() string {
	return string(c.buf)
}

// itemCounter counts the files in rsync's --itemize-changes output: sent,
// changed or deleted files and links, not folders.
type itemCounter struct {
	partial []byte
	n       int
}

func (c *itemCounter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			c.partial = append(c.partial, p...)
			return n, nil
		}
		c.count(string(c.partial) + string(p[:i]))
		c.partial = c.partial[:0]
		p = p[i+1:]
	}
}

func (c *itemCounter) count(line string) {
	if name, ok := strings.CutPrefix(line, "*deleting "); ok {
		if !strings.HasSuffix(strings.TrimSpace(name), "/") {
			c.n++
		}
		return
	}
	// An item line is an 11-character change code, a space and the name.
	if len(line) > 12 && line[11] == ' ' && line[1] != 'd' {
		c.n++
	}
}
