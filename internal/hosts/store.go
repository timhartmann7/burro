// Package hosts keeps burro's Host blocks in ~/.ssh/config.d/burro and runs
// ssh and rsync against them.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/timhartmann7/burro/internal/output"
)

const (
	// marker starts every block burro owns in its hosts file.
	marker = "# burro-host "
	// includeLine pulls the hosts file into the user's ssh config.
	includeLine = "Include config.d/burro"
	// probeName never matches a Host pattern of a real config, so ssh -G for
	// it shows what an unknown name resolves to.
	probeName = "burro-unmatched-probe-host"
)

var (
	userPattern = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*$`)
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	ipv6Pattern = regexp.MustCompile(`^[0-9A-Fa-f:.]+$`)
)

// Store is the client side of burro's hosts: the hosts file, the Include
// line and the options every ssh call gets.
type Store struct {
	home string
	env  []string
	// userConfig is the ssh config passed with -F, or empty when ssh finds
	// the same file on its own.
	userConfig string
	rename     func(oldpath, newpath string) error
}

// NewStore returns the store for the client's home folder. env is the
// environment of every ssh and rsync call.
func NewStore(home string, env []string) (*Store, error) {
	if !filepath.IsAbs(home) {
		return nil, output.NewError(output.ExitServer, "HOME is not set to an absolute path",
			"set HOME to your home folder")
	}
	s := &Store{home: home, env: env, rename: os.Rename}
	// ssh finds ~/.ssh through the passwd entry, not HOME. When the two
	// differ, point ssh at the config burro edits.
	if u, err := user.Current(); err == nil && filepath.Clean(u.HomeDir) != filepath.Clean(home) {
		s.userConfig = s.configPath()
	}
	return s, nil
}

func (s *Store) configPath() string {
	return filepath.Join(s.home, ".ssh", "config")
}

func (s *Store) hostsPath() string {
	return filepath.Join(s.home, ".ssh", "config.d", "burro")
}

// Lookup returns the host called name from the hosts file.
func (s *Store) Lookup(name string) (*Host, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	if e := f.find(name); e != nil {
		return s.host(e), nil
	}
	return nil, output.NewError(output.ExitUsage,
		fmt.Sprintf("host %s is not in %s", name, s.hostsPath()),
		fmt.Sprintf("add it with burro host add %s <user@host>", name))
}

// List returns every host in the order they were added.
func (s *Store) List() ([]*Host, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	list := make([]*Host, 0, len(f.entries))
	for i := range f.entries {
		list = append(list, s.host(&f.entries[i]))
	}
	return list, nil
}

func (s *Store) host(e *entry) *Host {
	return &Host{store: s, name: e.name, alias: e.alias, address: e.address(), key: e.key}
}

// Add writes the block for name and the Include line. target is
// user@host[:port], or the name itself to reuse an alias from the user's ssh
// config. The returned undo puts the hosts file back as it was, for when the
// first ssh call fails.
func (s *Store) Add(ctx context.Context, name, target, key string, force bool) (*Host, func() error, error) {
	f, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	ours := f.find(name) != nil
	if ours && !force {
		return nil, nil, output.NewError(output.ExitUsage, fmt.Sprintf("host %s already exists", name),
			"pick another name, or pass --force to replace it")
	}
	e, err := s.newEntry(ctx, name, target, key, ours)
	if err != nil {
		return nil, nil, err
	}

	before, err := os.ReadFile(filepath.Clean(s.hostsPath()))
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fileError("read", s.hostsPath(), err)
	}
	f.put(&e)
	if err := s.writeFile(s.hostsPath(), []byte(f.String())); err != nil {
		return nil, nil, err
	}
	undo := func() error {
		if existed {
			return s.writeFile(s.hostsPath(), before)
		}
		if err := os.Remove(s.hostsPath()); err != nil {
			return fileError("remove", s.hostsPath(), err)
		}
		return nil
	}
	if err := s.ensureInclude(); err != nil {
		return nil, nil, errors.Join(err, undo())
	}
	return s.host(&e), undo, nil
}

func (s *Store) newEntry(ctx context.Context, name, target, key string, ours bool) (entry, error) {
	if !strings.Contains(target, "@") {
		return s.aliasEntry(ctx, name, target, key, ours)
	}
	u, h, port, err := parseAddress(target)
	if err != nil {
		return entry{}, err
	}
	if key, err = checkKey(key); err != nil {
		return entry{}, err
	}
	if !ours {
		taken, err := s.inUserConfig(ctx, name)
		if err != nil {
			return entry{}, err
		}
		if taken {
			return entry{}, output.NewError(output.ExitUsage,
				fmt.Sprintf("host %s already exists in your ssh config", name),
				fmt.Sprintf("reuse it with burro host add %s %s, or pick another name", name, name))
		}
	}
	return addressEntry(name, u, h, port, key), nil
}

func (s *Store) aliasEntry(ctx context.Context, name, target, key string, ours bool) (entry, error) {
	if target != name {
		return entry{}, output.NewError(output.ExitUsage,
			fmt.Sprintf("%q is neither user@host nor the name %s", target, name),
			fmt.Sprintf("give an address like root@203.0.113.10, or reuse an ssh alias with burro host add %s %s", target, target))
	}
	if key != "" {
		return entry{}, output.NewError(output.ExitUsage, "-i works only with a user@host target",
			"drop -i; the alias keeps the key from your ssh config")
	}
	if !ours {
		found, err := s.inUserConfig(ctx, name)
		if err != nil {
			return entry{}, err
		}
		if !found {
			return entry{}, output.NewError(output.ExitUsage,
				fmt.Sprintf("host %s is not in your ssh config", name),
				fmt.Sprintf("give an address: burro host add %s <user@host>", name))
		}
	}
	return entry{name: name, alias: true, lines: []string{marker + name + " alias", ""}}, nil
}

// parseAddress splits user@host[:port]. IPv6 hosts go in brackets.
func parseAddress(target string) (u, h, port string, err error) {
	bad := output.NewError(output.ExitUsage, fmt.Sprintf("%q is not user@host[:port]", target),
		"give the server as user@host or user@host:port, for example root@203.0.113.10")
	u, rest, _ := strings.Cut(target, "@")
	h, port, ok := splitHostPort(rest)
	if !ok || !userPattern.MatchString(u) {
		return "", "", "", bad
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", "", "", bad
		}
	}
	return u, h, port, nil
}

// splitHostPort splits host[:port] or [ipv6][:port] and checks the host.
func splitHostPort(s string) (h, port string, ok bool) {
	inner, isV6 := strings.CutPrefix(s, "[")
	if !isV6 {
		h, port, hasPort := strings.Cut(s, ":")
		return h, port, hostPattern.MatchString(h) && (!hasPort || port != "")
	}
	h, rest, closed := strings.Cut(inner, "]")
	if !closed || !ipv6Pattern.MatchString(h) {
		return "", "", false
	}
	if rest == "" {
		return h, "", true
	}
	port, ok = strings.CutPrefix(rest, ":")
	return h, port, ok && port != ""
}

// checkKey makes the key path absolute and checks that it exists, so the
// block never points at a typo.
func checkKey(key string) (string, error) {
	if key == "" {
		return "", nil
	}
	if strings.ContainsFunc(key, unicode.IsControl) || strings.Contains(key, `"`) {
		return "", output.NewError(output.ExitUsage, fmt.Sprintf("key path %q has characters ssh config can't hold", key),
			"use a key path without quotes or control characters")
	}
	abs, err := filepath.Abs(key)
	if err == nil {
		_, err = os.Stat(abs)
	}
	if err != nil {
		return "", output.NewError(output.ExitUsage, fmt.Sprintf("key %s: %v", key, errors.Unwrap(err)),
			"pass the path of an existing private key with -i")
	}
	return abs, nil
}

