// Package statemachine compiles `.sm` DSL files into executable machine
// structures and runs them all on a single engine tick loop.
//
// The front-end (lexing, parsing, expression evaluation, reference checking)
// lives in package dsl; this package owns the semantic checks that need whole-
// program knowledge (transition targets, operator flags, daq_local
// reducibility) plus the runtime engine in engine.go.
package statemachine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"controlnode/dsl"
)

// ── Compiled program ──────────────────────────────────────────────────────────

// Source is one `.sm` file to compile.  Name is used in error messages.
type Source struct {
	Name string
	Text string
}

// Options controls compilation.
type Options struct {
	// KnownChannels is the set of channel refDes the machines may reference.
	// A nil slice disables channel-reference checking entirely, which is useful
	// for unit tests; production callers should always pass the full channel
	// space so unknown refDes are caught at startup.
	KnownChannels []string
}

// State is one compiled state of a machine.
type State struct {
	Name          string
	Operator      bool   // operator may command entry to this state
	DaqLocal      string // daqNode name, or "" when the state runs in the controlnode
	Controller    []dsl.Stmt
	Sequence      []dsl.Stmt
	AbortSequence []dsl.Stmt
	AbortRules    []*dsl.AbortRule
	Index         int // position in the file; 0 is the initial state

	daqLocal         *DaqLocalState // pre-resolved expressions for daq_local states
	CompletionTarget string         // for daq_local states: target state on sequence_complete; "" if none
	AbortTarget      string         // for daq_local states: target state on abort_triggered; "" if none

	// operatorFrom is the `operator from a, b` gate list: the states an
	// operator may command THIS state from.  Empty means ungated — commandable
	// from any state, the pre-gating behaviour.  It restricts operator input
	// only: `transition` statements, DAQ-reported aborts and completions are
	// never gated.  operatorFromLine is the source line, for compile errors.
	operatorFrom     []string
	operatorFromLine int
}

// OperatorFrom returns the states this state may be operator-commanded from.
// An empty result means the state is ungated: either it is not operator-
// commandable at all (check Operator), or it is commandable from any state.
// The returned slice is a copy, so callers cannot mutate the compiled program.
func (s *State) OperatorFrom() []string {
	if len(s.operatorFrom) == 0 {
		return nil
	}
	out := make([]string, len(s.operatorFrom))
	copy(out, s.operatorFrom)
	return out
}

// operatorCommandableFrom reports whether an operator may command entry to this
// state while the machine is in the state named cur.  Ungated states accept any
// current state.
func (s *State) operatorCommandableFrom(cur string) bool {
	if len(s.operatorFrom) == 0 {
		return true
	}
	for _, name := range s.operatorFrom {
		if name == cur {
			return true
		}
	}
	return false
}

// HasDaqLocal returns true if this state has a daq_local definition that needs resolution.
func (s *State) HasDaqLocal() bool { return s.daqLocal != nil }

// resolvePayload resolves this state's daq_local payload against reader.
func (s *State) resolvePayload(reader Reader) (*DaqStateUpdate, error) {
	if s.daqLocal == nil {
		return nil, fmt.Errorf("state %q is not daq_local", s.Name)
	}
	return resolveDaqLocalState(s.daqLocal, reader)
}

// Machine is one compiled state machine.
type Machine struct {
	Name    string
	Source  string // file name the machine was compiled from
	States  []*State
	Initial *State

	byName map[string]*State
}

// State looks up a state by name.
func (m *Machine) State(name string) (*State, bool) {
	s, ok := m.byName[name]
	return s, ok
}

// DaqStateUpdates returns the resolved DAQ payloads for this machine grouped
// by daqNode name, resolving all identifiers at send-time using the provided Reader.
// Machines with no daq_local states return an empty map.
func (m *Machine) DaqStateUpdates(reader Reader) (map[string][]*DaqStateUpdate, error) {
	out := make(map[string][]*DaqStateUpdate)
	for _, st := range m.States {
		if st.daqLocal == nil {
			continue
		}
		resolved, err := resolveDaqLocalState(st.daqLocal, reader)
		if err != nil {
			return nil, fmt.Errorf("state %q: %v", st.Name, err)
		}
		resolved.Machine = m.Name
		out[st.DaqLocal] = append(out[st.DaqLocal], resolved)
	}
	return out, nil
}

