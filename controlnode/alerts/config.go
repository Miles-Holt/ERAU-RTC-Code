package alerts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"controlnode/dsl"
)

// TemplateName is the only template the engine knows how to instantiate: one
// copy of its rules per configured daqNode.
const TemplateName = "every_daqnode"

// DefaultStaleMs is the per-daqNode data-receive timeout used when the template
// does not override it with `on stale <duration> -> …`.
const DefaultStaleMs int64 = 2000

// Template event names.
const (
	EventDisconnect = "disconnect"
	EventReconnect  = "reconnect"
	EventBadData    = "bad_data"
	EventStale      = "stale"
)

// Placeholder field names a template message may use in addition to channel
// names.  Rule messages may only use channel names.
const (
	FieldNode   = "node"
	FieldRefDes = "refDes"
	FieldValue  = "value"
)

// Rule is one compiled `alert <NAME>` block.
type Rule struct {
	Name     string
	Cond     dsl.Expr
	Severity string
	Message  string
	Latch    bool
	File     string
	Line     int
}

// ID is the stable registry id for this rule's alert.
func (r *Rule) ID() string { return "rule:" + r.Name }

// TemplateEvent is one `on <event> -> <severity> "<message>"` line.
type TemplateEvent struct {
	Event      string
	Severity   string
	Message    string
	DurationMs int64 // stale only; 0 = use DefaultStaleMs
	Line       int
}

// Template is the compiled `template every_daqnode` block.
type Template struct {
	Name   string
	Events map[string]TemplateEvent
	File   string
	Line   int
}

// StaleMs returns the configured stale timeout, or the default.
func (t *Template) StaleMs() int64 {
	if t == nil {
		return DefaultStaleMs
	}
	if ev, ok := t.Events[EventStale]; ok && ev.DurationMs > 0 {
		return ev.DurationMs
	}
	return DefaultStaleMs
}

// Config is everything parsed from config/alerts/*.alert.
type Config struct {
	Rules    []*Rule
	Template *Template
}

// Source is one .alert file, already read.
type Source struct {
	Name string // file name, used in error messages
	Text string
}

// Options carries the compile-time context: the full set of channel names the
// running config knows about.  Any reference outside it is a startup error, the
// same way .sm and .chan files behave.
type Options struct {
	KnownChannels []string
	// MachineNames is the set of state machines, so `machine.<name>.state`
	// references in rule conditions can be validated.
	MachineNames []string
	// MachineStates maps each machine name to its real state names, so a
	// `machine.<name>.state == "…"` comparison in a rule condition can be
	// checked against them at compile time (a typo'd state name would
	// otherwise be a guard that silently never fires). Optional: a machine
	// missing from this map (or a nil map) simply skips that validation for
	// its comparisons, MachineNames is still what makes the machine itself
	// known.
	MachineStates map[string][]string
}

// LoadDir parses every .alert file in dir.  A missing directory is not an error
// (a system may legitimately run without alert config); a malformed one is.
func LoadDir(dir string, opts Options) (*Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("alerts: read %s: %w", dir, err)
	}

	var srcs []Source
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".alert" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // deterministic order → deterministic error reporting
	for _, name := range names {
		data, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			return nil, fmt.Errorf("alerts: read %s: %w", name, rerr)
		}
		srcs = append(srcs, Source{Name: name, Text: string(data)})
	}
	return Load(srcs, opts)
}

