// Package report reads and writes a project's .burro folder on the server:
// run folders, status.json, the lock, step logs. It cleans logs and builds
// the summaries the executor sends back.
package report

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/timhartmann7/burro/internal/output"
)

const (
	// staleAfter is how old a heartbeat may get before a runner whose
	// process is gone counts as dead.
	staleAfter = 15 * time.Second
	// statusFile is the state of one run.
	statusFile = "status.json"
	pidFile    = "pid"
	runsFolder = ".burro/runs"
	lockFile   = ".burro/lock"
	// guardFile serializes every look at the lock, so two clients can't both
	// take it.
	guardFile = ".burro/lock.guard"
	// Died is the reason of a run whose runner vanished.
	Died = "runner died"
)

// runName matches run folders: r1, r2, ...
var runName = regexp.MustCompile(`^r([1-9]\d{0,8})$`)

// Status is status.json: the state of one run and its steps.
type Status struct {
	Run       string     `json:"run"`
	Pipeline  string     `json:"pipeline"`
	Tree      string     `json:"tree"`
	State     string     `json:"state"`
	Exit      *int       `json:"exit"`
	Reason    string     `json:"reason"`
	Started   time.Time  `json:"started"`
	Finished  *time.Time `json:"finished"`
	Heartbeat time.Time  `json:"heartbeat"`
	Steps     []Step     `json:"steps"`
}

// Step is one step of status.json.
type Step struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
	Seconds int    `json:"seconds"`
	Exit    *int   `json:"exit"`
}

// Finish ends the run at now with code and reason. Steps that never got to
// run become "not run".
func (s *Status) Finish(state string, code int, reason string, now time.Time) {
	for i := range s.Steps {
		if s.Steps[i].State == output.StatePending {
			s.Steps[i].State = output.StateNotRun
		}
	}
	s.State, s.Exit, s.Reason = state, &code, reason
	finished := Stamp(now)
	s.Finished = &finished
}

// Stamp is a time the way status.json stores it: UTC to the second.
func Stamp(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// Project is the .burro folder of one project on the server.
type Project struct {
	dir string
}

// Open returns the project in the absolute folder dir.
func Open(dir string) *Project {
	return &Project{dir: dir}
}

// Key is the run key: unique on the host, because two projects can both
// have r1.
func (p *Project) Key(id string) string {
	return p.KeyPrefix() + id
}

// KeyPrefix starts the run key of every run of the project.
func (p *Project) KeyPrefix() string {
	sum := sha256.Sum256([]byte(p.dir))
	return fmt.Sprintf("burro-%x-", sum[:4])
}

// RunDir is the folder of run id.
func (p *Project) RunDir(id string) string {
	return filepath.Join(p.dir, runsFolder, id)
}

// LogFile is the raw log of one step of run id.
func (p *Project) LogFile(id, step string) string {
	return filepath.Join(p.RunDir(id), step+".log")
}

// Locked runs fn while it holds the guard of the lock. Every reader and
// writer of the lock goes through it.
func (p *Project) Locked(fn func() error) error {
	if err := os.MkdirAll(filepath.Join(p.dir, ".burro"), 0o700); err != nil {
		return fmt.Errorf("create .burro: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(p.dir, guardFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock guard: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock guard: %w", err)
	}
	return fn()
}

// Holder is the run that holds the lock while its runner lives, or empty.
// Call it from Locked.
func (p *Project) Holder() string {
	data, err := os.ReadFile(filepath.Join(p.dir, lockFile))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if pid, err := p.pid(id); err != nil || !Alive(pid) {
		return ""
	}
	return id
}

// Busy is the error for a command that needs the lock while run id holds it.
func Busy(id string) error {
	return output.NewError(output.ExitBusy, fmt.Sprintf("run %s is active in this project", id),
		fmt.Sprintf("wait for it with burro wait %s, or stop it with burro cancel %s", id, id))
}

// Take writes the lock for run id over a stale one. Call it from Locked
// after Holder came back empty.
func (p *Project) Take(id string) error {
	path := filepath.Clean(filepath.Join(p.dir, lockFile))
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove stale lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("take lock: %w", err)
	}
	_, err = fmt.Fprintln(f, id)
	return errors.Join(err, f.Close())
}

// Release removes the lock if run id still holds it.
func (p *Project) Release(id string) error {
	return p.Locked(func() error {
		path := filepath.Clean(filepath.Join(p.dir, lockFile))
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		if strings.TrimSpace(string(data)) != id {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("release lock: %w", err)
		}
		return nil
	})
}

// NewRun creates the folder of the next run, one above the highest id, and
// returns the id. mkdir fails on an existing folder, so two callers never
// share one.
func (p *Project) NewRun() (string, error) {
	runs := filepath.Join(p.dir, runsFolder)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", runsFolder, err)
	}
	last, err := p.highest()
	if err != nil {
		return "", err
	}
	for n := last + 1; ; n++ {
		id := "r" + strconv.Itoa(n)
		err := os.Mkdir(p.RunDir(id), 0o700)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create run folder: %w", err)
		}
		return id, nil
	}
}