// Program is the full set of compiled machines.
type Program struct {
	Machines []*Machine

	byName map[string]*Machine
}

// Machine looks up a machine by name.
func (p *Program) Machine(name string) (*Machine, bool) {
	m, ok := p.byName[name]
	return m, ok
}

// ── Loading ───────────────────────────────────────────────────────────────────

// LoadDir compiles every `*.sm` file in dir (non-recursive).
func LoadDir(dir string, opts Options) (*Program, error) {
	paths, err := SMFiles(dir) // sorted → deterministic machine order
	if err != nil {
		return nil, err
	}
	return LoadFiles(paths, opts)
}

// SMFiles lists the `*.sm` files in dir (non-recursive, sorted).
func SMFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read machines dir: %w", err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sm" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

// ScanMachineNames returns the machine name declared by each `.sm` file in dir
// WITHOUT compiling it.  The integration layer needs the names before it can
// compile: the auto-generated SM-<NAME>-STATE / SM-<NAME>-TARGET channels have
// to exist in the known-channel set, or a machine that references another
// machine's channels fails to compile.
func ScanMachineNames(dir string) ([]string, error) {
	paths, err := SMFiles(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		toks, err := dsl.NewLexer(string(data)).Tokenize()
		if err != nil {
			return nil, rewriteFile(err, filepath.Base(p))
		}
		for i := 0; i+1 < len(toks); i++ {
			if toks[i].Type == dsl.TOK_MACHINE && toks[i+1].Type == dsl.TOK_IDENT {
				names = append(names, toks[i+1].Value)
				break
			}
		}
	}
	return names, nil
}

// LoadFiles compiles the named `.sm` files.
func LoadFiles(paths []string, opts Options) (*Program, error) {
	srcs := make([]Source, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		srcs = append(srcs, Source{Name: filepath.Base(p), Text: string(data)})
	}
	return Compile(srcs, opts)
}

// ── Compilation ───────────────────────────────────────────────────────────────

// Compile parses and checks the given sources and returns the executable
// program.  All errors found are reported together, one per line, each prefixed
// with `file:line:`.
func Compile(sources []Source, opts Options) (*Program, error) {
	c := &compiler{opts: opts}

	// Pass 1 — parse every file so machine names are known before checking.
	defs := make([]*dsl.MachineDef, 0, len(sources))
	files := make([]string, 0, len(sources))
	for _, src := range sources {
		def, err := parseMachine(src)
		if err != nil {
			c.errs = append(c.errs, err.Error())
			continue
		}
		defs = append(defs, def)
		files = append(files, src.Name)
	}
	if len(c.errs) > 0 {
		return nil, c.err()
	}

	prog := &Program{byName: make(map[string]*Machine, len(defs))}
	for i, def := range defs {
		file := files[i]
		if prev, ok := prog.byName[def.Name]; ok {
			c.errorf(file, def.LineNo, "machine %q already defined in %s", def.Name, prev.Source)
			continue
		}
		m := c.buildMachine(file, def)
		if m == nil {
			continue
		}
		prog.byName[m.Name] = m
		prog.Machines = append(prog.Machines, m)
	}
	if len(c.errs) > 0 {
		return nil, c.err()
	}

	// Pass 2 — whole-program checks: transition targets and channel references.
	for i, def := range defs {
		m := prog.Machines[i]
		c.checkTargets(m)
		c.checkOperatorGates(m)
		c.checkReferences(prog, m, def)
	}
	if len(c.errs) > 0 {
		return nil, c.err()
	}
	return prog, nil
}

type compiler struct {
	opts Options
	errs []string
}