// inUserConfig tells whether a Host block of the user's ssh config matches
// name: ssh resolves it differently from a name nothing matches.
func (s *Store) inUserConfig(ctx context.Context, name string) (bool, error) {
	got, err := s.resolve(ctx, name)
	if err != nil {
		return false, err
	}
	unknown, err := s.resolve(ctx, probeName)
	if err != nil {
		return false, err
	}
	if len(got) != len(unknown) {
		return true, nil
	}
	for i := range got {
		// Only values equal to the name itself, such as "hostname vps1",
		// differ for an unmatched name.
		if key, value, _ := strings.Cut(got[i], " "); value == name {
			got[i] = key + " " + probeName
		}
		if got[i] != unknown[i] {
			return true, nil
		}
	}
	return false, nil
}

// ensureInclude puts the Include line at the top of ~/.ssh/config unless the
// file already has it.
func (s *Store) ensureInclude() error {
	path := s.configPath()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileError("read", path, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == includeLine {
			return nil
		}
	}
	return s.writeFile(path, append([]byte(includeLine+"\n"), data...))
}

func (s *Store) load() (*hostsFile, error) {
	data, err := os.ReadFile(filepath.Clean(s.hostsPath()))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fileError("read", s.hostsPath(), err)
	}
	return parseHostsFile(string(data)), nil
}