// AlertFiles lists the .alert files in dir (for startup logging).
func AlertFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".alert" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Load compiles a set of .alert sources.
func Load(srcs []Source, opts Options) (*Config, error) {
	known := make(map[string]bool, len(opts.KnownChannels))
	for _, c := range opts.KnownChannels {
		known[c] = true
	}
	machines := make(map[string]bool, len(opts.MachineNames))
	for _, m := range opts.MachineNames {
		machines[m] = true
	}

	cfg := &Config{}
	seenRule := make(map[string]string) // rule name → file that defined it

	for _, src := range srcs {
		decls, err := splitDecls(src)
		if err != nil {
			return nil, err
		}
		for _, d := range decls {
			switch def := d.(type) {
			case *dsl.TemplateDef:
				tmpl, terr := compileTemplate(src.Name, def, known)
				if terr != nil {
					return nil, terr
				}
				if cfg.Template != nil {
					return nil, errf(src.Name, def.LineNo,
						"template %q already defined in %s", def.Name, cfg.Template.File)
				}
				cfg.Template = tmpl

			case *dsl.AlertDef:
				rule, rerr := compileRule(src.Name, def, known, machines, opts.MachineStates)
				if rerr != nil {
					return nil, rerr
				}
				if prev, dup := seenRule[rule.Name]; dup {
					return nil, errf(src.Name, def.LineNo, "alert %q already defined in %s", rule.Name, prev)
				}
				seenRule[rule.Name] = src.Name
				cfg.Rules = append(cfg.Rules, rule)

			default:
				return nil, errf(src.Name, d.Line(), "only `template` and `alert` declarations are allowed in .alert files")
			}
		}
	}
	return cfg, nil
}

// splitDecls tokenizes one file and parses each top-level template/alert block.
// The shared front-end parses one declaration at a time, so the token stream is
// cut at declaration boundaries first (the same approach softchan uses for
// multi-channel .chan files).
func splitDecls(src Source) ([]dsl.Decl, error) {
	tokens, err := dsl.NewLexer(src.Text).Tokenize()
	if err != nil {
		return nil, rewriteErr(err, src.Name)
	}

	var out []dsl.Decl
	pos := 0
	for pos < len(tokens) && tokens[pos].Type != dsl.TOK_EOF {
		t := tokens[pos]
		if t.Type != dsl.TOK_TEMPLATE && t.Type != dsl.TOK_ALERT {
			// Nothing else may appear at the top level of a .alert file.
			if t.Type == dsl.TOK_INDENT || t.Type == dsl.TOK_DEDENT {
				pos++
				continue
			}
			return nil, errf(src.Name, t.Line, "expected `template` or `alert` at the top level of a .alert file")
		}

		start := pos
		declLine := t.Line
		pos++ // keyword
		if pos >= len(tokens) || tokens[pos].Type != dsl.TOK_IDENT {
			return nil, errf(src.Name, declLine, "expected a name after the declaration keyword")
		}
		name := tokens[pos].Value
		pos++

		if pos >= len(tokens) || tokens[pos].Type != dsl.TOK_INDENT {
			return nil, errf(src.Name, declLine, "%q: expected an indented block", name)
		}
		pos++ // INDENT

		depth := 1
		for pos < len(tokens) && depth > 0 {
			switch tokens[pos].Type {
			case dsl.TOK_INDENT:
				depth++
			case dsl.TOK_DEDENT:
				depth--
			}
			pos++
		}

		decl, perr := dsl.Parse(tokens[start:pos])
		if perr != nil {
			return nil, rewriteErr(perr, src.Name)
		}
		out = append(out, decl)
	}
	return out, nil
}