func (c *compiler) errorf(file string, line int, msg string, args ...interface{}) {
	c.errs = append(c.errs, fmt.Sprintf("%s:%d: %s", file, line, fmt.Sprintf(msg, args...)))
}

func (c *compiler) err() error {
	return fmt.Errorf("%s", strings.Join(c.errs, "\n"))
}

// parseMachine lexes and parses one source, rewriting the front-end's generic
// "file:" prefix to the real file name.
func parseMachine(src Source) (*dsl.MachineDef, error) {
	toks, err := dsl.NewLexer(src.Text).Tokenize()
	if err != nil {
		return nil, rewriteFile(err, src.Name)
	}
	decl, err := dsl.Parse(toks)
	if err != nil {
		return nil, rewriteFile(err, src.Name)
	}
	def, ok := decl.(*dsl.MachineDef)
	if !ok {
		return nil, fmt.Errorf("%s:1: expected a machine definition, got %T", src.Name, decl)
	}
	return def, nil
}

func rewriteFile(err error, name string) error {
	msg := err.Error()
	msg = strings.Replace(msg, "file:", name+":", 1)
	msg = strings.Replace(msg, "lexer: line ", name+":", 1)
	return fmt.Errorf("%s", msg)
}

// buildMachine performs the per-machine structural checks and produces the
// runtime structure.  Returns nil when the machine could not be built.
func (c *compiler) buildMachine(file string, def *dsl.MachineDef) *Machine {
	before := len(c.errs)

	m := &Machine{
		Name:   def.Name,
		Source: file,
		byName: make(map[string]*State, len(def.States)),
	}

	for i, sd := range def.States {
		if _, dup := m.byName[sd.Name]; dup {
			c.errorf(file, sd.LineNo, "state %q already defined in machine %q", sd.Name, def.Name)
			continue
		}
		st := &State{
			Name:             sd.Name,
			Operator:         sd.Operator,
			operatorFrom:     sd.OperatorFrom,
			operatorFromLine: sd.OperatorFromLine,

			DaqLocal:      sd.DaqLocal,
			Controller:    sd.Controller,
			Sequence:      sd.Sequence,
			AbortSequence: sd.AbortSequence,
			AbortRules:    sd.AbortRules,
			Index:         i,
		}

		// controller: straight-line only — no blocking statements.
		c.checkController(file, def.Name, st, st.Controller)
		// sequence: timeouts must name a fallback state.
		c.checkSequence(file, def.Name, st, st.Sequence)

		if st.DaqLocal == "" && len(st.AbortRules) > 0 {
			c.errorf(file, sd.LineNo,
				"state %q: abort_rule requires daq_local (rules are evaluated on the DAQ node)", st.Name)
		}
		if st.DaqLocal == "" && sd.HasAbortSequence {
			c.errorf(file, sd.AbortSeqLine,
				"state %q: abort_sequence requires daq_local (the sequence runs on the DAQ node)", st.Name)
		}
		if st.DaqLocal != "" {
			daqLocal, err := compileDaqLocal(file, sd, st)
			if err != nil {
				c.errs = append(c.errs, err.Error())
			} else {
				st.daqLocal = daqLocal
			}
		}

		m.byName[st.Name] = st
		m.States = append(m.States, st)
	}

	if len(m.States) == 0 {
		c.errorf(file, def.LineNo, "machine %q: no states defined", def.Name)
		return nil
	}
	m.Initial = m.States[0] // first state in the file is the initial state

	if len(c.errs) > before {
		// Keep the machine so later passes can still report useful errors, but
		// compilation as a whole has already failed.
		return m
	}
	return m
}

// checkController rejects statements that cannot run inside a per-tick block.
func (c *compiler) checkController(file, machine string, st *State, stmts []dsl.Stmt) {
	for _, s := range stmts {
		switch v := s.(type) {
		case *dsl.SleepStmt:
			c.errorf(file, v.LineNo, "state %q controller: sleep is not allowed (controllers run every tick)", st.Name)
		case *dsl.WaitUntilStmt:
			c.errorf(file, v.LineNo, "state %q controller: wait_until is not allowed (controllers run every tick)", st.Name)
		case *dsl.IfStmt:
			c.checkController(file, machine, st, v.Body)
			for _, e := range v.Elif {
				c.checkController(file, machine, st, e.Body)
			}
		}
	}
}

