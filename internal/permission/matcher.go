package permission

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Substitutor handles ${cwd} / ${home} / ~ replacement in user rule
// patterns and target paths. We capture the values once at gate
// construction so all subsequent matches see consistent state — even
// if the agent later issues `/cwd` (which creates a new gate context).
//
// Tests inject a Substitutor with a fixed Cwd so behaviour is
// deterministic regardless of os.Getwd().
type Substitutor struct {
	// Cwd is the absolute path used for ${cwd} substitution. Empty
	// disables ${cwd} expansion (the literal stays in the pattern,
	// which will then never match — fail-closed).
	Cwd string

	// Home is the absolute path used for ${home} and ~ substitution.
	// Defaults to os.UserHomeDir() at construction.
	Home string
}

// NewSubstitutor returns a Substitutor whose Home is os.UserHomeDir()
// and Cwd is the supplied value (empty means "leave unset"). Errors
// from UserHomeDir are intentionally swallowed: callers that want
// strict behaviour should set both fields explicitly.
func NewSubstitutor(cwd string) *Substitutor {
	home, _ := os.UserHomeDir()
	return &Substitutor{Cwd: cwd, Home: home}
}

// Expand replaces every supported variable in s. Variables not
// configured (empty Cwd or Home) are left as-is so the pattern fails
// closed instead of accidentally matching everything.
func (s *Substitutor) Expand(in string) string {
	out := in
	if s.Cwd != "" {
		out = strings.ReplaceAll(out, "${cwd}", s.Cwd)
		out = strings.ReplaceAll(out, "$cwd", s.Cwd) // tolerate the un-braced form
	}
	if s.Home != "" {
		out = strings.ReplaceAll(out, "${home}", s.Home)
		out = strings.ReplaceAll(out, "$home", s.Home)
		// ~ at the very start of the string. We only handle the
		// leading-slash form ("~/") and bare "~"; embedded "~"
		// stays literal because it's a valid filename character.
		if out == "~" {
			out = s.Home
		} else if strings.HasPrefix(out, "~/") {
			out = s.Home + out[1:]
		}
	}
	return out
}

// matchUserRule returns true iff the rule applies to the given
// operation. Substitutor handles variable expansion in the pattern;
// the operation's Path is taken as-is (the agent loop is expected to
// have already absolutized it).
//
// The match logic per granularity:
//   - GranTool:    exact string equality on tool name (no globs).
//   - GranCommand: doublestar.Match on Operation.Command (bash only).
//   - GranPath:    doublestar.Match on Operation.Path after
//                  expanding the rule's pattern.
//
// rules with an explicit ToolName field that doesn't match the
// operation's tool are skipped — this lets users write a single
// allow rule scoped to one tool ("only let read_file see /etc").
func matchUserRule(r *UserRule, op *Operation, sub *Substitutor) bool {
	if r == nil || op == nil {
		return false
	}
	// ToolName scoping: when the rule pinned itself to a particular
	// tool, only that tool triggers the rule.
	if r.ToolName != "" && r.ToolName != op.ToolName {
		return false
	}

	switch r.Granularity {
	case GranTool:
		// Tool-level rules: pattern is the exact tool name. We do
		// not glob here so users don't accidentally allow / deny
		// every tool by writing "*".
		return r.Pattern == op.ToolName

	case GranCommand:
		// Command rules apply only to bash. A rule that omits
		// ToolName but uses GranCommand implicitly scopes to bash
		// (the only tool with a Command field).
		if op.ToolName != "bash" || op.Command == "" {
			return false
		}
		ok, _ := doublestar.Match(r.Pattern, op.Command)
		return ok

	case GranPath:
		if op.Path == "" {
			return false
		}
		expanded := sub.Expand(r.Pattern)
		// We use Match (not PathMatch) so behaviour is identical on
		// Linux and macOS; the agent always normalises paths to
		// forward-slash form before reaching the gate.
		ok, _ := doublestar.Match(expanded, op.Path)
		return ok
	}
	return false
}