func compileTemplate(file string, def *dsl.TemplateDef, known map[string]bool) (*Template, error) {
	if def.Name != TemplateName {
		return nil, errf(file, def.LineNo,
			"unknown template %q: the only supported template is %q", def.Name, TemplateName)
	}
	if len(def.Rules) == 0 {
		return nil, errf(file, def.LineNo, "template %q: no `on <event>` rules", def.Name)
	}

	tmpl := &Template{
		Name:   def.Name,
		Events: make(map[string]TemplateEvent, len(def.Rules)),
		File:   file,
		Line:   def.LineNo,
	}
	for _, r := range def.Rules {
		switch r.Event {
		case EventDisconnect, EventReconnect, EventBadData, EventStale:
		default:
			return nil, errf(file, r.LineNo,
				"unknown event %q: expected disconnect, reconnect, bad_data or stale", r.Event)
		}
		if _, dup := tmpl.Events[r.Event]; dup {
			return nil, errf(file, r.LineNo, "event %q already declared in template %q", r.Event, def.Name)
		}
		if !ValidSeverity(r.Severity) {
			return nil, errf(file, r.LineNo,
				"unknown severity %q for event %q: expected info, warning or alarm", r.Severity, r.Event)
		}
		if r.DurationMs != 0 && r.Event != EventStale {
			return nil, errf(file, r.LineNo, "event %q does not take a duration (only `stale` does)", r.Event)
		}
		if err := checkPlaceholders(file, r.LineNo, r.Message, known, true); err != nil {
			return nil, err
		}
		tmpl.Events[r.Event] = TemplateEvent{
			Event:      r.Event,
			Severity:   r.Severity,
			Message:    r.Message,
			DurationMs: r.DurationMs,
			Line:       r.LineNo,
		}
	}
	return tmpl, nil
}

func compileRule(file string, def *dsl.AlertDef, known, machines map[string]bool, machineStates map[string][]string) (*Rule, error) {
	if def.Condition == nil {
		return nil, errf(file, def.LineNo, "alert %q: missing `if <expression>`", def.Name)
	}
	if def.Message == "" {
		return nil, errf(file, def.LineNo, "alert %q: missing `message \"…\"`", def.Name)
	}
	if def.Severity == "" {
		return nil, errf(file, def.LineNo, "alert %q: missing `severity <info|warning|alarm>`", def.Name)
	}
	if !ValidSeverity(def.Severity) {
		return nil, errf(file, def.LineNo,
			"alert %q: unknown severity %q: expected info, warning or alarm", def.Name, def.Severity)
	}

	// Unknown channel references are a startup error, never a silent 0 — the
	// same contract .sm and .chan files have.
	checker := dsl.NewChecker(keys(known), func(machine, field string) bool {
		return machines[machine]
	}).WithMachineStates(func(machine string) ([]string, bool) {
		states, ok := machineStates[machine]
		return states, ok
	})
	if res := checker.Check(def); len(res.Errors) > 0 {
		return nil, fmt.Errorf("%s", strings.Replace(res.Errors[0], "file:", file+":", 1))
	}
	if err := checkPlaceholders(file, def.LineNo, def.Message, known, false); err != nil {
		return nil, err
	}

	return &Rule{
		Name:     def.Name,
		Cond:     def.Condition,
		Severity: def.Severity,
		Message:  def.Message,
		Latch:    def.Latch,
		File:     file,
		Line:     def.LineNo,
	}, nil
}

// checkPlaceholders rejects a message that interpolates something the running
// config cannot resolve.  A typo'd {CPT-1} would otherwise reach the operator as
// a literal "?" in the middle of an alarm.
func checkPlaceholders(file string, line int, msg string, known map[string]bool, templateFields bool) error {
	for _, name := range Placeholders(msg) {
		if templateFields {
			switch name {
			case FieldNode, FieldRefDes, FieldValue:
				continue
			}
		}
		if known[name] {
			continue
		}
		if templateFields {
			return errf(file, line, "message placeholder {%s} is not a known channel or event field (node, refDes, value)", name)
		}
		return errf(file, line, "message placeholder {%s} is not a known channel", name)
	}
	return nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func errf(file string, line int, msg string, args ...interface{}) error {
	return fmt.Errorf("%s:%d: %s", file, line, fmt.Sprintf(msg, args...))
}

// rewriteErr replaces the front-end's generic location prefixes with the real
// file name, so every config error reads "<file>:<line>: …".
func rewriteErr(err error, file string) error {
	msg := err.Error()
	msg = strings.Replace(msg, "file:", file+":", 1)
	msg = strings.Replace(msg, "lexer: line ", file+":", 1)
	return fmt.Errorf("%s", msg)
}
