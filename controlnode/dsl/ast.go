package dsl

// ── Expressions ───────────────────────────────────────────────────────────

// Expr is the interface for all expression nodes.
type Expr interface {
	expr()
	Line() int
}

// BinaryExpr represents a binary operation: left op right.
type BinaryExpr struct {
	Left   Expr
	Op     string // "+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "and", "or"
	Right  Expr
	LineNo int
}

func (e *BinaryExpr) expr()  {}
func (e *BinaryExpr) Line() int { return e.LineNo }

// UnaryExpr represents a unary operation: op operand.
type UnaryExpr struct {
	Op       string // "-", "not"
	Operand  Expr
	LineNo   int
}

func (e *UnaryExpr) expr()  {}
func (e *UnaryExpr) Line() int { return e.LineNo }

// LiteralExpr represents a literal value.
type LiteralExpr struct {
	Value  interface{} // int64, float64, bool, or string
	LineNo int
}

func (e *LiteralExpr) expr()  {}
func (e *LiteralExpr) Line() int { return e.LineNo }

// IdentExpr represents a simple identifier or member access (e.g., "x" or "machine.fuelSeq.state").
type IdentExpr struct {
	Name   string // full dotted name
	LineNo int
}

func (e *IdentExpr) expr()  {}
func (e *IdentExpr) Line() int { return e.LineNo }

// ── Statements ────────────────────────────────────────────────────────────

// Stmt is the interface for all statement nodes.
type Stmt interface {
	stmt()
	Line() int
}

// AssignStmt represents an assignment: target = expr.
type AssignStmt struct {
	Target string // identifier
	Value  Expr
	LineNo int
}

func (s *AssignStmt) stmt()  {}
func (s *AssignStmt) Line() int { return s.LineNo }

// IncrementStmt represents an increment: target++.
type IncrementStmt struct {
	Target string
	LineNo int
}

func (s *IncrementStmt) stmt()  {}
func (s *IncrementStmt) Line() int { return s.LineNo }

// DecrementStmt represents a decrement: target--.
type DecrementStmt struct {
	Target string
	LineNo int
}

func (s *DecrementStmt) stmt()  {}
func (s *DecrementStmt) Line() int { return s.LineNo }

// CompoundAssignStmt represents a compound assignment: target += expr or
// target -= expr.  This is deliberately its own node rather than being
// desugared into an AssignStmt{Target, BinaryExpr{Target, Op, expr}} at parse
// time: format.go reconstructs source from the AST, and desugaring here would
// silently rewrite a user's `T-TIME += CYCLE_TIME` into
// `T-TIME = T-TIME + CYCLE_TIME` the next time the file was formatted.  One
// node with an Op field (rather than separate AddAssignStmt/SubAssignStmt
// types) keeps the many switches over statement kinds (check, format, engine,
// sequence execution) from needing two more near-duplicate cases each.
type CompoundAssignStmt struct {
	Target string
	Op     string // "+=" or "-="
	Value  Expr
	LineNo int
}

func (s *CompoundAssignStmt) stmt()  {}
func (s *CompoundAssignStmt) Line() int { return s.LineNo }

// IfStmt represents an if/elif/else statement.
type IfStmt struct {
	Condition Expr
	Body      []Stmt
	Elif      []*IfStmt // nil condition means else
	LineNo    int
}

func (s *IfStmt) stmt()  {}
func (s *IfStmt) Line() int { return s.LineNo }

// TransitionStmt represents a transition to another state.
type TransitionStmt struct {
	Target string // state name
	LineNo int
}

func (s *TransitionStmt) stmt()  {}
func (s *TransitionStmt) Line() int { return s.LineNo }

// CommandStmt represents a cross-machine command: command <machine> -> <state>.
// Unlike TransitionStmt (same machine, no authority check) and unlike an
// operator's SM-<NAME>-TARGET write (gated by the `operator` flag and any
// `operator from` list), a CommandStmt is the issuing machine's own logic
// commanding another machine directly: it bypasses the operator flag and gate
// entirely.  Both Machine and State are validated at compile time (unknown
// machine, unknown state, and self-command are all compile errors — see
// statemachine/compile.go's checkCommands).
type CommandStmt struct {
	Machine string // target machine name
	Target  string // target state name
	LineNo  int
}

func (s *CommandStmt) stmt()  {}
func (s *CommandStmt) Line() int { return s.LineNo }

// SleepStmt represents a sleep: sleep <duration or expr>.
type SleepStmt struct {
	Duration Expr // evaluates to float64 seconds
	LineNo   int
}

func (s *SleepStmt) stmt()  {}
func (s *SleepStmt) Line() int { return s.LineNo }