// matchHardDeny returns true iff the hard rule applies. Hard rules
// have no variable expansion (they're code-built-in and absolute) and
// scope by ToolName when set; an empty ToolName means "any tool that
// has a Path / Command field".
//
// Bash command matching uses two passes:
//
//  1. Whitespace-normalised literal substring/equality match. This
//     catches patterns containing glob metacharacters that have
//     non-glob meaning in shell syntax (e.g. the fork-bomb pattern
//     ":(){:|:&};:" contains "{" which doublestar interprets as
//     alternation and rejects).
//  2. Doublestar glob match. This handles patterns with intentional
//     wildcards like "rm -rf /*" or "* > /etc/passwd*".
//
// The first pass that succeeds wins; both run because hard rules use
// a mix of styles.
func matchHardDeny(r *HardDenyRule, op *Operation) bool {
	if r == nil || op == nil {
		return false
	}
	if r.ToolName != "" && r.ToolName != op.ToolName {
		return false
	}

	// Bash: match against Command.
	if op.ToolName == "bash" {
		if op.Command == "" {
			return false
		}
		norm := normaliseSpaces(op.Command)
		patt := normaliseSpaces(r.Pattern)

		// Pass 1: literal equality after whitespace normalisation.
		// Used for patterns containing characters doublestar treats
		// as metacharacters but that we want literal — chiefly the
		// fork-bomb spellings ":(){:|:&};:" which contain "{".
		// Using equality (not Contains) so that "rm -rf /" doesn't
		// accidentally fire on "rm -rf /home/user".
		if !patternHasGlobMeta(patt) && norm == patt {
			return true
		}

		// Pass 2: shell-wildcard match. We deliberately do NOT use
		// doublestar here: doublestar's glob engine treats "/" as
		// a path separator and "*" never crosses it, which is the
		// exact opposite of what we want when matching shell
		// commands (URLs, flag values like "of=/dev/sda" all
		// contain "/"). Instead we treat "*" / "**" as a generic
		// "any sequence of characters" wildcard, anchored at both
		// ends — closer to what an operator expects when writing
		// blacklist patterns.
		return matchBashPattern(patt, norm)
	}

	// Other tools: try Path match. If the operation has no Path,
	// the rule simply doesn't apply.
	if op.Path == "" {
		return false
	}
	ok, _ := doublestar.Match(r.Pattern, op.Path)
	if ok {
		return true
	}
	// Also try a normalised-trailing-slash form so "/etc/passwd"
	// catches "/etc/passwd/" if the OS handed us the directory form.
	return strings.TrimSuffix(op.Path, "/") == strings.TrimSuffix(r.Pattern, "/")
}

// patternHasGlobMeta returns true iff the pattern contains characters
// that doublestar treats as glob metacharacters. Used by matchHardDeny
// to decide which matching strategy to apply.
//
// Note: "{" and "}" *are* glob meta in doublestar (alternation), but
// we treat them as literal here because the hard-blacklist patterns
// that contain them (fork bombs) are meant to be matched literally.
func patternHasGlobMeta(p string) bool {
	for _, c := range p {
		switch c {
		case '*', '?', '[':
			return true
		}
	}
	return false
}

// normaliseSpaces collapses every run of ASCII whitespace down to a
// single space so the fork-bomb pattern matches regardless of how
// many spaces the LLM emitted. Non-whitespace bytes are unchanged so
// quoted strings inside commands stay intact.
func normaliseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteByte(c)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

// absorbPath is a convenience for callers that want to feed a relative
// path through the gate. It returns filepath.Abs(p) on success and
// the original p on failure (failing closed: an unresolvable path
// won't accidentally match a glob anchored at /).
func absorbPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// matchBashPattern tests whether `command` matches `pattern` under
// our shell-wildcard semantics. Used exclusively by matchHardDeny
// for bash-class rules.
//
// Differences from doublestar:
//
//   - "*" and "**" are treated identically: each means "any sequence
//     of characters, possibly including '/'". This is what an operator
//     intuitively expects when writing patterns like "curl * | sh*".
//   - The match is anchored at both ends (the whole `command` must
//     match `pattern`), like doublestar.Match. We deliberately do NOT
//     do substring matching — that would let "rm -rf /" fire on
//     "ls && rm -rf / && echo".
//
// We compile each pattern fresh on every call. A fast scan of the
// hard denylist is dominated by the number of rules (~50) times the
// length of `command`; both are small enough that caching the regex
// would be premature optimisation.
//
// Special characters other than "*" are escaped as regexp literal so
// "$HOME", "{", ":" etc. are matched verbatim.
func matchBashPattern(pattern, command string) bool {
	var sb strings.Builder
	sb.WriteByte('^')
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		if c == '*' {
			// Collapse runs of '*' (so "**" behaves identically).
			for i < len(pattern) && pattern[i] == '*' {
				i++
			}
			sb.WriteString(".*")
			continue
		}
		// Escape every regex metachar so the pattern is otherwise
		// matched literally.
		switch c {
		case '.', '+', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			sb.WriteByte('\\')
		}
		sb.WriteByte(c)
		i++
	}
	sb.WriteByte('$')

	re, err := regexp.Compile(sb.String())
	if err != nil {
		// Malformed escape — fail closed. (In practice this branch
		// is unreachable because we escape every metacharacter.)
		return false
	}
	return re.MatchString(command)
}
