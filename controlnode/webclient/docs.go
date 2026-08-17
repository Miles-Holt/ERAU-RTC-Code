package webclient

import (
	"controlnode/alerts"
	"controlnode/config"
	"controlnode/dsl"
	"controlnode/softchan"
	"controlnode/statemachine"
	"embed"
	"fmt"
	"html"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// =============================================================================
// /docs — self-documentation generated from the COMPILED configuration
// =============================================================================
//
// Every page below is rendered from the same structures the engine executes:
// the parsed YAML config, the compiled .sm program, the software-channel store
// and the compiled .alert config.  Nothing is written by hand and nothing is
// generated ahead of time, so the pages describe the configuration this process
// actually loaded — not what a file on disk says today.
//
// The only exception is /docs/protocol, which renders docs/websocket-protocol.md
// (a hand-written document about the wire format).  It is read from disk when
// available and falls back to the copy embedded at build time, mirroring how the
// WebClient is served from -webroot or the embed.

// protocolMd is the build-time copy of docs/websocket-protocol.md.  build.bat
// refreshes it before compiling, the same way it refreshes static/ from
// ../WebClient; the checked-in copy keeps `go build` working on its own.
//
//go:embed embedded/websocket-protocol.md
var protocolMd embed.FS

const protocolMdEmbedPath = "embedded/websocket-protocol.md"

// DocsInput is everything the /docs pages render.  Any field may be nil: the
// corresponding page then reports that nothing of that kind is loaded, which is
// itself accurate documentation of the running system.
type DocsInput struct {
	// System is the parsed YAML config (hardware channels, daqNodes, rates).
	System *config.SystemConfig
	// Program is the compiled .sm program.
	Program *statemachine.Program
	// Soft is the software channel store (.chan files).
	Soft *softchan.Store
	// Alerts is the compiled .alert config.
	Alerts *alerts.Config
	// AlertStaleMs is the effective per-node stale timeout the alert engine runs.
	AlertStaleMs int64
	// ProtocolPath is an optional on-disk path to docs/websocket-protocol.md.
	// When it is readable it wins over the embedded copy.
	ProtocolPath string
}

// SetDocs attaches the documentation source structures to the server.  It is
// called once during startup wiring, before ListenAndServe.
func (s *Server) SetDocs(d *DocsInput) { s.docs = d }

// handleDocs routes the /docs pages.  Documentation is read-only and carries no
// operational authority, so it is served without authentication, like the
// WebClient itself.
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	page := strings.Trim(strings.TrimPrefix(r.URL.Path, "/docs"), "/")

	var body string
	var title string
	switch page {
	case "":
		title, body = "Overview", s.docsIndex()
	case "channels":
		title, body = "Channels", s.docsChannels()
	case "machines":
		title, body = "State machines", s.docsMachines()
	case "alerts":
		title, body = "Alerts", s.docsAlerts()
	case "protocol":
		title, body = "WebSocket protocol", s.docsProtocol()
	default:
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // the config can change on restart
	fmt.Fprint(w, docsPage(title, page, body))
}

// ── Page shell ────────────────────────────────────────────────────────────────

const docsCSS = `
:root{--bg:#fff;--fg:#1b1f23;--muted:#6a737d;--line:#d8dee4;--head:#f2f4f7;
      --accent:#0b6bcb;--warn:#b26a00;--alarm:#b3261e;--code:#f4f5f7}
@media (prefers-color-scheme:dark){
  :root{--bg:#14171a;--fg:#dfe3e8;--muted:#8b949e;--line:#2c3238;--head:#1c2126;
        --accent:#5aa9ff;--warn:#e0a02c;--alarm:#ff6b60;--code:#1b1f24}
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);
     font:14px/1.55 -apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif}
header{border-bottom:1px solid var(--line);padding:14px 24px;display:flex;
       align-items:baseline;gap:18px;flex-wrap:wrap}
header h1{font-size:16px;margin:0;font-weight:600}
header .sub{color:var(--muted);font-size:12px}
nav{padding:8px 24px;border-bottom:1px solid var(--line);display:flex;gap:16px;flex-wrap:wrap}
nav a{color:var(--muted);text-decoration:none;font-size:13px;padding:2px 0;border-bottom:2px solid transparent}
nav a:hover{color:var(--fg)}
nav a.on{color:var(--accent);border-bottom-color:var(--accent)}
main{padding:20px 24px 60px;max-width:1100px}
h2{font-size:15px;margin:26px 0 8px;padding-bottom:4px;border-bottom:1px solid var(--line)}
h3{font-size:13.5px;margin:18px 0 6px}
h4{font-size:13px;margin:14px 0 4px;color:var(--muted)}
p{margin:8px 0}
code,pre,.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
code{background:var(--code);padding:1px 4px;border-radius:3px;font-size:12.5px}
pre{background:var(--code);padding:10px 12px;border-radius:5px;overflow-x:auto;font-size:12.5px;
    border:1px solid var(--line)}
pre code{background:none;padding:0}
.scroll{overflow-x:auto}
table{border-collapse:collapse;font-size:12.5px;margin:6px 0 14px;min-width:100%}
th,td{border:1px solid var(--line);padding:4px 9px;text-align:left;vertical-align:top}
th{background:var(--head);font-weight:600;white-space:nowrap}
td.num{text-align:right;font-family:ui-monospace,Consolas,monospace}
.tag{display:inline-block;font-size:11px;padding:0 6px;border-radius:9px;
     border:1px solid var(--line);color:var(--muted);white-space:nowrap}
.tag.op{color:var(--accent);border-color:var(--accent)}
.tag.daq{color:var(--warn);border-color:var(--warn)}
.tag.alarm{color:var(--alarm);border-color:var(--alarm)}
.cards{display:flex;gap:14px;flex-wrap:wrap;margin:10px 0 4px}
.card{border:1px solid var(--line);border-radius:6px;padding:10px 14px;min-width:150px}
.card .n{font-size:22px;font-weight:600}
.card .l{color:var(--muted);font-size:12px}
.empty{color:var(--muted);font-style:italic}
.hint{color:var(--muted);font-size:12px;margin:4px 0 12px}
svg text{font-family:ui-monospace,Consolas,monospace}
blockquote{margin:8px 0;padding:2px 12px;border-left:3px solid var(--line);color:var(--muted)}
`

// docsPage wraps a body fragment in the standard shell.
func docsPage(title, page, body string) string {
	nav := func(href, label, key string) string {
		cls := ""
		if key == page {
			cls = ` class="on"`
		}
		return fmt.Sprintf(`<a href="%s"%s>%s</a>`, href, cls, label)
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString("<title>RTC docs — " + html.EscapeString(title) + "</title>")
	b.WriteString("<style>" + docsCSS + "</style></head><body>")
	b.WriteString(`<header><h1>RTC control node — configuration reference</h1>`)
	b.WriteString(`<span class="sub">generated live from the loaded configuration</span></header>`)
	b.WriteString("<nav>")
	b.WriteString(nav("/docs", "Overview", ""))
	b.WriteString(nav("/docs/channels", "Channels", "channels"))
	b.WriteString(nav("/docs/machines", "State machines", "machines"))
	b.WriteString(nav("/docs/alerts", "Alerts", "alerts"))
	b.WriteString(nav("/docs/protocol", "WebSocket protocol", "protocol"))
	b.WriteString(`<a href="/">← control panel</a>`)
	b.WriteString("</nav><main>")
	b.WriteString(body)
	b.WriteString("</main></body></html>")
	return b.String()
}

// ── /docs — overview ──────────────────────────────────────────────────────────

func (s *Server) docsIndex() string {
	d := s.docs
	var b strings.Builder
	b.WriteString("<h2>System summary</h2>")
	if d == nil {
		b.WriteString(`<p class="empty">No configuration was attached to this server (test harness).</p>`)
		return b.String()
	}

	nodes, hwChannels, cmdChannels := 0, 0, 0
	var nodeNames []string
	if d.System != nil {
		for i := range d.System.DaqNodes.Nodes {
			if d.System.DaqNodes.Nodes[i].Enabled {
				nodes++
				nodeNames = append(nodeNames, d.System.DaqNodes.Nodes[i].RefDes)
			}
		}
		for _, c := range d.System.ControlList.Controls {
			if !c.Enabled {
				continue
			}
			for _, ch := range c.Channels {
				hwChannels++
				if ch.Role != "" {
					cmdChannels++
				}
			}
		}
	}
	settable, computed := 0, 0
	if d.Soft != nil {
		for _, c := range d.Soft.Docs() {
			if c.Computed {
				computed++
			} else {
				settable++
			}
		}
	}
	machines, states, daqStates := 0, 0, 0
	if d.Program != nil {
		machines = len(d.Program.Machines)
		for _, m := range d.Program.Machines {
			states += len(m.States)
			for _, st := range m.States {
				if st.DaqLocal != "" {
					daqStates++
				}
			}
		}
	}
	rules, tmpl := 0, "none"
	if d.Alerts != nil {
		rules = len(d.Alerts.Rules)
		if d.Alerts.Template != nil {
			tmpl = d.Alerts.Template.Name
		}
	}

	card := func(n int, label string) string {
		return fmt.Sprintf(`<div class="card"><div class="n">%d</div><div class="l">%s</div></div>`, n, label)
	}
	b.WriteString(`<div class="cards">`)
	b.WriteString(card(nodes, "enabled daqNodes"))
	b.WriteString(card(hwChannels, "hardware channels"))
	b.WriteString(card(settable+computed, "software channels"))
	b.WriteString(card(machines, "state machines"))
	b.WriteString(card(rules, "alert rules"))
	b.WriteString("</div>")

	b.WriteString("<h2>Rates and timing</h2><div class=\"scroll\"><table>")
	b.WriteString("<tr><th>Setting</th><th>Value</th><th>Source</th><th>What it drives</th></tr>")
	if d.System != nil {
		n := d.System.Network
		row := func(k, v, src, what string) {
			b.WriteString("<tr><td>" + esc(k) + "</td><td class=\"num\">" + esc(v) + "</td><td><code>" +
				esc(src) + "</code></td><td>" + esc(what) + "</td></tr>")
		}
		row("engineTickRateHz", itoa(n.EngineTickRateHz)+" Hz", "system.yaml",
			"state machine tick: computed channels → controllers → alert rules; also the resolution of sleep / wait_until")
		row("broadcastRateHz", itoa(n.BroadcastRateHz)+" Hz", "system.yaml", "data fan-out to browsers and DAQ sample rate")
		row("connectionManagementRateHz", itoa(n.ManagementRateHz)+" Hz", "system.yaml", "keepalive rate sent to daqNodes in their config")
		row("channelStaleMs", itoa(n.ChannelStaleMs)+" ms", "system.yaml", "browser-side per-channel stale shading")
		row("webSocketPort", itoa(n.WebSocketPort), "system.yaml", "this HTTP + WebSocket server")
	}
	if d.AlertStaleMs > 0 {
		b.WriteString("<tr><td>stale timeout</td><td class=\"num\">" + itoa(int(d.AlertStaleMs)) +
			" ms</td><td><code>alerts/*.alert</code></td><td>server-side per-daqNode data-staleness alert</td></tr>")
	}
	b.WriteString("</table></div>")

	b.WriteString("<h2>DAQ nodes</h2>")
	if d.System == nil || len(d.System.DaqNodes.Nodes) == 0 {
		b.WriteString(`<p class="empty">No daqNodes configured.</p>`)
	} else {
		b.WriteString(`<div class="scroll"><table><tr><th>refDes</th><th>Address</th><th>Enabled</th>` +
			`<th>Description</th><th>Channels</th><th>daq_local states</th></tr>`)
		for i := range d.System.DaqNodes.Nodes {
			n := d.System.DaqNodes.Nodes[i]
			chCount := 0
			for _, c := range d.System.ControlList.Controls {
				if !c.Enabled {
					continue
				}
				for _, ch := range c.Channels {
					if ch.RefDesDaq == n.RefDes {
						chCount++
					}
				}
			}
			local := docsDaqLocalStates(d.Program, n.RefDes)
			enabled := "no"
			if n.Enabled {
				enabled = "yes"
			}
			b.WriteString("<tr><td><code>" + esc(n.RefDes) + "</code></td><td class=\"mono\">" +
				esc(fmt.Sprintf("%s:%d", n.IP, n.WSPort)) + "</td><td>" + enabled + "</td><td>" +
				esc(n.Description) + "</td><td class=\"num\">" + itoa(chCount) + "</td><td>" +
				docsOrDash(strings.Join(local, ", ")) + "</td></tr>")
		}
		b.WriteString("</table></div>")
	}

	b.WriteString("<h2>Loaded configuration</h2><div class=\"scroll\"><table>")
	b.WriteString("<tr><th>What</th><th>Where</th><th>Loaded</th></tr>")
	b.WriteString(docsRow("Hardware channels + daqNodes", "controls.yaml, daqNodes/*.yaml",
		itoa(hwChannels)+" channels ("+itoa(cmdChannels)+" commandable) on "+itoa(nodes)+" enabled node(s)"))
	b.WriteString(docsRow("Software channels", "channels/*.chan",
		itoa(settable)+" settable, "+itoa(computed)+" computed"))
	b.WriteString(docsRow("State machines", "machines/*.sm",
		itoa(machines)+" machine(s), "+itoa(states)+" state(s), "+itoa(daqStates)+" daq_local"))
	b.WriteString(docsRow("Alerts", "alerts/*.alert",
		itoa(rules)+" rule(s), template: "+tmpl))
	b.WriteString("</table></div>")

	b.WriteString(`<p class="hint">These pages are rendered from the compiled configuration on every
request, so they always describe the running process. Restart the control node to pick up file edits.
For the language itself see <code>docs/dsl-guide.md</code> (tutorial) and
<code>docs/restructure/dsl_spec.md</code> (formal semantics).</p>`)
	return b.String()
}

func docsRow(cells ...string) string {
	var b strings.Builder
	b.WriteString("<tr>")
	for _, c := range cells {
		b.WriteString("<td>" + esc(c) + "</td>")
	}
	b.WriteString("</tr>")
	return b.String()
}

// docsDaqLocalStates lists "machine.state" for every daq_local state on node.
func docsDaqLocalStates(prog *statemachine.Program, node string) []string {
	if prog == nil {
		return nil
	}
	var out []string
	for _, m := range prog.Machines {
		for _, st := range m.States {
			if st.DaqLocal == node {
				out = append(out, m.Name+"."+st.Name)
			}
		}
	}
	return out
}

// ── /docs/channels ────────────────────────────────────────────────────────────

func (s *Server) docsChannels() string {
	d := s.docs
	var b strings.Builder

	b.WriteString("<h2>Hardware channels</h2>")
	b.WriteString(`<p class="hint">From <code>controls.yaml</code>, grouped by the daqNode that owns them.
Disabled controls are omitted — the control node never sends them to a node or a browser.</p>`)
	if d == nil || d.System == nil {
		b.WriteString(`<p class="empty">No YAML configuration loaded.</p>`)
	} else {
		byNode := map[string][]docsHwChannel{}
		for _, c := range d.System.ControlList.Controls {
			if !c.Enabled {
				continue
			}
			for _, ch := range c.Channels {
				node := ch.RefDesDaq
				if node == "" {
					node = "(unassigned)"
				}
				byNode[node] = append(byNode[node], docsHwChannel{
					RefDes: ch.RefDes, Role: ch.Role, Units: ch.DaqMx.Units,
					Min: ch.ValidMin, Max: ch.ValidMax,
					Control: c.RefDes, ControlDesc: c.Description, Type: c.Type,
					Module: ch.ModuleModelNumber, Chan: ch.ChannelNumber,
				})
			}
		}
		nodes := make([]string, 0, len(byNode))
		for k := range byNode {
			nodes = append(nodes, k)
		}
		sort.Strings(nodes)
		if len(nodes) == 0 {
			b.WriteString(`<p class="empty">No enabled controls.</p>`)
		}
		for _, node := range nodes {
			b.WriteString("<h3>" + esc(node) + " <span class=\"tag\">" + itoa(len(byNode[node])) + " channels</span></h3>")
			b.WriteString(`<div class="scroll"><table><tr><th>refDes</th><th>Role</th><th>Units</th>` +
				`<th>validMin</th><th>validMax</th><th>Control</th><th>Type</th><th>Module / channel</th></tr>`)
			for _, ch := range byNode[node] {
				b.WriteString("<tr><td><code>" + esc(ch.RefDes) + "</code></td><td>" +
					docsRoleTag(ch.Role) + "</td><td>" + esc(ch.Units) + "</td><td class=\"num\">" +
					docsOrDash(ch.Min) + "</td><td class=\"num\">" + docsOrDash(ch.Max) + "</td><td>" +
					esc(ch.Control) + docsParen(ch.ControlDesc) + "</td><td>" + esc(ch.Type) + "</td><td class=\"mono\">" +
					esc(strings.TrimSpace(ch.Module+" "+ch.Chan)) + "</td></tr>")
			}
			b.WriteString("</table></div>")
		}

		if len(d.System.CtrNode.Health.Sensors) > 0 || len(d.System.CtrNode.Health.Commands) > 0 {
			b.WriteString("<h3>" + esc(d.System.CtrNode.RefDes) + " (control node health)</h3>")
			b.WriteString(`<div class="scroll"><table><tr><th>refDes</th><th>Role</th><th>Units</th><th>Description</th></tr>`)
			for _, sn := range d.System.CtrNode.Health.Sensors {
				b.WriteString("<tr><td><code>" + esc(sn.RefDes) + "</code></td><td>" + docsRoleTag("") +
					"</td><td>" + esc(sn.Units) + "</td><td>" + esc(sn.Description) + "</td></tr>")
			}
			for _, cm := range d.System.CtrNode.Health.Commands {
				b.WriteString("<tr><td><code>" + esc(cm.RefDes) + "</code></td><td>" + docsRoleTag(cm.Role) +
					"</td><td></td><td>" + esc(cm.Description) + "</td></tr>")
			}
			b.WriteString("</table></div>")
		}
	}

	b.WriteString("<h2>Software channels</h2>")
	b.WriteString(`<p class="hint">From <code>channels/*.chan</code>. Settable channels are operator-writable
within their bounds and persist across restarts; computed channels are recomputed every engine tick in
dependency order and are read-only. <code>SM-&lt;NAME&gt;-STATE</code> / <code>SM-&lt;NAME&gt;-TARGET</code>
are generated automatically, one pair per state machine.</p>`)
	if d == nil || d.Soft == nil {
		b.WriteString(`<p class="empty">No software channel store loaded.</p>`)
		return b.String()
	}
	docsList := d.Soft.Docs()
	var settable, computed []softchan.ChannelDoc
	for _, c := range docsList {
		if c.Computed {
			computed = append(computed, c)
		} else {
			settable = append(settable, c)
		}
	}

	b.WriteString("<h3>Settable</h3>")
	if len(settable) == 0 {
		b.WriteString(`<p class="empty">None.</p>`)
	} else {
		b.WriteString(`<div class="scroll"><table><tr><th>refDes</th><th>Role</th><th>Default</th><th>Min</th>` +
			`<th>Max</th><th>Units</th><th>Description</th></tr>`)
		for _, c := range settable {
			b.WriteString("<tr><td><code>" + esc(c.RefDes) + "</code></td><td>" + docsRoleTag(c.Role) +
				"</td><td class=\"num\">" + ftoa(c.Default) + "</td><td class=\"num\">" + docsOptFloat(c.Min) +
				"</td><td class=\"num\">" + docsOptFloat(c.Max) + "</td><td>" + esc(c.Units) + "</td><td>" +
				esc(c.Description) + "</td></tr>")
		}
		b.WriteString("</table></div>")
	}

	b.WriteString("<h3>Computed</h3>")
	if len(computed) == 0 {
		b.WriteString(`<p class="empty">None.</p>`)
	} else {
		b.WriteString(`<div class="scroll"><table><tr><th>refDes</th><th>Units</th><th>Expression (recompute order)</th>` +
			`<th>Description</th></tr>`)
		for _, c := range computed {
			b.WriteString("<tr><td><code>" + esc(c.RefDes) + "</code></td><td>" + esc(c.Units) +
				"</td><td><code>" + esc(c.Compute) + "</code></td><td>" + esc(c.Description) + "</td></tr>")
		}
		b.WriteString("</table></div>")
	}
	return b.String()
}

type docsHwChannel struct {
	RefDes, Role, Units, Min, Max string
	Control, ControlDesc, Type    string
	Module, Chan                  string
}

// ── /docs/machines ────────────────────────────────────────────────────────────

func (s *Server) docsMachines() string {
	d := s.docs
	var b strings.Builder
	if d == nil || d.Program == nil || len(d.Program.Machines) == 0 {
		b.WriteString("<h2>State machines</h2>")
		b.WriteString(`<p class="empty">No state machines are loaded (no <code>.sm</code> files in config/machines/).</p>`)
		return b.String()
	}

	for _, m := range d.Program.Machines {
		b.WriteString("<h2>machine " + esc(m.Name) + "</h2>")
		b.WriteString(`<p class="hint">Compiled from <code>` + esc(m.Source) +
			`</code>. Initial state: <code>` + esc(m.Initial.Name) +
			`</code> (first in the file). Operator target channel: <code>SM-` + esc(m.Name) +
			`-TARGET</code>; current state index published on <code>SM-` + esc(m.Name) + `-STATE</code>.</p>`)

		trans := docsTransitions(m)
		b.WriteString(docsStateDiagram(m, trans))

		b.WriteString("<h3>States</h3><div class=\"scroll\"><table>")
		b.WriteString("<tr><th>#</th><th>State</th><th>Operator</th><th>daq_local</th><th>Blocks</th><th>Leaves to</th></tr>")
		for _, st := range m.States {
			blocks := []string{}
			if len(st.Controller) > 0 {
				blocks = append(blocks, "controller")
			}
			if len(st.Sequence) > 0 {
				blocks = append(blocks, "sequence")
			}
			if len(st.AbortSequence) > 0 {
				blocks = append(blocks, "abort_sequence")
			}
			if len(st.AbortRules) > 0 {
				blocks = append(blocks, itoa(len(st.AbortRules))+"× abort_rule")
			}
			if len(blocks) == 0 {
				blocks = append(blocks, "— (resting state)")
			}
			var outs []string
			seen := map[string]bool{}
			for _, t := range trans {
				if t.From == st.Name && !seen[t.To] {
					seen[t.To] = true
					outs = append(outs, t.To)
				}
			}
			opTag := "—"
			if st.Operator {
				opTag = `<span class="tag op">operator</span> <span class="tag">` +
					esc(docsGateText(st)) + `</span>`
			}
			daqTag := "—"
			if st.DaqLocal != "" {
				daqTag = `<span class="tag daq">` + esc(st.DaqLocal) + `</span>`
			}
			b.WriteString("<tr><td class=\"num\">" + itoa(st.Index) + "</td><td><code>" + esc(st.Name) +
				"</code></td><td>" + opTag + "</td><td>" + daqTag + "</td><td>" + esc(strings.Join(blocks, ", ")) +
				"</td><td class=\"mono\">" + docsOrDash(strings.Join(outs, ", ")) + "</td></tr>")
		}
		b.WriteString("</table></div>")

		b.WriteString("<h3>Transitions</h3><div class=\"scroll\"><table>")
		b.WriteString("<tr><th>From</th><th>To</th><th>Trigger</th><th>Condition / detail</th></tr>")
		if len(trans) == 0 {
			b.WriteString(`<tr><td colspan="4" class="empty">No transitions — every state is entered by operator request only.</td></tr>`)
		}
		for _, t := range trans {
			b.WriteString("<tr><td class=\"mono\">" + esc(t.From) + "</td><td class=\"mono\">" + esc(t.To) +
				"</td><td>" + esc(t.Kind) + "</td><td><code>" + esc(t.Detail) + "</code></td></tr>")
		}
		b.WriteString("</table></div>")
		b.WriteString(`<p class="hint">Operator-flagged states are additionally reachable by writing their name
to <code>SM-` + esc(m.Name) + `-TARGET</code>; those requests are not listed above. A state whose flag carries
a gate (<code>operator from a, b</code>) accepts that request only while the machine is in one of the listed
states — the gate restricts operator input only, never a <code>transition</code> in the machine itself or a
DAQ-reported abort.</p>`)

		for _, st := range m.States {
			b.WriteString(docsStateDetail(m, st))
		}
	}
	return b.String()
}

// docsTransition is one edge of a machine's state graph, recovered from the
// compiled AST.
type docsTransition struct {
	From, To, Kind, Detail string
}

// docsTransitions walks every compiled block of a machine and extracts every
// way one state can hand control to another: controller and sequence
// `transition` statements (with the enclosing `if` condition when there is one),
// `wait_until … timeout -> state`, and the two DAQ-reported edges of a
// daq_local state (sequence_complete → completion target, abort_triggered →
// abort destination).
func docsTransitions(m *statemachine.Machine) []docsTransition {
	var out []docsTransition
	for _, st := range m.States {
		add := func(to, kind, detail string) {
			out = append(out, docsTransition{From: st.Name, To: to, Kind: kind, Detail: detail})
		}
		var walk func(stmts []dsl.Stmt, kind, cond string)
		walk = func(stmts []dsl.Stmt, kind, cond string) {
			for _, s := range stmts {
				switch v := s.(type) {
				case *dsl.TransitionStmt:
					detail := cond
					if detail == "" {
						detail = "transition " + v.Target
					}
					add(v.Target, kind, detail)
				case *dsl.CommandStmt:
					// Cross-machine: "To" names the other machine's state as
					// "<machine>.<state>" so it reads distinctly from this
					// machine's own state names above. The circular per-machine
					// diagram below only draws edges between this machine's own
					// states, so a command edge harmlessly has no diagram node
					// to land on and is skipped there — it still shows up here
					// in the transitions table and in the state's source.
					add(v.Machine+"."+v.Target, "command",
						"command "+v.Machine+" -> "+v.Target)
				case *dsl.WaitUntilStmt:
					if v.TimeoutState != "" {
						add(v.TimeoutState, kind+" timeout",
							"wait_until "+dsl.ExprString(v.Condition)+" timeout "+dsl.ExprString(v.Timeout))
					}
				case *dsl.IfStmt:
					c := "if " + dsl.ExprString(v.Condition)
					if cond != "" {
						c = cond + " ∧ " + c
					}
					walk(v.Body, kind, c)
					for _, e := range v.Elif {
						ec := cond
						if e.Condition == nil {
							if ec == "" {
								ec = "else"
							} else {
								ec += " ∧ else"
							}
						} else {
							ec = "elif " + dsl.ExprString(e.Condition)
							if cond != "" {
								ec = cond + " ∧ " + ec
							}
						}
						walk(e.Body, kind, ec)
					}
				}
			}
		}
		walk(st.Controller, "controller", "")
		if st.DaqLocal == "" {
			walk(st.Sequence, "sequence", "")
		}
		if st.CompletionTarget != "" {
			add(st.CompletionTarget, "sequence_complete",
				"daq_local "+st.DaqLocal+" reported the cached entry_sequence finished")
		}
		if st.AbortTarget != "" {
			add(st.AbortTarget, "abort_triggered",
				"daq_local "+st.DaqLocal+" tripped an abort_rule and ran exit_sequence locally")
		}
	}
	return out
}

// docsStateDetail renders one state's compiled blocks as DSL-shaped source.
func docsStateDetail(m *statemachine.Machine, st *statemachine.State) string {
	var b strings.Builder
	b.WriteString("<h3>state " + esc(st.Name) + "</h3>")

	var flags []string
	if st.Operator {
		flags = append(flags, `<span class="tag op">`+esc(dsl.OperatorString(st.OperatorFrom()))+`</span>`)
	}
	if st.DaqLocal != "" {
		flags = append(flags, `<span class="tag daq">daq_local `+esc(st.DaqLocal)+`</span>`)
	}
	if st.Index == 0 {
		flags = append(flags, `<span class="tag">initial</span>`)
	}
	if len(flags) > 0 {
		b.WriteString("<p>" + strings.Join(flags, " ") + "</p>")
	}
	if st.Operator {
		b.WriteString(`<p class="hint">Operator command: ` + esc(docsGateText(st)) +
			`. Requests from any other state are refused by the engine (the HMI also hides them).</p>`)
	} else {
		b.WriteString(`<p class="hint">Not operator-commandable: this state is entered only by the machine's
own logic or by a DAQ report.</p>`)
	}

	if len(st.Controller) > 0 {
		b.WriteString("<h4>controller — runs every engine tick while active</h4>")
		b.WriteString(docsCode(dsl.StmtLines(st.Controller, 0)))
	}
	if len(st.Sequence) > 0 {
		if st.DaqLocal != "" {
			b.WriteString("<h4>sequence — compiled to <code>entry_sequence</code> and cached on " +
				esc(st.DaqLocal) + "</h4>")
		} else {
			b.WriteString("<h4>sequence — runs once on entry, in its own goroutine</h4>")
		}
		b.WriteString(docsCode(dsl.StmtLines(st.Sequence, 0)))
	}
	if st.DaqLocal != "" && st.CompletionTarget != "" {
		b.WriteString(`<p class="hint">Completion transition: on <code>sequence_complete</code> from ` +
			esc(st.DaqLocal) + ` the machine enters <code>` + esc(st.CompletionTarget) + `</code>.</p>`)
	}
	if len(st.AbortSequence) > 0 {
		b.WriteString("<h4>abort_sequence — compiled to <code>exit_sequence</code>, run locally by " +
			esc(st.DaqLocal) + " when a rule trips</h4>")
		b.WriteString(docsCode(dsl.StmtLines(st.AbortSequence, 0)))
	}
	if st.AbortTarget != "" {
		b.WriteString(`<p class="hint">Abort destination: on <code>abort_triggered</code> the machine enters <code>` +
			esc(st.AbortTarget) + `</code>.</p>`)
	}
	if len(st.AbortRules) > 0 {
		b.WriteString("<h4>abort_rule — evaluated on " + esc(st.DaqLocal) +
			", in its own time base starting at entry_sequence t=0</h4>")
		b.WriteString(`<div class="scroll"><table><tr><th>Channel</th><th>Op</th><th>Threshold</th>` +
			`<th>Window from</th><th>to</th></tr>`)
		for _, r := range st.AbortRules {
			b.WriteString("<tr><td><code>" + esc(r.Channel) + "</code></td><td class=\"mono\">" + esc(r.Op) +
				"</td><td class=\"mono\">" + esc(dsl.ExprString(r.Value)) + "</td><td class=\"mono\">" +
				esc(dsl.ExprString(r.FromMs)) + "</td><td class=\"mono\">" + esc(dsl.ExprString(r.ToMs)) +
				"</td></tr>")
		}
		b.WriteString("</table></div>")
		b.WriteString(`<p class="hint">Thresholds and window bounds written as channel names are resolved to
numbers when the payload is sent (state entry, or a node <code>state_req</code>), so operators can retune
them without recompiling. An unresolvable reference refuses the payload instead of sending a 0.</p>`)
	}
	if len(st.Controller) == 0 && len(st.Sequence) == 0 {
		b.WriteString(`<p class="hint">Resting state: no controller and no sequence. It holds until an
operator or another machine commands a transition out.</p>`)
	}
	return b.String()
}

func docsCode(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return "<pre><code>" + esc(strings.Join(lines, "\n")) + "</code></pre>"
}

// ── State diagram (inline SVG) ────────────────────────────────────────────────

// docsStateDiagram lays the states out on a circle and draws one arrow per
// distinct (from → to) pair.  A circular layout needs no graph library and
// stays readable for the handful of states a test machine has; the transition
// table underneath is the authoritative listing.
func docsStateDiagram(m *statemachine.Machine, trans []docsTransition) string {
	n := len(m.States)
	if n == 0 {
		return ""
	}
	const (
		rx = 62.0 // node half-width
		ry = 17.0 // node half-height
	)
	radius := 90.0 + 20.0*float64(n)
	if radius > 200 {
		radius = 200
	}
	w := 2*(radius+rx) + 40
	h := 2*(radius+ry) + 80
	cx, cy := w/2, h/2

	type pt struct{ x, y float64 }
	pos := make(map[string]pt, n)
	for i, st := range m.States {
		// Start at the top and go clockwise so the initial state leads.
		ang := -math.Pi/2 + 2*math.Pi*float64(i)/float64(n)
		pos[st.Name] = pt{cx + radius*math.Cos(ang), cy + radius*math.Sin(ang)}
	}

	// One arrow per distinct pair, labelled with its trigger kinds.
	type edge struct {
		from, to string
		kinds    []string
	}
	var edges []edge
	idx := map[string]int{}
	for _, t := range trans {
		key := t.From + "\x00" + t.To
		i, ok := idx[key]
		if !ok {
			edges = append(edges, edge{from: t.From, to: t.To})
			i = len(edges) - 1
			idx[key] = i
		}
		kind := t.Kind
		dup := false
		for _, k := range edges[i].kinds {
			if k == kind {
				dup = true
			}
		}
		if !dup {
			edges[i].kinds = append(edges[i].kinds, kind)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<div class="scroll"><svg viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role="img" `+
		`aria-label="state transition diagram for machine %s" style="max-width:100%%;height:auto">`,
		w, h, w, h, esc(m.Name))
	b.WriteString(`<defs>` +
		`<marker id="ah" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" ` +
		`orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="currentColor"/></marker>` +
		`<marker id="ohd" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" ` +
		`orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="#b36b00"/></marker>` +
		`</defs>`)
	b.WriteString(`<g fill="none" stroke="currentColor" stroke-opacity="0.55">`)

	for _, e := range edges {
		p1, ok1 := pos[e.from]
		p2, ok2 := pos[e.to]
		if !ok1 || !ok2 {
			continue
		}
		label := strings.Join(e.kinds, " / ")
		if e.from == e.to {
			// Self-transition (re-entry): a small loop above the node.
			fmt.Fprintf(&b, `<path d="M %.1f %.1f C %.1f %.1f, %.1f %.1f, %.1f %.1f" marker-end="url(#ah)"/>`,
				p1.x-14, p1.y-ry, p1.x-46, p1.y-ry-46, p1.x+46, p1.y-ry-46, p1.x+14, p1.y-ry)
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="9" text-anchor="middle" fill="currentColor" `+
				`stroke="none" fill-opacity="0.75">%s</text>`, p1.x, p1.y-ry-38, esc(label))
			continue
		}
		dx, dy := p2.x-p1.x, p2.y-p1.y
		// Bow every edge slightly so a reciprocal pair does not overlap.
		mx, my := (p1.x+p2.x)/2, (p1.y+p2.y)/2
		length := math.Hypot(dx, dy)
		if length == 0 {
			continue
		}
		nxo, nyo := -dy/length, dx/length // unit normal
		bow := 26.0
		qx, qy := mx+nxo*bow, my+nyo*bow
		// Trim both ends to the node ellipse so arrows touch the box, not its centre.
		sx, sy := trimToBox(p1.x, p1.y, qx, qy, rx+4, ry+4)
		ex, ey := trimToBox(p2.x, p2.y, qx, qy, rx+6, ry+6)
		fmt.Fprintf(&b, `<path d="M %.1f %.1f Q %.1f %.1f %.1f %.1f" marker-end="url(#ah)"/>`, sx, sy, qx, qy, ex, ey)
		lx, ly := (sx+2*qx+ex)/4, (sy+2*qy+ey)/4
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="9" text-anchor="middle" fill="currentColor" `+
			`stroke="none" fill-opacity="0.75">%s</text>`, lx, ly-3, esc(label))
	}
	b.WriteString("</g>")

	// Operator-command edges: from each state an `operator from ...` gate
	// permits, to the gated target. Drawn dashed and in a distinct color so
	// they read as operator input, not machine-driven transitions (the solid
	// arrows above, which sequence/controller/transition logic always uses
	// and which gating never restricts).
	type opEdge struct{ from, to string }
	var opEdges []opEdge
	for _, st := range m.States {
		if !st.Operator {
			continue
		}
		for _, from := range st.OperatorFrom() {
			if from == st.Name {
				continue
			}
			opEdges = append(opEdges, opEdge{from: from, to: st.Name})
		}
	}
	if len(opEdges) > 0 {
		b.WriteString(`<g fill="none" stroke="#b36b00" stroke-opacity="0.75" stroke-dasharray="4 3">`)
		for _, oe := range opEdges {
			p1, ok1 := pos[oe.from]
			p2, ok2 := pos[oe.to]
			if !ok1 || !ok2 {
				continue
			}
			dx, dy := p2.x-p1.x, p2.y-p1.y
			mx, my := (p1.x+p2.x)/2, (p1.y+p2.y)/2
			length := math.Hypot(dx, dy)
			if length == 0 {
				continue
			}
			nxo, nyo := -dy/length, dx/length
			bow := -18.0 // bow opposite the transition arrows so the two don't overlap
			qx, qy := mx+nxo*bow, my+nyo*bow
			sx, sy := trimToBox(p1.x, p1.y, qx, qy, rx+4, ry+4)
			ex, ey := trimToBox(p2.x, p2.y, qx, qy, rx+6, ry+6)
			fmt.Fprintf(&b, `<path d="M %.1f %.1f Q %.1f %.1f %.1f %.1f" marker-end="url(#ohd)"/>`,
				sx, sy, qx, qy, ex, ey)
		}
		b.WriteString("</g>")
	}

	for _, st := range m.States {
		p := pos[st.Name]
		dash := ""
		if st.DaqLocal != "" {
			dash = ` stroke-dasharray="5 3"`
		}
		weight := "1"
		if st.Operator {
			weight = "2"
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" rx="6" width="%.1f" height="%.1f" fill="none" `+
			`stroke="currentColor"%s stroke-width="%s" stroke-opacity="0.85"/>`,
			p.x-rx, p.y-ry, 2*rx, 2*ry, dash, weight)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="11" text-anchor="middle" fill="currentColor">%s</text>`,
			p.x, p.y+4, esc(st.Name))
		if st.Index == 0 {
			// Entry marker for the initial state.
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4" fill="currentColor"/>`, p.x-rx-14, p.y)
			fmt.Fprintf(&b, `<path d="M %.1f %.1f L %.1f %.1f" stroke="currentColor" fill="none" `+
				`marker-end="url(#ah)"/>`, p.x-rx-9, p.y, p.x-rx-2, p.y)
		}
	}
	b.WriteString("</svg></div>")
	b.WriteString(`<p class="hint">Bold box = <code>operator</code>-commandable · dashed box =
<code>daq_local</code> (cached and executed on the node) · filled dot = initial state.
Arrow labels name the trigger, not the condition; the table below carries the conditions.</p>`)
	b.WriteString(`<p class="hint">` +
		`<svg width="28" height="10" aria-hidden="true"><line x1="1" y1="5" x2="27" y2="5" ` +
		`stroke="currentColor" stroke-opacity="0.55"/></svg> sequence / controller transition (machine logic) &nbsp;&nbsp; ` +
		`<svg width="28" height="10" aria-hidden="true"><line x1="1" y1="5" x2="27" y2="5" ` +
		`stroke="#b36b00" stroke-opacity="0.75" stroke-dasharray="4 3"/></svg> operator command ` +
		`(restricted by <code>operator from</code>; never blocks aborts or sequence completions)</p>`)
	return b.String()
}

// trimToBox walks from the centre of a node towards a control point and returns
// the point where the node's bounding box is left.
func trimToBox(cx, cy, tx, ty, hw, hh float64) (float64, float64) {
	dx, dy := tx-cx, ty-cy
	if dx == 0 && dy == 0 {
		return cx, cy
	}
	scale := math.Inf(1)
	if dx != 0 {
		scale = math.Min(scale, hw/math.Abs(dx))
	}
	if dy != 0 {
		scale = math.Min(scale, hh/math.Abs(dy))
	}
	if math.IsInf(scale, 1) {
		return cx, cy
	}
	return cx + dx*scale, cy + dy*scale
}

// ── /docs/alerts ──────────────────────────────────────────────────────────────

func (s *Server) docsAlerts() string {
	d := s.docs
	var b strings.Builder
	b.WriteString("<h2>Per-daqNode template</h2>")
	b.WriteString(`<p class="hint">The <code>every_daqnode</code> template is instantiated once per enabled
daqNode. These alerts are raised by the control node from link and data events — the browser only renders
what the server publishes.</p>`)

	if d == nil || d.Alerts == nil {
		b.WriteString(`<p class="empty">No alert configuration loaded.</p>`)
		return b.String()
	}
	if d.Alerts.Template == nil {
		b.WriteString(`<p class="empty">No template declared: node connect / disconnect / stale / bad-data
events raise nothing.</p>`)
	} else {
		b.WriteString(`<div class="scroll"><table><tr><th>Event</th><th>Severity</th><th>Message</th><th>When</th></tr>`)
		order := []struct{ ev, when string }{
			{alerts.EventDisconnect, "the node's WebSocket link drops"},
			{alerts.EventReconnect, "the link comes back (never on the first connect — nothing was lost)"},
			{alerts.EventBadData, "a channel value leaves its validMin/validMax band"},
			{alerts.EventStale, "a connected node stops delivering data for the stale timeout"},
		}
		for _, o := range order {
			ev, ok := d.Alerts.Template.Events[o.ev]
			if !ok {
				b.WriteString("<tr><td><code>" + esc(o.ev) + "</code></td><td colspan=\"2\" class=\"empty\">not declared</td><td>" +
					esc(o.when) + "</td></tr>")
				continue
			}
			when := o.when
			if o.ev == alerts.EventStale {
				when += " (" + itoa(int(d.Alerts.Template.StaleMs())) + " ms)"
			}
			b.WriteString("<tr><td><code>" + esc(ev.Event) + "</code></td><td>" + docsSeverityTag(ev.Severity) +
				"</td><td>" + esc(ev.Message) + "</td><td>" + esc(when) + "</td></tr>")
		}
		b.WriteString("</table></div>")
		if len(d.Alerts.Template.Events) > 0 {
			b.WriteString(`<p class="hint">Placeholders: <code>{node}</code> the daqNode refDes,
<code>{refDes}</code> the channel (the node name for node-level events), <code>{value}</code> the value that
tripped a bad-data check, and <code>{ANY-CHANNEL}</code> for any channel's live value.</p>`)
		}
	}

	b.WriteString("<h2>Rules</h2>")
	b.WriteString(`<p class="hint">Rules are evaluated once per engine tick, after the controllers, against the
same channel snapshot. They are edge-triggered: an alert is raised when the condition goes false → true.
A non-latching rule clears itself on true → false; a latching one stays raised until an operator
acknowledges it.</p>`)
	if len(d.Alerts.Rules) == 0 {
		b.WriteString(`<p class="empty">No rules defined.</p>`)
		return b.String()
	}
	b.WriteString(`<div class="scroll"><table><tr><th>Name</th><th>Severity</th><th>Latch</th><th>Condition</th>` +
		`<th>Message</th><th>Defined in</th></tr>`)
	for _, r := range d.Alerts.Rules {
		latch := "auto-clears"
		if r.Latch {
			latch = `<span class="tag alarm">latched</span>`
		}
		b.WriteString("<tr><td><code>" + esc(r.Name) + "</code></td><td>" + docsSeverityTag(r.Severity) +
			"</td><td>" + latch + "</td><td><code>" + esc(dsl.ExprString(r.Cond)) + "</code></td><td>" +
			esc(r.Message) + "</td><td class=\"mono\">" + esc(fmt.Sprintf("%s:%d", r.File, r.Line)) + "</td></tr>")
	}
	b.WriteString("</table></div>")
	b.WriteString(`<p class="hint">Every alert reaches the browser as an <code>alert</code> message and is
re-sent in the 1 Hz <code>alert_snapshot</code>; acknowledging one sends <code>ack_alert</code> back on
<code>/ws/ctrl</code>. See the <a href="/docs/protocol">protocol page</a>.</p>`)
	return b.String()
}

// ── /docs/protocol ────────────────────────────────────────────────────────────

func (s *Server) docsProtocol() string {
	md, source := s.protocolMarkdown()
	if md == "" {
		return `<h2>WebSocket protocol</h2><p class="empty">websocket-protocol.md is not available in this build.</p>`
	}
	return `<p class="hint">Rendered from <code>` + esc(source) + `</code>.</p>` + renderMarkdown(md)
}

// protocolMarkdown prefers the on-disk document (so an edit shows up on the next
// request during development) and falls back to the copy embedded at build time,
// exactly like -webroot vs. the embedded WebClient.
func (s *Server) protocolMarkdown() (text, source string) {
	if s.docs != nil && s.docs.ProtocolPath != "" {
		if data, err := os.ReadFile(s.docs.ProtocolPath); err == nil {
			return string(data), s.docs.ProtocolPath
		}
	}
	data, err := protocolMd.ReadFile(protocolMdEmbedPath)
	if err != nil {
		return "", ""
	}
	return string(data), "docs/websocket-protocol.md (embedded at build time)"
}

// renderMarkdown converts the subset of Markdown these docs use — headings,
// fenced code, pipe tables, lists, block quotes, rules and paragraphs — to HTML.
// It exists so /docs stays dependency-free; it is not a general Markdown engine.
func renderMarkdown(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var b strings.Builder
	inPara, inList, inCode := false, false, false
	listTag := "ul"

	closePara := func() {
		if inPara {
			b.WriteString("</p>\n")
			inPara = false
		}
	}
	closeList := func() {
		if inList {
			b.WriteString("</" + listTag + ">\n")
			inList = false
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				b.WriteString("</code></pre>\n")
				inCode = false
			} else {
				closePara()
				closeList()
				b.WriteString("<pre><code>")
				inCode = true
			}
			continue
		}
		if inCode {
			b.WriteString(esc(line) + "\n")
			continue
		}

		if trimmed == "" {
			closePara()
			closeList()
			continue
		}

		// Headings.
		if h := strings.IndexFunc(trimmed, func(r rune) bool { return r != '#' }); h > 0 && h <= 6 &&
			strings.HasPrefix(trimmed[h:], " ") {
			closePara()
			closeList()
			level := h + 1 // #  → h2, so the page's own <h1> stays unique
			if level > 6 {
				level = 6
			}
			b.WriteString(fmt.Sprintf("<h%d>%s</h%d>\n", level, inlineMarkdown(strings.TrimSpace(trimmed[h:])), level))
			continue
		}

		// Horizontal rule.
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			closePara()
			closeList()
			b.WriteString("<hr>\n")
			continue
		}

		// Pipe table: a header row followed by a |---|---| separator.
		if strings.HasPrefix(trimmed, "|") && i+1 < len(lines) && isTableSep(lines[i+1]) {
			closePara()
			closeList()
			b.WriteString(`<div class="scroll"><table><tr>`)
			for _, c := range splitRow(trimmed) {
				b.WriteString("<th>" + inlineMarkdown(c) + "</th>")
			}
			b.WriteString("</tr>\n")
			i++ // skip separator
			for i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "|") {
				i++
				b.WriteString("<tr>")
				for _, c := range splitRow(strings.TrimSpace(lines[i])) {
					b.WriteString("<td>" + inlineMarkdown(c) + "</td>")
				}
				b.WriteString("</tr>\n")
			}
			b.WriteString("</table></div>\n")
			continue
		}

		// Block quote.
		if strings.HasPrefix(trimmed, "> ") {
			closePara()
			closeList()
			b.WriteString("<blockquote>" + inlineMarkdown(trimmed[2:]) + "</blockquote>\n")
			continue
		}

		// Lists.
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			closePara()
			if inList && listTag != "ul" {
				closeList()
			}
			if !inList {
				listTag = "ul"
				b.WriteString("<ul>\n")
				inList = true
			}
			b.WriteString("<li>" + inlineMarkdown(trimmed[2:]) + "</li>\n")
			continue
		}
		if n := orderedItem(trimmed); n != "" {
			closePara()
			if inList && listTag != "ol" {
				closeList()
			}
			if !inList {
				listTag = "ol"
				b.WriteString("<ol>\n")
				inList = true
			}
			b.WriteString("<li>" + inlineMarkdown(n) + "</li>\n")
			continue
		}

		// Paragraph text.
		closeList()
		if !inPara {
			b.WriteString("<p>")
			inPara = true
		} else {
			b.WriteString(" ")
		}
		b.WriteString(inlineMarkdown(trimmed))
	}
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	closePara()
	closeList()
	return b.String()
}

