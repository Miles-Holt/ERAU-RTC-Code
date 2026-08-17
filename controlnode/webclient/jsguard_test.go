package webclient

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The WebClient is plain browser JS with no build step, no bundler and no test
// runner — there is no Node on the machines this repo is developed on, so
// nothing checks those files before they are served to an operator.
//
// This is not a JS parser and does not pretend to be one. It is a cheap
// structural gate for the two failure modes that have actually shipped here:
//
//   - a file that got truncated or half-written, which serves a 200 with a
//     syntax error and silently disables every feature after the break;
//   - assigning to a global declared `const` in state.js, which throws at the
//     first call and takes the whole handler with it. That bug shipped once and
//     left the operator unable to command any state transition.
//
// Both are catchable without evaluating the language, so they are caught here.

const jsDir = "../../WebClient/js"

// jsSourceFiles lists the hand-written JS. Vendored libraries are skipped:
// they are minified, and minified code defeats the scanner below.
func jsSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(jsDir)
	if err != nil {
		t.Skipf("WebClient/js not present (%v) — nothing to check", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".js") {
			continue
		}
		if strings.HasSuffix(name, ".min.js") {
			continue
		}
		out = append(out, filepath.Join(jsDir, name))
	}
	if len(out) == 0 {
		t.Fatal("no .js sources found — the glob or the directory moved")
	}
	return out
}

// stripJS removes comments and string/template literals, replacing them with
// spaces so byte offsets stay usable. Regex literals are the one genuinely
// ambiguous case in JS ('/' is both division and a regex delimiter); we resolve
// it the way every hand-written scanner does, by looking at the last
// significant character — a regex can only start where a value is expected.
func stripJS(src string) string {
	out := []byte(src)
	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}

	lastSignificant := byte(0)
	lastWord := ""
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				blank(i)
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			for i < len(src) && !(src[i] == '*' && i+1 < len(src) && src[i+1] == '/') {
				blank(i)
				i++
			}
			for j := 0; j < 2 && i < len(src); j++ {
				blank(i)
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			quote := c
			blank(i)
			i++
			for i < len(src) {
				if src[i] == '\\' {
					blank(i)
					i++
					if i < len(src) {
						blank(i)
						i++
					}
					continue
				}
				if src[i] == quote {
					blank(i)
					i++
					break
				}
				blank(i)
				i++
			}
			lastSignificant = 'x' // a string is a value
		case c == '/' && regexCanStartAfter(lastSignificant, lastWord):
			blank(i)
			i++
			for i < len(src) && src[i] != '\n' {
				if src[i] == '\\' {
					blank(i)
					i++
					if i < len(src) {
						blank(i)
						i++
					}
					continue
				}
				if src[i] == '/' {
					blank(i)
					i++
					break
				}
				blank(i)
				i++
			}
			lastSignificant = 'x'
		default:
			if isWordByte(c) {
				start := i
				for i < len(src) && isWordByte(src[i]) {
					i++
				}
				lastWord = src[start:i]
				lastSignificant = src[i-1]
				continue
			}
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				lastSignificant = c
				lastWord = ""
			}
			i++
		}
	}
	return string(out)
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' || c == '_' || c == '$'
}

// regexKeywords are the keywords a regex literal may directly follow. Without
// them a scanner that only inspects the previous character reads `return /re/`
// as a division, because "return" ends in a letter — which is exactly how this
// guard first reported a perfectly good editor.js as truncated.
var regexKeywords = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "in": true, "of": true,
	"new": true, "delete": true, "void": true, "throw": true, "case": true,
	"do": true, "else": true, "yield": true, "await": true,
}

// regexCanStartAfter reports whether a '/' here begins a regex literal rather
// than a division. After a value — identifier, number, closing bracket — '/'
// divides; after an operator, a keyword, or nothing at all, it opens a regex.
func regexCanStartAfter(prev byte, prevWord string) bool {
	if regexKeywords[prevWord] {
		return true
	}
	switch {
	case prev == 0:
		return true
	case isWordByte(prev), prev == ')', prev == ']':
		return false
	}
	return true
}

