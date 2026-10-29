package report

import (
	"cmp"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// maxBody caps the kept text of one failure; message and frame come
	// from its start.
	maxBody = 64 << 10
	// maxMessage caps the first message line a summary shows.
	maxMessage = 1000
	elemSuite  = "testsuite"
)

// errUnreadable is a report that is empty, broken or too big.
var errUnreadable = errors.New("report unreadable")

var (
	// pathFrame finds path:line in a stack line: node, vitest, pytest, go,
	// rust and php all print one.
	pathFrame = regexp.MustCompile(`(?:^|[\s(\[@'"])(?:file://)?((?:/|\./)?(?:[\w.@+~$-]+/)*[\w@+~$-][\w.@+~$-]*\.[A-Za-z]\w*):(\d+)`)
	// javaFrame is a JVM frame: the class tells whether it is the project's,
	// the file name alone can't.
	javaFrame = regexp.MustCompile(`\bat (?:[\w$.@-]*/)*([\w$.]+)\.[\w$<>-]+\(([\w$.-]+\.\w+):(\d+)\)`)
	// playwrightHeader is the first line of a Playwright failure: project,
	// file and line of the test, title.
	playwrightHeader = regexp.MustCompile(`^\s*\[[^\]]+\] › (\S+):(\d+):\d+ › `)
	// projectPrefix is the "[chromium] " a Playwright test name may start
	// with; the same test in another project is the same failure.
	projectPrefix = regexp.MustCompile(`^\[[^\]]+\]\s+(?:›\s+)?`)
	// noise are lines that say nothing about the failure: go test's own
	// markers, gotestsum's "Failed", rust's panic location line.
	noise = regexp.MustCompile(`^(?:=== (?:RUN|PAUSE|CONT)\s|--- (?:FAIL|PASS|SKIP):|Failed$|thread '.*' panicked at )`)
)

// vendored are folders whose files belong to dependencies, not the project.
func vendored() []string {
	return []string{"node_modules", "vendor", "site-packages", "dist-packages", ".venv"}
}

// jvmToolchain are class prefixes of the JDK, the test frameworks and the
// build tools.
func jvmToolchain() []string {
	return []string{
		"java.", "javax.", "jdk.", "sun.", "com.sun.", "kotlin.", "kotlinx.", "scala.",
		"org.junit.", "junit.", "org.opentest4j.", "org.testng.", "org.apache.maven.", "org.gradle.", "worker.org.gradle.",
	}
}

// Case is one failed test of a report.
type Case struct {
	File    string
	Line    int
	Name    string
	Message string
	Frame   string
}

// junit is what the reports of one step hold.
type junit struct {
	total, failed, skipped int
	cases                  []Case
}

// testcase is one <testcase> while it is read.
type testcase struct {
	name, classname, file string
	line                  int
	suite                 suite
	failed, skipped       bool
	message               string
	body                  []byte
}

type suite struct {
	name, file string
}

// junitParser reads a report token by token, so nested suites and huge
// reports need neither recursion nor the whole document in memory.
type junitParser struct {
	root   string
	rep    junit
	suites []suite
	cur    *testcase
	// depth counts open elements inside the current testcase.
	depth  int
	inBody bool
}

// parseJUnit reads a JUnit report whose paths may start with root, the
// project folder. Anything but a well-formed report with a <testsuites> or
// <testsuite> root is errUnreadable.
func parseJUnit(r io.Reader, root string) (*junit, error) {
	d := xml.NewDecoder(r)
	p := &junitParser{root: filepath.Clean(root)}
	rooted := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) && rooted {
			return &p.rep, nil
		}
		if err != nil {
			return nil, errUnreadable
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !rooted && t.Name.Local != "testsuites" && t.Name.Local != elemSuite {
				return nil, errUnreadable
			}
			rooted = true
			p.start(t)
		case xml.EndElement:
			p.end(t)
		case xml.CharData:
			if p.inBody {
				p.cur.body = append(p.cur.body, t[:min(len(t), maxBody-len(p.cur.body))]...)
			}
		}
	}
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (p *junitParser) start(e xml.StartElement) {
	if p.cur != nil {
		p.depth++
		p.child(e)
		return
	}
	switch e.Name.Local {
	case elemSuite:
		p.suites = append(p.suites, suite{name: attr(e, "name"), file: attr(e, "file")})
	case "testcase":
		c := &testcase{name: attr(e, "name"), classname: attr(e, "classname"), file: attr(e, "file")}
		c.line, _ = strconv.Atoi(attr(e, "line"))
		if len(p.suites) > 0 {
			c.suite = p.suites[len(p.suites)-1]
		}
		p.cur, p.depth = c, 0
	}
}

