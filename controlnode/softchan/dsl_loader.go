package softchan

import (
	"controlnode/dsl"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadFromDir loads all .chan files from the given directory and returns a Store.
func LoadFromDir(chanDir, valuesPath string) (*Store, error) {
	// For now, we'll look for all .chan files; typically should be config/channels/softchannels.chan
	files, err := os.ReadDir(chanDir)
	if err != nil {
		return nil, fmt.Errorf("softchan: read channels dir %s: %w", chanDir, err)
	}

	// Parse all .chan files and collect channel definitions
	allDefs := make(map[string]*chanDef)
	allComputeDefs := make(map[string]*computeChannelDef)

	for _, f := range files {
		if !f.IsDir() && filepath.Ext(f.Name()) == ".chan" {
			fpath := filepath.Join(chanDir, f.Name())
			data, err := os.ReadFile(fpath)
			if err != nil {
				return nil, fmt.Errorf("softchan: read %s: %w", fpath, err)
			}

			if err := loadChanFile(f.Name(), string(data), allDefs, allComputeDefs); err != nil {
				// Errors already carry "<file>:<line>: …" like .sm files do.
				return nil, fmt.Errorf("softchan: %w", err)
			}
		}
	}

	// Check for cycles in computed channels
	if err := detectCycles(allComputeDefs); err != nil {
		return nil, fmt.Errorf("softchan: cycle detection: %w", err)
	}

	// Load persisted values
	persisted := make(map[string]float64)
	valData, err := os.ReadFile(valuesPath)
	if err == nil {
		var vf yamlValuesFile
		if err := yaml.Unmarshal(valData, &vf); err == nil && vf.Values != nil {
			persisted = vf.Values
		}
	}

	// Build the store
	s := &Store{
		defsPath:     chanDir,
		valuesPath:   valuesPath,
		defIndex:     make(map[string]int),
		values:       make(map[string]float64),
		computeExprs: make(map[string]dsl.Expr),
		computeOrder: []string{},
	}

	// Add all regular channels (in sorted order for deterministic behavior)
	var channelNames []string
	for name := range allDefs {
		channelNames = append(channelNames, name)
	}
	sort.Strings(channelNames)

	idx := 0
	for _, name := range channelNames {
		d := allDefs[name]
		s.defs = append(s.defs, *d)
		s.defIndex[name] = idx

		// Set value: use persisted if available, otherwise default
		if v, ok := persisted[name]; ok {
			s.values[name] = v
		} else {
			s.values[name] = d.Default
		}
		idx++
	}

	// Add computed channels (will be populated via Recompute)
	s.computeMeta = make(map[string]computeMeta, len(allComputeDefs))
	for name := range allComputeDefs {
		s.values[name] = 0 // initial value
		s.computeExprs[name] = allComputeDefs[name].expr
		s.computeMeta[name] = computeMeta{
			Units:       allComputeDefs[name].units,
			Description: allComputeDefs[name].description,
		}
	}

	// Topologically sort computed channels
	s.computeOrder = topologicalSort(allComputeDefs)

	return s, nil
}

// computeChannelDef is an internal struct for tracking computed channels
type computeChannelDef struct {
	name string
	expr dsl.Expr
	deps map[string]bool // channel names this depends on
	// units / description are not needed to evaluate the channel, but the
	// /docs pages render them, so they are carried through the loader.
	units       string
	description string
}

// chanErrf formats a loader error with the same "<file>:<line>: message" shape
// the .sm compiler produces, so config problems read identically everywhere.
func chanErrf(file string, line int, msg string, args ...interface{}) error {
	return fmt.Errorf("%s:%d: %s", file, line, fmt.Sprintf(msg, args...))
}

// rewriteChanErr rewrites the front-end's generic prefixes to the real file name.
func rewriteChanErr(err error, file string) error {
	msg := err.Error()
	msg = strings.Replace(msg, "file:", file+":", 1)
	msg = strings.Replace(msg, "lexer: line ", file+":", 1)
	return fmt.Errorf("%s", msg)
}

// loadChanFile parses a .chan file which may contain multiple channel declarations
func loadChanFile(file, content string, defs map[string]*chanDef, computeDefs map[string]*computeChannelDef) error {
	lexer := dsl.NewLexer(content)
	tokens, err := lexer.Tokenize()
	if err != nil {
		return rewriteChanErr(err, file)
	}

	// Parse all channel declarations
	pos := 0
	for pos < len(tokens) && tokens[pos].Type != dsl.TOK_EOF {
		// Find the next channel keyword
		if tokens[pos].Type != dsl.TOK_CHANNEL {
			pos++
			continue
		}

		// Find the extent of this channel declaration
		// It spans from TOK_CHANNEL to the matching TOK_DEDENT
		start := pos
		declLine := tokens[pos].Line
		pos++ // skip TOK_CHANNEL
		if pos >= len(tokens) || tokens[pos].Type != dsl.TOK_IDENT {
			return chanErrf(file, declLine, "channel: expected a channel name")
		}
		name := tokens[pos].Value
		pos++ // skip name

		// Skip to INDENT
		for pos < len(tokens) && tokens[pos].Type != dsl.TOK_INDENT {
			pos++
		}
		if pos >= len(tokens) {
			return chanErrf(file, declLine, "channel %q: expected an indented block", name)
		}
		pos++ // skip INDENT

		// Find the matching DEDENT
		indentLevel := 1
		for pos < len(tokens) && indentLevel > 0 {
			if tokens[pos].Type == dsl.TOK_INDENT {
				indentLevel++
			} else if tokens[pos].Type == dsl.TOK_DEDENT {
				indentLevel--
			}
			pos++
		}

		// Extract the tokens for this channel
		chanTokens := tokens[start:pos]

		// Parse this channel using dsl.Parse
		decl, err := dsl.Parse(chanTokens)
		if err != nil {
			return rewriteChanErr(err, file)
		}

		if channelDef, ok := decl.(*dsl.ChannelDef); ok {
			if _, dup := defs[channelDef.Name]; dup {
				return chanErrf(file, channelDef.LineNo, "channel %q already defined", channelDef.Name)
			}
			if _, dup := computeDefs[channelDef.Name]; dup {
				return chanErrf(file, channelDef.LineNo, "channel %q already defined", channelDef.Name)
			}
			if channelDef.Compute != nil {
				// Computed channel
				cd := &computeChannelDef{
					name:        channelDef.Name,
					expr:        channelDef.Compute,
					deps:        extractChannelDeps(channelDef.Compute),
					units:       channelDef.Units,
					description: channelDef.Description,
				}
				computeDefs[channelDef.Name] = cd
			} else {
				// Regular channel
				d := &chanDef{
					RefDes:      channelDef.Name,
					Description: channelDef.Description,
					Units:       channelDef.Units,
					Role:        channelDef.Type, // will be "float" or "bool"
					Default:     extractDefaultValue(channelDef.Default),
					Min:         extractLiteralFloat(channelDef.Min),
					Max:         extractLiteralFloat(channelDef.Max),
				}
				if d.Role != "" {
					// Map type to role convention
					if d.Role == "float" {
						d.Role = "cmd-float"
					} else if d.Role == "bool" {
						d.Role = "cmd-bool"
					}
				}
				defs[channelDef.Name] = d
			}
		}
	}

	return nil
}

// extractChannelDeps extracts all channel identifiers from an expression
func extractChannelDeps(expr dsl.Expr) map[string]bool {
	deps := make(map[string]bool)
	walkExpr(expr, deps)
	return deps
}

// walkExpr recursively extracts identifiers from an expression
func walkExpr(expr dsl.Expr, deps map[string]bool) {
	if expr == nil {
		return
	}
	switch e := expr.(type) {
	case *dsl.IdentExpr:
		deps[e.Name] = true
	case *dsl.BinaryExpr:
		walkExpr(e.Left, deps)
		walkExpr(e.Right, deps)
	case *dsl.UnaryExpr:
		walkExpr(e.Operand, deps)
	}
}

// extractDefaultValue extracts a float64 from a LiteralExpr
func extractDefaultValue(lit *dsl.LiteralExpr) float64 {
	if lit == nil {
		return 0
	}
	switch v := lit.Value.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case bool:
		if v {
			return 1
		}
		return 0
	}
	return 0
}