// TestJSDelimitersBalanced catches truncated or half-written files: an
// unbalanced brace means the file cannot parse, and the browser silently loses
// everything defined after it.
func TestJSDelimitersBalanced(t *testing.T) {
	for _, path := range jsSourceFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			t.Errorf("%s is empty", path)
			continue
		}

		for _, problem := range unbalancedDelimiters(string(raw)) {
			t.Errorf("%s%s", path, problem)
		}
	}
}

// unbalancedDelimiters returns a description of every delimiter problem in src,
// or nil when it is balanced.
func unbalancedDelimiters(src string) []string {
	code := stripJS(src)
	var problems []string
	var stack []rune
	var stackLines []int
	line := 1
	for _, c := range code {
		switch c {
		case '\n':
			line++
		case '{', '(', '[':
			stack = append(stack, c)
			stackLines = append(stackLines, line)
		case '}', ')', ']':
			want := map[rune]rune{'}': '{', ')': '(', ']': '['}[c]
			if len(stack) == 0 {
				problems = append(problems, fmt.Sprintf(":%d: closing %q with nothing open", line, c))
				continue
			}
			if got := stack[len(stack)-1]; got != want {
				problems = append(problems, fmt.Sprintf(
					":%d: closing %q but the innermost open delimiter is %q", line, c, got))
			}
			stack = stack[:len(stack)-1]
			stackLines = stackLines[:len(stackLines)-1]
		}
	}
	if len(stack) != 0 {
		problems = append(problems, fmt.Sprintf(
			": %d delimiter(s) never closed (innermost %q opened at line %d) — file looks truncated",
			len(stack), stack[len(stack)-1], stackLines[len(stackLines)-1]))
	}
	return problems
}

// TestJSGuardItself proves the guard catches what it claims to. A checker that
// has never been seen failing is indistinguishable from one that always passes
// — and the regex/division ambiguity below is exactly what made this guard
// report a healthy editor.js as truncated on its first run.
func TestJSGuardItself(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantBad bool
	}{
		{"balanced", "function f(a) { return a[0]; }\n", false},
		{"truncated function", "function f(a) {\n  if (a) {\n    g();\n", true},
		{"stray close", "function f() {}\n}\n", true},
		{"braces inside a string are not code", "const s = \"{{{\";\n", false},
		{"braces inside a comment are not code", "// {{{\nconst a = 1;\n", false},
		{"braces in a regex after return", "function f(s) { return /[:#{}[\\]]/.test(s); }\n", false},
		{"regex after an operator", "const ok = x || /[{]/.test(y);\n", false},
		{"division is not a regex", "const r = (a + b) / c; const d = e / f;\n", false},
		{"regex after typeof", "if (typeof x === 'string' && /^[{]/.test(x)) { g(); }\n", false},
		{"template literal with braces", "const t = `a${b}c{`;\n", false},
		{"escaped quote does not end the string", "const s = 'it\\'s {'; const u = 1;\n", false},
	}
	for _, c := range cases {
		got := unbalancedDelimiters(c.src)
		if c.wantBad && len(got) == 0 {
			t.Errorf("%s: expected a problem, got none — the guard would miss this", c.name)
		}
		if !c.wantBad && len(got) != 0 {
			t.Errorf("%s: false positive %v on valid JS", c.name, got)
		}
	}
}

var (
	constGlobalRe = regexp.MustCompile(`(?m)^const\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=`)
	// An assignment at the start of a statement: name = ... but not ==, ===,
	// >=, <=, != and not a property/index write (obj.name = / obj[name] =).
	assignRe = regexp.MustCompile(`(^|[^.\w$])([A-Za-z_$][A-Za-z0-9_$]*)\s*=([^=])`)
)