// child handles an element inside a testcase. Only direct children count:
// surefire nests rerun failures of tests that passed in the end.
func (p *junitParser) child(e xml.StartElement) {
	if p.depth != 1 {
		return
	}
	switch e.Name.Local {
	case "failure", "error":
		if !p.cur.failed {
			p.cur.failed, p.cur.message, p.inBody = true, attr(e, "message"), true
		}
	case "skipped":
		p.cur.skipped = true
	}
}

func (p *junitParser) end(e xml.EndElement) {
	switch {
	case p.cur != nil && p.depth > 0:
		if p.depth == 1 {
			p.inBody = false
		}
		p.depth--
	case p.cur != nil:
		p.finish(p.cur)
		p.cur = nil
	case e.Name.Local == elemSuite && len(p.suites) > 0:
		p.suites = p.suites[:len(p.suites)-1]
	}
}

func (p *junitParser) finish(c *testcase) {
	p.rep.total++
	switch {
	case c.failed:
		p.rep.failed++
		p.rep.cases = append(p.rep.cases, p.failure(c))
	case c.skipped:
		p.rep.skipped++
	}
}

// failure turns a failed testcase into what a summary shows.
func (p *junitParser) failure(c *testcase) Case {
	body := strings.Split(string(c.body), "\n")
	lines := slices.Concat(strings.Split(c.message, "\n"), body)
	for i, l := range lines {
		lines[i] = strings.TrimSpace(cleanLine([]byte(l)))
	}
	frame := p.frame(lines)
	file, line := p.file(c, frame, lines)
	return Case{File: file, Line: line, Name: projectPrefix.ReplaceAllString(c.name, ""), Message: message(c, lines, frame), Frame: frame}
}

// frame is the first path:line in lines that points into the project.
func (p *junitParser) frame(lines []string) string {
	for _, l := range lines {
		if playwrightHeader.MatchString(l) {
			// It points at the test, not at what failed.
			continue
		}
		if m := javaFrame.FindStringSubmatch(l); m != nil {
			if !slices.ContainsFunc(jvmToolchain(), func(prefix string) bool { return strings.HasPrefix(m[1], prefix) }) {
				return m[2] + ":" + m[3]
			}
			continue
		}
		for _, m := range pathFrame.FindAllStringSubmatch(l, -1) {
			if rel, ok := p.inProject(m[1]); ok {
				return rel + ":" + m[2]
			}
		}
	}
	return ""
}

// inProject returns name relative to the project root when it is a
// project file: inside the root and not in a dependency folder.
func (p *junitParser) inProject(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	if path.IsAbs(name) {
		rel, ok := strings.CutPrefix(name, p.root+"/")
		if !ok {
			return "", false
		}
		name = rel
	}
	if !filepath.IsLocal(name) {
		return "", false
	}
	for part := range strings.SplitSeq(name, "/") {
		if slices.Contains(vendored(), part) {
			return "", false
		}
	}
	return name, true
}

// file is the test's file and line. A file attribute names it directly.
// Otherwise the classname may be a module or a path relative to the test
// folder, and the frame in the same file says where it really is.
func (p *junitParser) file(c *testcase, frame string, lines []string) (name string, line int) {
	line = c.line
	name = cmp.Or(c.file, c.suite.file)
	if name != "" {
		if rel, ok := p.inProject(name); ok {
			name = rel
		}
		return name, line
	}
	name = cmp.Or(c.classname, c.suite.name)
	for _, l := range lines {
		if m := playwrightHeader.FindStringSubmatch(l); m != nil && line == 0 {
			name = m[1]
			line, _ = strconv.Atoi(m[2])
			break
		}
	}
	framePath := frame[:max(0, strings.LastIndexByte(frame, ':'))]
	if framePath != "" && (name == "" || strings.ContainsAny(name, " \t") || sameFile(framePath, name)) {
		return framePath, line
	}
	return name, line
}

// sameFile tells whether the project path file is what a report calls
// name: the same path, a path below the test folder, or a dotted module.
func sameFile(file, name string) bool {
	module := strings.ReplaceAll(strings.TrimSuffix(file, path.Ext(file)), "/", ".")
	return file == name || strings.HasSuffix(file, "/"+name) || module == name || strings.HasSuffix(module, "."+name)
}

// message is the first line that says what went wrong. A location the
// frame already shows is cut from its start.
func message(c *testcase, lines []string, frame string) string {
	for _, l := range lines {
		if l == "" || noise.MatchString(l) || playwrightHeader.MatchString(l) || l == c.classname+"::"+c.name {
			continue
		}
		if frame != "" {
			l = strings.TrimSpace(strings.TrimPrefix(l, frame+":"))
		}
		return truncate(l, maxMessage)
	}
	return ""
}

// truncate cuts s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