// WaitUntilStmt represents a wait_until with optional timeout.
type WaitUntilStmt struct {
	Condition      Expr
	Timeout        Expr    // optional; evaluates to float64 seconds
	TimeoutState   string  // optional; state to transition to on timeout
	LineNo         int
}

func (s *WaitUntilStmt) stmt()  {}
func (s *WaitUntilStmt) Line() int { return s.LineNo }

// ── Top-level declarations ────────────────────────────────────────────────

// Decl is the interface for all top-level declarations.
type Decl interface {
	decl()
	Line() int
}

// MachineDef defines a state machine.
type MachineDef struct {
	Name   string
	States []*StateDef
	LineNo int
}

func (d *MachineDef) decl()  {}
func (d *MachineDef) Line() int { return d.LineNo }

// StateDef defines a state within a machine.
type StateDef struct {
	Name     string
	Operator bool

	// OperatorFrom is the optional gate list of `operator from a, b`: the state
	// may only be operator-commanded while the machine is in one of these
	// states.  Empty means the `operator` flag is ungated (commandable from any
	// state), which is the original behaviour.  OperatorFromLine is the source
	// line the list was written on, for compile errors about it.
	OperatorFrom     []string
	OperatorFromLine int

	DaqLocal   string // optional daqNode name
	Controller []Stmt
	Sequence   []Stmt
	AbortRules []*AbortRule
	LineNo     int

	// AbortSequence is the `abort_sequence` block of a daq_local state: timed
	// set-steps the DAQ runs locally when an abort_rule trips, plus a trailing
	// `transition X` naming the abort destination.  HasAbortSequence
	// distinguishes "declared but empty" from "not declared".
	AbortSequence    []Stmt
	HasAbortSequence bool
	AbortSeqLine     int
}

func (d *StateDef) decl()  {}
func (d *StateDef) Line() int { return d.LineNo }

// AbortRule represents an abort_rule declaration.
type AbortRule struct {
	Channel  string // channel name
	Op       string // ">", "<", ">=", "<=", "==", "!="
	Value    Expr   // evaluates to float64
	FromMs   Expr   // evaluates to float64 seconds (name kept for wire/history; the DAQ-ms conversion happens only at daq_local serialization)
	ToMs     Expr   // evaluates to float64 seconds (see FromMs)
	LineNo   int
}

func (d *AbortRule) decl()  {}
func (d *AbortRule) Line() int { return d.LineNo }

// ChannelDef defines a software channel.
type ChannelDef struct {
	Name        string
	Type        string      // "float", "bool", or omitted for compute
	Default     *LiteralExpr
	Min         *LiteralExpr
	Max         *LiteralExpr
	Units       string
	Description string
	Compute     Expr // optional; read-only if present
	LineNo      int
}

func (d *ChannelDef) decl()  {}
func (d *ChannelDef) Line() int { return d.LineNo }

// TemplateDef defines an alert template.
type TemplateDef struct {
	Name  string
	Rules []*TemplateRule
	LineNo int
}

func (d *TemplateDef) decl()  {}
func (d *TemplateDef) Line() int { return d.LineNo }

// TemplateRule represents an "on <event> [<duration>] -> <severity> <message>" rule.
//
// The optional duration qualifies the event.  Only `stale` uses it today, where
// it overrides the default data-receive timeout (`on stale 5s -> …`).
// DurationMs is 0 when no duration was given.
type TemplateRule struct {
	Event      string // "disconnect", "reconnect", "bad_data", "stale"
	Severity   string // "alarm", "warning", "info"
	Message    string
	DurationMs int64 // 0 = not specified
	LineNo     int
}

// AlertDef defines an alert rule.
type AlertDef struct {
	Name     string
	Condition Expr
	Severity string
	Message  string
	Description string // optional long form; "" when the alert has no `describe`
	Latch    bool
	// PlotChannels and Lines are the optional `channels` block (item 09),
	// declaring what the alarm panel plots for this alert — nil/nil when the
	// alert has none, in which case the panel falls back to the condition's
	// own attributed channels (item 07's default). Only `alert` blocks carry
	// this; the `every_daqnode` template does not (see LineDef's doc for why).
	PlotChannels []string
	Lines        []*LineDef
	LineNo   int
}

func (d *AlertDef) decl()  {}
func (d *AlertDef) Line() int { return d.LineNo }

// LineDef is one `line <value-or-channel> "<label>"` declaration inside an
// alert's `channels` block (item 09). Value is left as a raw Expr here — the
// dsl package parses grammar, it doesn't know about "channels" semantics;
// classifying Value into a literal number vs. a channel reference happens in
// controlnode/alerts (compileRule), the same division of labor Condition
// already has (dsl parses/type-checks the expression shape, alerts
// interprets what a channel name in THIS position means).
type LineDef struct {
	Value  Expr
	Label  string
	LineNo int
}