func isTableSep(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "|") {
		return false
	}
	for _, r := range t {
		switch r {
		case '|', '-', ':', ' ':
		default:
			return false
		}
	}
	return strings.Contains(t, "-")
}

func splitRow(row string) []string {
	row = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(row), "|"), "|")
	parts := strings.Split(row, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// orderedItem returns the text of an "N. item" line, or "" if it is not one.
func orderedItem(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(s) || s[i] != '.' || s[i+1] != ' ' {
		return ""
	}
	return strings.TrimSpace(s[i+2:])
}

// inlineMarkdown escapes HTML and then applies inline code, bold, italics and
// links.  Code spans are extracted first so their contents are never re-styled.
func inlineMarkdown(s string) string {
	var spans []string
	var b strings.Builder
	for {
		i := strings.Index(s, "`")
		if i < 0 {
			b.WriteString(s)
			break
		}
		j := strings.Index(s[i+1:], "`")
		if j < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		b.WriteString("\x00" + strconv.Itoa(len(spans)) + "\x00")
		spans = append(spans, s[i+1:i+1+j])
		s = s[i+1+j+1:]
	}
	out := esc(b.String())
	out = replacePair(out, "**", "<strong>", "</strong>")
	out = replacePair(out, "*", "<em>", "</em>")
	out = markdownLinks(out)
	for i, sp := range spans {
		out = strings.ReplaceAll(out, "\x00"+strconv.Itoa(i)+"\x00", "<code>"+esc(sp)+"</code>")
	}
	return out
}

// replacePair turns matched delimiter pairs into open/close tags.
func replacePair(s, delim, open, close string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, delim)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.Index(s[i+len(delim):], delim)
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(open + s[i+len(delim):i+len(delim)+j] + close)
		s = s[i+len(delim)+j+len(delim):]
	}
}