// Last is the newest run of the project.
func (p *Project) Last() (string, error) {
	n, err := p.highest()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", output.NewError(output.ExitUsage, "this project has no runs yet", "start one with burro run")
	}
	return "r" + strconv.Itoa(n), nil
}

func (p *Project) highest() (int, error) {
	entries, err := os.ReadDir(filepath.Join(p.dir, runsFolder))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", runsFolder, err)
	}
	last := 0
	for _, e := range entries {
		if m := runName.FindStringSubmatch(e.Name()); m != nil && e.IsDir() {
			n, err := strconv.Atoi(m[1])
			if err == nil {
				last = max(last, n)
			}
		}
	}
	return last, nil
}

// WritePid records the runner's process id.
func (p *Project) WritePid(id string, pid int) error {
	return writeAtomic(filepath.Join(p.RunDir(id), pidFile), []byte(strconv.Itoa(pid)+"\n"))
}

func (p *Project) pid(id string) (int, error) {
	data, err := os.ReadFile(filepath.Join(p.RunDir(id), pidFile))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// Signal sends sig to the runner of run id.
func (p *Project) Signal(id string, sig syscall.Signal) error {
	pid, err := p.pid(id)
	if err != nil {
		return fmt.Errorf("read runner pid: %w", err)
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal runner: %w", err)
	}
	return nil
}

// Status reads status.json of run id.
func (p *Project) Status(id string) (*Status, error) {
	data, err := os.ReadFile(filepath.Join(p.RunDir(id), statusFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, output.NewError(output.ExitUsage, fmt.Sprintf("this project has no run %s", id),
			"leave the id out for the last run")
	}
	if err != nil {
		return nil, fmt.Errorf("read status of %s: %w", id, err)
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("status of %s: %w", id, err)
	}
	return &s, nil
}

// WriteStatus replaces status.json in one rename, so readers never see half
// a file.
func (p *Project) WriteStatus(s *Status) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode status: %w", err)
	}
	return writeAtomic(filepath.Join(p.RunDir(s.Run), statusFile), append(data, '\n'))
}

// Current reads status.json of run id. A run that says running while its
// heartbeat is stale and its runner is gone is marked failed with reason
// "runner died", and that is written back.
func (p *Project) Current(id string, now time.Time) (*Status, error) {
	s, err := p.Status(id)
	if err != nil || s.State != output.StateRunning || now.Sub(s.Heartbeat) <= staleAfter {
		return s, err
	}
	if pid, err := p.pid(id); err == nil && Alive(pid) {
		return s, nil
	}
	for i := range s.Steps {
		if s.Steps[i].State == output.StateRunning {
			s.Steps[i].State, s.Steps[i].Detail = output.StateFail, Died
		}
	}
	s.Finish(output.StateDone, int(output.ExitServer), Died, s.Heartbeat)
	return s, p.WriteStatus(s)
}

// writeAtomic writes path through a temp file in the same folder.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync(), f.Close())
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", filepath.Base(path), err), os.Remove(f.Name()))
	}
	return nil
}

// WriteFile writes path atomically, for the files the runner keeps beside
// the run folders.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return writeAtomic(path, data)
}

// Alive tells whether process pid runs. A process that died while nobody
// reaps it stays a zombie, and that counts as dead.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// Without /proc, as on macOS, the signal check is all there is.
		return true
	}
	proc, ok := parseStat(data)
	return !ok || !proc.zombie
}

// Members lists the live processes whose process group or session is id.
// It reads /proc and finds nothing where there is none.
func Members(id int) []int {
	dirs, _ := filepath.Glob("/proc/[0-9]*/stat")
	var pids []int
	for _, path := range dirs {
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			continue
		}
		proc, ok := parseStat(data)
		if ok && !proc.zombie && (proc.group == id || proc.session == id) {
			pids = append(pids, proc.pid)
		}
	}
	return pids
}

type proc struct {
	pid, group, session int
	zombie              bool
}

// parseStat reads /proc/<pid>/stat. The command name in brackets may hold
// spaces and brackets, so fields count from the last ")".
func parseStat(data []byte) (proc, bool) {
	s := string(data)
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 1 || end < open {
		return proc{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	fields := strings.Fields(s[end+1:])
	if err != nil || len(fields) < 4 {
		return proc{}, false
	}
	group, err1 := strconv.Atoi(fields[2])
	session, err2 := strconv.Atoi(fields[3])
	if err1 != nil || err2 != nil {
		return proc{}, false
	}
	return proc{pid: pid, group: group, session: session, zombie: fields[0] == "Z"}, true
}