// writeFile replaces path atomically: a temp file in the same folder, fsync,
// rename. The old mode stays; a new file gets 0600 and new folders 0700. A
// symlinked file is written through to its target.
func (s *Store) writeFile(path string, data []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fileError("create", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fileError("write", path, err)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fileError("write", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fileError("write", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fileError("write", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fileError("write", path, err)
	}
	if err := s.rename(tmp.Name(), path); err != nil {
		return fileError("write", path, err)
	}
	done = true
	return nil
}

func fileError(verb, path string, err error) error {
	return output.NewError(output.ExitServer, fmt.Sprintf("%s %s: %v", verb, path, errors.Unwrap(err)),
		fmt.Sprintf("check the permissions of %s", filepath.Dir(path)))
}

// entry is one block of the hosts file. lines keep the block as written, so
// edits inside other blocks survive a rewrite.
type entry struct {
	name  string
	alias bool
	user  string
	host  string
	port  string
	key   string
	lines []string
}

func addressEntry(name, u, h, port, key string) entry {
	lines := []string{marker + name, "Host " + name, "  HostName " + h, "  User " + u}
	if port != "" {
		lines = append(lines, "  Port "+port)
	}
	if key != "" {
		lines = append(lines, `  IdentityFile "`+key+`"`)
	}
	return entry{name: name, user: u, host: h, port: port, key: key, lines: append(lines, "")}
}

// address is user@host[:port] for display, or empty for an alias.
func (e *entry) address() string {
	if e.alias {
		return ""
	}
	return joinAddress(e.user, e.host, e.port)
}

func joinAddress(u, h, port string) string {
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	addr := h
	if u != "" {
		addr = u + "@" + h
	}
	if port != "" && port != "22" {
		addr += ":" + port
	}
	return addr
}

type hostsFile struct {
	preamble []string
	entries  []entry
}

func parseHostsFile(data string) *hostsFile {
	f := &hostsFile{}
	if data == "" {
		return f
	}
	var cur *entry
	for line := range strings.SplitSeq(strings.TrimSuffix(data, "\n"), "\n") {
		if rest, ok := strings.CutPrefix(line, marker); ok {
			if fields := strings.Fields(rest); len(fields) > 0 {
				f.entries = append(f.entries, entry{name: fields[0], alias: len(fields) > 1 && fields[1] == "alias"})
				cur = &f.entries[len(f.entries)-1]
				cur.lines = []string{line}
				continue
			}
		}
		if cur == nil {
			f.preamble = append(f.preamble, line)
			continue
		}
		cur.lines = append(cur.lines, line)
		cur.setOption(line)
	}
	return f
}

func (e *entry) setOption(line string) {
	key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
	value = strings.Trim(strings.TrimSpace(value), `"`)
	switch strings.ToLower(key) {
	case "hostname":
		e.host = value
	case "user":
		e.user = value
	case "port":
		e.port = value
	case "identityfile":
		e.key = value
	}
}

func (f *hostsFile) find(name string) *entry {
	for i := range f.entries {
		if f.entries[i].name == name {
			return &f.entries[i]
		}
	}
	return nil
}

// put replaces the entry with the same name in place, or appends e.
func (f *hostsFile) put(e *entry) {
	if old := f.find(e.name); old != nil {
		*old = *e
		return
	}
	f.entries = append(f.entries, *e)
}

func (f *hostsFile) String() string {
	lines := slices.Clone(f.preamble)
	for _, e := range f.entries {
		lines = append(lines, e.lines...)
	}
	return strings.Join(lines, "\n") + "\n"
}