// markdownLinks turns [text](href) into an anchor.  Only relative and http(s)
// targets are emitted, so a document cannot inject a javascript: URL.
func markdownLinks(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "[")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		close := strings.Index(s[i:], "](")
		if close < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := strings.Index(s[i+close:], ")")
		if end < 0 {
			b.WriteString(s)
			return b.String()
		}
		text := s[i+1 : i+close]
		href := s[i+close+2 : i+close+end]
		b.WriteString(s[:i])
		if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") ||
			strings.HasPrefix(href, "#") || strings.HasPrefix(href, "/") ||
			(!strings.Contains(href, ":") && href != "") {
			b.WriteString(`<a href="` + esc(href) + `">` + text + `</a>`)
		} else {
			b.WriteString(text)
		}
		s = s[i+close+end+1:]
	}
}

// ── Small formatting helpers ──────────────────────────────────────────────────

func esc(s string) string { return html.EscapeString(s) }

func itoa(i int) string { return strconv.Itoa(i) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func docsOptFloat(f *float64) string {
	if f == nil {
		return "—"
	}
	return ftoa(*f)
}

func docsOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return esc(s)
}

// docsGateText renders a state's operator-command gate as plain text for the
// /docs machines page. Callers esc() the result themselves.
//   - not operator-commandable at all: "not operator-commandable"
//   - operator-commandable, no gate:    "commandable from: any state"
//   - operator-commandable, gated:      "commandable from: a, b"
func docsGateText(st *statemachine.State) string {
	if !st.Operator {
		return "not operator-commandable"
	}
	from := st.OperatorFrom()
	if len(from) == 0 {
		return "commandable from: any state"
	}
	return "commandable from: " + strings.Join(from, ", ")
}

func docsParen(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return " <span class=\"tag\">" + esc(s) + "</span>"
}

func docsRoleTag(role string) string {
	if role == "" {
		return `<span class="tag">sensor</span>`
	}
	return `<span class="tag op">` + esc(role) + `</span>`
}

func docsSeverityTag(sev string) string {
	cls := "tag"
	switch sev {
	case "alarm":
		cls = "tag alarm"
	case "warning":
		cls = "tag daq"
	}
	return `<span class="` + cls + `">` + esc(sev) + `</span>`
}
