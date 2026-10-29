package hosts

import "strings"

// commandLine joins argv into one command line for a POSIX shell. Every
// remote command and the ssh command rsync runs pass through it, so no other
// code builds shell strings.
func commandLine(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = Quote(a)
	}
	return strings.Join(quoted, " ")
}

// Quote makes s one word for a POSIX shell, for remote command lines and for
// the commands burro prints for the user. Plain words stay bare, which keeps
// command lines readable; everything else is single-quoted. Inside single
// quotes only the quote itself needs care: it closes the string, adds an
// escaped quote and reopens it.
func Quote(s string) string {
	if s != "" && strings.IndexFunc(s, unsafeRune) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func unsafeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	default:
		// No "=": a bare first word with it would be a variable assignment.
		return !strings.ContainsRune("@%+:,./_-", r)
	}
}