// extractLiteralFloat extracts a float64 pointer from a LiteralExpr
func extractLiteralFloat(lit *dsl.LiteralExpr) *float64 {
	if lit == nil {
		return nil
	}
	v := extractDefaultValue(lit)
	return &v
}

// detectCycles checks for dependency cycles in computed channels
func detectCycles(defs map[string]*computeChannelDef) error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var visit func(string) error
	visit = func(name string) error {
		if recStack[name] {
			return fmt.Errorf("cycle detected: %q", name)
		}
		if visited[name] {
			return nil
		}

		visited[name] = true
		recStack[name] = true

		if def, ok := defs[name]; ok {
			for dep := range def.deps {
				if defs[dep] != nil { // only check computed channels
					if err := visit(dep); err != nil {
						return err
					}
				}
			}
		}

		recStack[name] = false
		return nil
	}

	for name := range defs {
		if !visited[name] {
			if err := visit(name); err != nil {
				return err
			}
		}
	}

	return nil
}

// topologicalSort returns computed channel names in dependency order
func topologicalSort(defs map[string]*computeChannelDef) []string {
	visited := make(map[string]bool)
	var result []string

	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true

		if def, ok := defs[name]; ok {
			for dep := range def.deps {
				if defs[dep] != nil { // only computed channels
					visit(dep)
				}
			}
		}
		result = append(result, name)
	}

	for name := range defs {
		visit(name)
	}

	return result
}
