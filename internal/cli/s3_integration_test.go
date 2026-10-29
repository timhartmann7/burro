//go:build integration

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/timhartmann7/burro/internal/report"
	"github.com/timhartmann7/burro/internal/testutil"
)

const (
	// testImage is the test server's base image, the one image steps run.
	testImage = "debian:stable-slim@sha256:13e0a2e78aa0554ad5ff66e4e260c6dbcdff807eb1f39d43b9fb0288aae88b04"
	shmDir    = "/dev/shm/burro"
)

// randomHex is a fresh name part, so docker resources of one test never
// meet those of another.
func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// serverKey is the run key of run id of the project in dir under the
// server's home folder.
func serverKey(dir, id string) string {
	return report.Open("/home/burro/" + dir).Key(id)
}

// hostDocker runs docker against the daemon the docker server shares and
// returns its stdout. stderr carries pull progress when the image isn't
// there yet, so it only explains failures.
func hostDocker(t *testing.T, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "docker", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// owned lists the containers and volumes of run key, by either label.
func owned(t *testing.T, key string) (containers, volumes []string) {
	t.Helper()
	for _, label := range []string{"com.docker.compose.project=" + key, "burro.run=" + key} {
		containers = append(containers, strings.Fields(hostDocker(t, "ps", "-aq", "--filter", "label="+label))...)
		volumes = append(volumes, strings.Fields(hostDocker(t, "volume", "ls", "-q", "--filter", "label="+label))...)
	}
	return containers, volumes
}

// dropOwned removes whatever a failed test left of run key.
func dropOwned(t *testing.T, key string) {
	t.Helper()
	t.Cleanup(func() {
		containers, volumes := owned(t, key)
		if len(containers) > 0 {
			hostDocker(t, append([]string{"rm", "-f"}, containers...)...)
		}
		if len(volumes) > 0 {
			hostDocker(t, append([]string{"volume", "rm", "-f"}, volumes...)...)
		}
	})
}

func (w *world) in(repo string) {
	w.t.Helper()
	w.t.Chdir(repo)
	w.repo = repo
}

func (w *world) shm() string {
	w.t.Helper()
	return w.srv.Exec(w.t, "sh", "-c", "ls "+shmDir+" 2>/dev/null; true")
}

const dockerSteps = `
[pipelines]
default = ["up"]

[steps.up]
run = '''docker compose up -d --quiet-pull && docker run -d --label burro.run=$BURRO_RUN ` + testImage + ` sleep 600 &&
docker volume create --label burro.run=$BURRO_RUN $BURRO_RUN-extra && echo ready && while [ ! -e stop ]; do sleep 0.2; done'''
`

const composeFile = "services:\n  db:\n    image: " + testImage + "\n    command: sleep 600\n    volumes: [data:/data]\nvolumes:\n  data: {}\n"

// acceptance: S3-07 S3-08
func TestDockerIsolation(t *testing.T) {
	foreign := hostDocker(t, "run", "-d", "--rm", testImage, "sleep", "600")
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", foreign).Run() })
	name := "iso-" + randomHex(t)
	project := func(dir string) map[string]string {
		return map[string]string{tomlFile: "host = \"vps1\"\npath = \"~/burro/" + dir + "\"\n" + dockerSteps, "docker-compose.yml": composeFile}
	}
	w := newWorldOn(t, testutil.StartDockerServer(t), project(name+"-a"))
	w.addHost(v1)
	repoA, repoB := w.repo, testutil.GitRepo(t, project(name+"-b"))
	keyA, keyB := serverKey("burro/"+name+"-a", "r1"), serverKey("burro/"+name+"-b", "r1")
	dropOwned(t, keyA)
	dropOwned(t, keyB)
	t.Cleanup(func() { w.in(repoA); w.burro(v1, argCancel); w.in(repoB); w.burro(v1, argCancel) })

	// Two projects with the same compose file, both at r1.
	w.must(v1, argRun)
	w.waitFor("burro/"+name+"-a/.burro/runs/r1/up.log", "ready")
	w.in(repoB)
	w.must(v1, argRun)
	w.waitFor("burro/"+name+"-b/.burro/runs/r1/up.log", "ready")
	for _, key := range []string{keyA, keyB} {
		if containers, volumes := owned(t, key); len(containers) != 2 || len(volumes) != 2 {
			t.Fatalf("%s: containers %v volumes %v", key, containers, volumes)
		}
	}
	if shm := w.shm(); !strings.Contains(shm, keyA) || !strings.Contains(shm, keyB) {
		t.Fatalf("secrets folders %q", shm)
	}

	// Cancel cleans B up and leaves A alone.
	w.must(v1, argCancel)
	if containers, volumes := owned(t, keyB); len(containers)+len(volumes) != 0 {
		t.Fatalf("B left containers %v volumes %v", containers, volumes)
	}
	if containers, _ := owned(t, keyA); len(containers) != 2 || strings.Contains(w.shm(), keyB) || !strings.Contains(w.shm(), keyA) {
		t.Fatalf("A after B's cancel: %v, shm %q", containers, w.shm())
	}

	// A ends on its own and cleans up at the end.
	w.in(repoA)
	w.srv.Exec(t, "touch", "burro/"+name+"-a/stop")
	if r := w.burro(v1, argWait); r.Code != 0 {
		t.Fatalf("wait: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	if containers, volumes := owned(t, keyA); len(containers)+len(volumes) != 0 || strings.Contains(w.shm(), keyA) {
		t.Fatalf("A left containers %v volumes %v shm %q", containers, volumes, w.shm())
	}
	if r := w.burro(v1, argClean); r.Code != 0 || r.Stdout != "r1 · nothing to remove\n" {
		t.Fatalf("clean: %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	if running := hostDocker(t, "inspect", "-f", "{{.State.Running}}", foreign); running != flagSet {
		t.Fatalf("foreign container running=%s", running)
	}
}

// secretWorld is a world whose step token needs the secret token, read from
// a file in the client's home folder.
func secretWorld(t *testing.T, value string) *world {
	t.Helper()
	w := ready(t, `
[pipelines]
default = ["quick"]
hold = ["leak"]

[steps.quick]
run = 'test -s "$BURRO_SECRETS/token"'
secrets = ["token"]

[steps.leak]
run = '''cat "$BURRO_SECRETS/token"; echo "token=$(cat "$BURRO_SECRETS/token")" >&2; echo ready; while [ ! -e stop-$BURRO_RUN_ID ]; do sleep 0.2; done; exit 1'''
secrets = ["token"]

[secrets]
token = "~/.burro-token"
`, nil)
	writeFile(t, filepath.Join(w.srv.Home(), ".burro-token"), value+"\n")
	t.Cleanup(func() { w.killServer("stop-$BURRO_RUN_ID") })
	w.cleanup()
	return w
}

// acceptance: S3-09
func TestSecretsStayInTmpfs(t *testing.T) {
	value := "burro-secret-" + randomHex(t) + randomHex(t)
	w := secretWorld(t, value)
	w.must(v1, argRun, "hold")
	w.waitFor(runsDir+"/r1/leak.log", "ready")
	folder := shmDir + "/" + serverKey(appDir, "r1")
	if got := w.srv.Exec(t, "stat", "-c", "%a", folder, folder+"/token"); got != "700\n600" {
		t.Fatalf("modes %q", got)
	}
	if r := w.burro(v1, argExec, "--", "sh", "-c", "cat "+shmDir+"/*/token"); r.Code != 0 || r.Stdout != "***\n" {
		t.Fatalf("exec: %d %q", r.Code, r.Stdout)
	}
	// What ps shows comes from /proc; the checks after this one carry the
	// value in their own arguments.
	cmdlines := w.srv.Root(t, "sh", "-c", `for p in /proc/[0-9]*; do tr '\0' ' ' <"$p/cmdline" 2>/dev/null; echo; done`)
	if strings.Contains(cmdlines, value) || !strings.Contains(cmdlines, "agent runner") {
		t.Fatalf("secret in a command line")
	}
	found := w.srv.Root(t, "sh", "-c", `find / -xdev -type f -print0 2>/dev/null | xargs -0 grep -lF -e "$1" 2>/dev/null; true`, "sh", value)
	if found != "" {
		t.Fatalf("secret on disk in %s", found)
	}
	if log := w.serverFile(runsDir + "/r1/leak.log"); strings.Contains(log, value) || !strings.Contains(log, "token=***") {
		t.Fatalf("log %q", log)
	}
	w.srv.Exec(t, "touch", appDir+"/stop-r1")
	r := w.burro(v1, argWait)
	if r.Code != 1 || strings.Contains(r.Stdout, value) || !strings.Contains(r.Stdout, "\n  ***\n") || !strings.Contains(r.Stdout, "\n  token=***\n") {
		t.Fatalf("wait: %d %q", r.Code, r.Stdout)
	}
	for _, args := range [][]string{{argFails, argJSON}, {cmdLog, argStep, "leak"}, {argStatus}} {
		if r := w.burro(v1, args...); strings.Contains(r.Stdout+r.Stderr, value) {
			t.Fatalf("%q shows the secret", args)
		}
	}
	if shm := w.shm(); shm != "" {
		t.Fatalf("secrets folder kept: %q", shm)
	}
}

// acceptance: S3-10
func TestSecretsGoAway(t *testing.T) {
	w := secretWorld(t, "burro-secret-"+randomHex(t))
	folder := func(id string) string { return serverKey(appDir, id) }

	// A successful run.
	w.must(v1, argRun)
	if r := w.burro(v1, argWait); r.Code != 0 || w.shm() != "" {
		t.Fatalf("after success: %d %q, shm %q", r.Code, r.Stdout, w.shm())
	}

	// A canceled run.
	w.must(v1, argRun, "hold")
	w.waitFor(runsDir+"/r2/leak.log", "ready")
	if w.shm() != folder("r2") {
		t.Fatalf("shm during r2: %q", w.shm())
	}
	w.must(v1, argCancel)
	if w.shm() != "" {
		t.Fatalf("after cancel: %q", w.shm())
	}

	// A runner killed with -9 leaves its folder until clean.
	w.must(v1, argRun, "hold")
	w.waitFor(runsDir+"/r3/leak.log", "ready")
	w.killServer("agent runner")
	w.killServer("stop-$BURRO_RUN_ID")
	if w.shm() != folder("r3") {
		t.Fatalf("shm after kill: %q", w.shm())
	}
	if r := w.burro(v1, argClean); r.Code != 0 || r.Stdout != "r3 · removed the secrets folder\n" || w.shm() != "" {
		t.Fatalf("clean: %d %q %q, shm %q", r.Code, r.Stdout, r.Stderr, w.shm())
	}

	// ... or until the next run starts.
	w.must(v1, argRun, "hold")
	w.waitFor(runsDir+"/r4/leak.log", "ready")
	w.killServer("agent runner")
	w.killServer("stop-$BURRO_RUN_ID")
	w.must(v1, argRun)
	if r := w.burro(v1, argWait); r.Code != 0 || w.shm() != "" {
		t.Fatalf("after the next run: %d %q, shm %q", r.Code, r.Stdout, w.shm())
	}
}

// acceptance: S3-15
func TestMissingSecret(t *testing.T) {
	w := ready(t, "\n[pipelines]\ndefault = [\"test\"]\n\n[steps.test]\nrun = \"true\"\nsecrets = [\"token\"]\n\n[secrets]\ntoken = \"~/nope\"\n", nil)
	r := w.burro(v1, argRun)
	if r.Code != 4 || r.Stdout != "" || r.Stderr != "burro: secret token: ~/nope does not exist\nfix: create ~/nope, or point secrets.token in burro.toml to the file that holds it\n" {
		t.Fatalf("exit %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	if got := w.srv.Exec(t, "sh", "-c", "ls -A ~/burro 2>/dev/null; true"); got != "" {
		t.Fatalf("something reached the server: %q", got)
	}
}

func fixtureFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "junit", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const reportSteps = `
[pipelines]
verify = ["e2e", "py", "broken"]
other = ["py"]

[steps.e2e]
run = "mkdir -p .burro/out && cp reports/playwright.xml .burro/out/e2e.xml && exit 1"
junit = ".burro/out/e2e.xml"
continue_on_fail = true

[steps.py]
run = "mkdir -p .burro/out/py && rm -f .burro/out/py/* && cp reports/py/*.xml .burro/out/py/ && exit 1"
junit = ".burro/out/py/*.xml"
continue_on_fail = true

[steps.broken]
run = "echo boom; head -c 300 reports/jest.xml > .burro/out/broken.xml; exit 1"
junit = ".burro/out/broken.xml"
`

// acceptance: S3-02 S3-04 S3-06
func TestFailuresFromReports(t *testing.T) {
	// ready moves into the project, so the fixtures are read first.
	jest, pytest := fixtureFile(t, "jest.xml"), fixtureFile(t, "pytest.xml")
	w := ready(t, reportSteps, map[string]string{
		"reports/playwright.xml": fixtureFile(t, "playwright.xml"),
		"reports/jest.xml":       jest,
		"reports/py/pytest.xml":  pytest,
	})
	w.must(v1, argRun, argVerify)
	r := w.burro(v1, argWait)
	if r.Code != 1 || !strings.Contains(r.Stdout, "\ne2e     FAIL  3/12  ") ||
		!strings.Contains(r.Stdout, "\n  NEW   site.spec.js:8  cohorts are listed  x3\n        expect(received).toHaveLength(expected)\n") ||
		!strings.Contains(r.Stdout, "\n  NEW   tests/test_calc.py  test_divides\n        assert 2.0 == 3\n        at tests/test_calc.py:11\n") ||
		!strings.Contains(r.Stdout, "\nbroken  FAIL  ") || !strings.Contains(r.Stdout, "\n  report unreadable\n  boom\nmore: burro log r1 --step broken\n") ||
		!strings.HasSuffix(r.Stdout, "more: burro log r1 --step e2e --grep cohorts\n") {
		t.Fatalf("exit %d\n%s", r.Code, r.Stdout)
	}
	if more := w.burro(v1, strings.Fields("log r1 --step e2e --grep cohorts")...); more.Code != 0 {
		t.Fatalf("more: %d %q", more.Code, more.Stderr)
	}
	if f := w.burro(v1, argFails); f.Stdout != r.Stdout || f.Code != 1 {
		t.Fatalf("failures differs from wait:\n%s", f.Stdout)
	}

	// Another pipeline fails differently; the next verify run compares with
	// r1 only.
	writeFile(t, filepath.Join(w.repo, "reports", "py", "pytest.xml"), jest)
	w.must(v1, argRun, "other")
	w.burro(v1, argWait)
	writeFile(t, filepath.Join(w.repo, "reports", "py", "pytest.xml"), pytest)
	writeFile(t, filepath.Join(w.repo, "reports", "py", "jest.xml"), jest)
	w.must(v1, argRun, argVerify)
	r = w.burro(v1, argWait)
	for _, want := range []string{"  SEEN  site.spec.js:8  cohorts are listed  x3", "  SEEN  tests/test_calc.py  test_divides", "  NEW   calc divides numbers  calc divides numbers"} {
		if !strings.Contains(r.Stdout, "\n"+want+"\n") {
			t.Fatalf("no %q in\n%s", want, r.Stdout)
		}
	}
}

// acceptance: S3-05
func TestFailuresCut(t *testing.T) {
	w := ready(t, `
[pipelines]
default = ["unit"]

[steps.unit]
run = '''mkdir -p out && { echo '<testsuites><testsuite name="unit">'; for i in $(seq 1 300); do echo "<testcase classname=\"pkg/a_test.go\" name=\"TestCase$i\"><failure message=\"got $i, want 0\"/></testcase>"; done; echo '</testsuite></testsuites>'; } > out/unit.xml; exit 1'''
junit = "out/unit.xml"
`, nil)
	w.must(v1, argRun)
	r := w.burro(v1, argWait)
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	m := regexp.MustCompile(`^\+(\d+) more: (burro failures r1 --max-lines 400)$`).FindStringSubmatch(lines[len(lines)-1])
	if r.Code != 1 || len(lines) > 40 || m == nil || !strings.HasPrefix(lines[1], "unit  FAIL  0/300  ") {
		t.Fatalf("exit %d, %d lines\n%s", r.Code, len(lines), r.Stdout)
	}
	shown := strings.Count(r.Stdout, "  NEW   ")
	if fmt.Sprint(300-shown) != m[1] {
		t.Fatalf("%d shown, says +%s", shown, m[1])
	}
	more := w.burro(v1, strings.Fields(m[2])[1:]...)
	moreLines := strings.Split(strings.TrimSuffix(more.Stdout, "\n"), "\n")
	if more.Code != 1 || len(moreLines) > 400 || strings.Count(more.Stdout, "  NEW   ") <= shown ||
		!strings.HasPrefix(moreLines[len(moreLines)-1], "+") || !strings.Contains(more.Stdout, "\n  NEW   pkg/a_test.go  TestCase1\n        got 1, want 0\n") {
		t.Fatalf("more: exit %d, %d lines", more.Code, len(moreLines))
	}
	if all := w.burro(v1, argFails, "--max-lines", "1000"); all.Code != 1 || strings.Count(all.Stdout, "  NEW   ") != 300 || strings.Contains(all.Stdout, "more: burro failures") {
		t.Fatalf("all: %d %d", all.Code, strings.Count(all.Stdout, "  NEW   "))
	}
	if r := w.burro(v1, argFails, "--max-lines", "40", argJSON); r.Code != 1 || !strings.Contains(r.Stdout, fmt.Sprintf(`"hidden":%d`, 300-shown)) || strings.Count(r.Stdout, "\n") != 1 {
		t.Fatalf("json: %q", r.Stdout)
	}
}