// TestJSNoConstGlobalReassignment catches the bug that shipped: applyStateConfig
// did `machineStateConfig = {}` on a global declared `const` in state.js, which
// throws TypeError on the first state_config message and aborts the handler —
// the operator could not command any state transition, and nothing in CI
// noticed. Clearing such a container in place is fine; rebinding it is not.
func TestJSNoConstGlobalReassignment(t *testing.T) {
	files := jsSourceFiles(t)

	// Only state.js is the shared-globals file. Scanning every file for
	// top-level consts produced false positives: a `const tab` at column 0 in
	// one file collided with an ordinary local named `tab` in another.
	globalsFile := filepath.Join(jsDir, "state.js")
	rawGlobals, err := os.ReadFile(globalsFile)
	if err != nil {
		t.Skipf("no %s to read globals from (%v)", globalsFile, err)
	}
	consts := map[string]string{} // name -> file that declares it
	for _, m := range constGlobalRe.FindAllStringSubmatch(stripJS(string(rawGlobals)), -1) {
		consts[m[1]] = filepath.Base(globalsFile)
	}
	if len(consts) == 0 {
		t.Skip("no top-level const globals found — nothing to guard")
	}

	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		code := stripJS(string(raw))
		// A file that declares its own binding of the name is shadowing the
		// global, not rebinding it.
		shadowed := map[string]bool{}
		for name := range consts {
			local := regexp.MustCompile(`(?m)(^|[^.\w$])(const|let|var|function)\s+` + regexp.QuoteMeta(name) + `\b`)
			if local.MatchString(code) {
				shadowed[name] = true
			}
		}

		for _, problem := range constReassignments(code, consts, shadowed) {
			t.Errorf("%s%s", path, problem)
		}
	}
}

// constReassignments finds statements that rebind a name declared `const`.
func constReassignments(code string, consts map[string]string, shadowed map[string]bool) []string {
	var problems []string
	for lineNo, lineText := range strings.Split(code, "\n") {
		trimmed := strings.TrimSpace(lineText)
		// Skip the declarations themselves.
		if strings.HasPrefix(trimmed, "const ") || strings.HasPrefix(trimmed, "let ") ||
			strings.HasPrefix(trimmed, "var ") {
			continue
		}
		for _, m := range assignRe.FindAllStringSubmatch(lineText, -1) {
			name := m[2]
			decl, isConst := consts[name]
			if !isConst || shadowed[name] {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				":%d: assigns to %q, declared `const` in %s — this throws at runtime and "+
					"aborts the enclosing handler; mutate it in place instead (e.g. delete its keys)",
				lineNo+1, name, decl))
		}
	}
	return problems
}

// TestConstGuardItself replays the bug this check exists for: applyStateConfig
// rebound two containers that state.js declares `const`, which threw on the
// first state_config message and left the operator unable to command any state.
func TestConstGuardItself(t *testing.T) {
	consts := map[string]string{"machineStateConfig": "state.js", "tabs": "state.js"}
	none := map[string]bool{}

	bad := "function applyStateConfig(msg) {\n    machineStateConfig = {};\n}\n"
	if got := constReassignments(bad, consts, none); len(got) == 0 {
		t.Error("the shipped bug (machineStateConfig = {}) was not detected")
	}

	ok := []struct{ name, src string }{
		{"clearing in place is fine", "for (const k of Object.keys(machineStateConfig)) delete machineStateConfig[k];\n"},
		{"writing a property is fine", "machineStateConfig[m.name] = m;\n"},
		{"comparing is not assigning", "if (machineStateConfig === other) { g(); }\n"},
		{"a different name is ignored", "somethingElse = {};\n"},
		{"iterating is fine", "for (const tab of tabs) { g(tab); }\n"},
	}
	for _, c := range ok {
		if got := constReassignments(c.src, consts, none); len(got) != 0 {
			t.Errorf("%s: false positive %v", c.name, got)
		}
	}

	// A file that declares its own binding is shadowing, not rebinding.
	shadow := "function f() {\n    let machineStateConfig = {};\n    machineStateConfig = {};\n}\n"
	if got := constReassignments(shadow, consts, map[string]bool{"machineStateConfig": true}); len(got) != 0 {
		t.Errorf("shadowed local flagged as a const rebind: %v", got)
	}
}
