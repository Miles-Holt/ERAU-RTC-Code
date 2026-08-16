package alerts

import (
	"math"
	"strconv"
	"strings"

	"controlnode/dsl"
)

// Placeholders returns the {name} placeholders in a message, in order.
// An unterminated "{" is ignored (it is checked at load time by the parser's
// string handling, and a stray brace must never break a raised alert).
func Placeholders(msg string) []string {
	var out []string
	for i := 0; i < len(msg); i++ {
		if msg[i] != '{' {
			continue
		}
		end := strings.IndexByte(msg[i:], '}')
		if end < 0 {
			break
		}
		name := msg[i+1 : i+end]
		if name != "" {
			out = append(out, name)
		}
		i += end
	}
	return out
}

// interpolate substitutes {name} placeholders at raise time.  Event fields
// ({node}, {refDes}, {value}) win over channel names; anything else is looked up
// in the channel space.  A placeholder that cannot be resolved becomes "?" —
// load-time validation means that only happens for a channel that exists in the
// config but has no value yet.
func interpolate(msg string, fields map[string]string, cs dsl.ChannelSpace) string {
	if !strings.ContainsRune(msg, '{') {
		return msg
	}
	var b strings.Builder
	for i := 0; i < len(msg); i++ {
		if msg[i] != '{' {
			b.WriteByte(msg[i])
			continue
		}
		end := strings.IndexByte(msg[i:], '}')
		if end < 0 {
			b.WriteString(msg[i:])
			break
		}
		name := msg[i+1 : i+end]
		b.WriteString(resolve(name, fields, cs))
		i += end
	}
	return b.String()
}

func resolve(name string, fields map[string]string, cs dsl.ChannelSpace) string {
	if v, ok := fields[name]; ok {
		return v
	}
	if cs != nil {
		if v, ok := cs.Get(name); ok {
			return FormatValue(v)
		}
	}
	return "?"
}

// FormatValue renders a channel value for an operator-facing message: whole
// numbers without a decimal point, everything else to two decimals.
func FormatValue(v dsl.Value) string {
	switch v.Type() {
	case "bool":
		return strconv.FormatBool(v.Bool())
	case "string":
		return v.String()
	default:
		return FormatFloat(v.Float())
	}
}

// FormatFloat renders a number the way the HMI shows it.
func FormatFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}