// checkSequence rejects `wait_until … timeout <d>` without a `-> state` target,
// which would otherwise have no defined behaviour on expiry.
func (c *compiler) checkSequence(file, machine string, st *State, stmts []dsl.Stmt) {
	for _, s := range stmts {
		switch v := s.(type) {
		case *dsl.WaitUntilStmt:
			if v.Timeout != nil && v.TimeoutState == "" {
				c.errorf(file, v.LineNo, "state %q: wait_until timeout requires \"-> <state>\"", st.Name)
			}
		case *dsl.IfStmt:
			c.checkSequence(file, machine, st, v.Body)
			for _, e := range v.Elif {
				c.checkSequence(file, machine, st, e.Body)
			}
		}
	}
}

// checkTargets verifies every transition / wait_until timeout names a real state.
func (c *compiler) checkTargets(m *Machine) {
	var walk func(stmts []dsl.Stmt)
	walk = func(stmts []dsl.Stmt) {
		for _, s := range stmts {
			switch v := s.(type) {
			case *dsl.TransitionStmt:
				if _, ok := m.byName[v.Target]; !ok {
					c.errorf(m.Source, v.LineNo, "transition to unknown state %q in machine %q", v.Target, m.Name)
				}
			case *dsl.WaitUntilStmt:
				if v.TimeoutState != "" {
					if _, ok := m.byName[v.TimeoutState]; !ok {
						c.errorf(m.Source, v.LineNo, "wait_until timeout target %q is not a state of machine %q", v.TimeoutState, m.Name)
					}
				}
			case *dsl.IfStmt:
				walk(v.Body)
				for _, e := range v.Elif {
					walk(e.Body)
				}
			}
		}
	}
	for _, st := range m.States {
		walk(st.Controller)
		walk(st.Sequence)
		walk(st.AbortSequence) // the abort destination must exist too
	}
}

// checkOperatorGates validates every `operator from a, b` list.  It runs as a
// whole-machine pass because a gate may name a state declared later in the file.
func (c *compiler) checkOperatorGates(m *Machine) {
	for _, st := range m.States {
		if len(st.operatorFrom) == 0 {
			continue
		}
		line := st.operatorFromLine
		if !st.Operator {
			// Unreachable through the parser (it rejects a bare `from` line);
			// kept so a programmatically built AST cannot slip a dead gate past.
			c.errorf(m.Source, line,
				"state %q: \"from\" requires the operator flag", st.Name)
			continue
		}
		seen := make(map[string]bool, len(st.operatorFrom))
		for _, name := range st.operatorFrom {
			switch {
			case name == st.Name:
				c.errorf(m.Source, line,
					"state %q: \"operator from\" may not list the state itself "+
						"(re-entry by operator command is not a transition anyone declares)", st.Name)
			case seen[name]:
				c.errorf(m.Source, line,
					"state %q: duplicate state %q in \"operator from\"", st.Name, name)
			default:
				seen[name] = true
				if _, ok := m.byName[name]; !ok {
					c.errorf(m.Source, line,
						"state %q: \"operator from\" names %q, which is not a state of machine %q",
						st.Name, name, m.Name)
				}
			}
		}
	}
}

// checkReferences runs the dsl reference checker over one machine.
func (c *compiler) checkReferences(prog *Program, m *Machine, def *dsl.MachineDef) {
	if c.opts.KnownChannels == nil {
		return
	}
	checker := dsl.NewChecker(c.opts.KnownChannels, func(machine, field string) bool {
		_, ok := prog.byName[machine]
		return ok && field == "state"
	})
	res := checker.Check(def)
	for _, e := range res.Errors {
		c.errs = append(c.errs, strings.Replace(e, "file:", m.Source+":", 1))
	}
}
