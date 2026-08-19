package webclient

import (
	"controlnode/alerts"
	"controlnode/broker"
	"controlnode/config"
	"controlnode/softchan"
	"controlnode/statemachine"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realConfigDir is declared in protocol_test.go — both suites drive the shipped
// configuration on purpose.

// docsTestServer builds a Server whose /docs pages are rendered from the REAL
// config directory.  Using the shipped configuration is the point: the test
// then fails if a page stops rendering something the config actually contains.
func docsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	if _, err := os.Stat(realConfigDir); err != nil {
		t.Skipf("config dir not found (%v)", err)
	}

	cfg, err := config.ParseDir(realConfigDir)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	sc, err := softchan.LoadFromDir(
		filepath.Join(realConfigDir, "channels"),
		filepath.Join(realConfigDir, "softChannelValues.yaml"))
	if err != nil {
		t.Fatalf("softchan: %v", err)
	}
	machineNames, err := statemachine.ScanMachineNames(filepath.Join(realConfigDir, "machines"))
	if err != nil {
		t.Fatalf("scan machines: %v", err)
	}
	sc.RegisterStateMachineChannels(machineNames)

	prog, err := statemachine.LoadDir(filepath.Join(realConfigDir, "machines"), statemachine.Options{})
	if err != nil {
		t.Fatalf("load machines: %v", err)
	}
	// The alert compiler checks every reference against the full channel space,
	// so assemble it the way main.go does: hardware refDes + software channels.
	known := make([]string, 0, 64)
	for refDes := range config.BuildRefDesMap(cfg) {
		known = append(known, refDes)
	}
	for refDes := range sc.RefDesMap() {
		known = append(known, refDes)
	}
	alertCfg, err := alerts.LoadDir(filepath.Join(realConfigDir, "alerts"), alerts.Options{
		KnownChannels: known,
		MachineNames:  machineNames,
	})
	if err != nil {
		t.Fatalf("load alerts: %v", err)
	}

	b := broker.New(nil, nil, nil)
	s := New(0, `{"type":"config","controls":[]}`, nil, nil, nil, b, "", nil, nil, nil, nil, nil, nil, nil)
	s.SetDocs(&DocsInput{
		System:       cfg,
		Program:      prog,
		Soft:         sc,
		Alerts:       alertCfg,
		AlertStaleMs: alertCfg.Template.StaleMs(),
		ProtocolPath: filepath.Join(realConfigDir, "..", "docs", "websocket-protocol.md"),
	})

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getDocsPage(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", path, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET %s content-type = %q, want text/html", path, ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(body) == 0 {
		t.Fatalf("GET %s returned an empty body", path)
	}
	return string(body)
}

// TestDocsPagesServe covers all five pages: each must answer 200 with HTML, and
// each must contain something only the real loaded config could have produced.
func TestDocsPagesServe(t *testing.T) {
	ts := docsTestServer(t)

	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/docs", []string{"System summary", "DAQ001", "engineTickRateHz"}},
		{"/docs/channels", []string{"Hardware channels", "Software channels", "SEQ-CUTOFF-T", "CPT-01"}},
		{"/docs/machines", []string{"firingSequence", "autoSequence", "postTest", "daq_local", "<svg"}},
		{"/docs/alerts", []string{"CHAMBER-HIGH", "every_daqnode", "disconnect"}},
		{"/docs/protocol", []string{"state_update", "sequence_complete", "config_req"}},
	} {
		body := getDocsPage(t, ts, tc.path)
		if !strings.Contains(body, "<html") {
			t.Errorf("%s: body does not look like HTML", tc.path)
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: page does not mention %q", tc.path, want)
			}
		}
	}
}

// TestDocsMachineTransitions checks that the machine page recovers the graph
// from the compiled AST for the real, shipped firingSequence machine: it runs
// entirely control-node side (no daq_local any more), so its edges are plain
// `transition` statements — including the completion edge into the new
// postTest state — rather than DAQ-reported sequence_complete/abort_triggered.
func TestDocsMachineTransitions(t *testing.T) {
	ts := docsTestServer(t)
	body := getDocsPage(t, ts, "/docs/machines")

	for _, want := range []string{
		"transition postTest", // autoSequence's nominal completion
		"transition abort",    // autoSequence's controller-guarded aborts
	} {
		if !strings.Contains(body, want) {
			t.Errorf("machines page missing %q", want)
		}
	}
}

// TestDocsMachineOperatorGate checks the machines page renders the
// `operator from a, b` gate text (docsGateText) for the real daq001.sm
// config: a gated state's allowed sources, and the "any state" phrasing for
// an ungated one if there is one, plus the "not operator-commandable" phrasing
// for a non-operator state.
func TestDocsMachineOperatorGate(t *testing.T) {
	ts := docsTestServer(t)
	body := getDocsPage(t, ts, "/docs/machines")

	for _, want := range []string{
		"commandable from: manualControl, abort, postTest",        // safe
		"commandable from: safe",                                  // manualControl
		"commandable from: manualControl",                         // autoSequence
		"commandable from: manualControl, autoSequence, postTest", // abort
		"operator command",                                        // SVG legend
	} {
		if !strings.Contains(body, want) {
			t.Errorf("machines page missing gate text %q", want)
		}
	}
}

// TestDocsUnknownPage404 keeps /docs from swallowing arbitrary paths.
func TestDocsUnknownPage404(t *testing.T) {
	ts := docsTestServer(t)
	resp, err := http.Get(ts.URL + "/docs/nope")
	if err != nil {
		t.Fatalf("GET /docs/nope: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /docs/nope status = %d, want 404", resp.StatusCode)
	}
}

// TestDocsWithoutConfig covers the degraded path: a server with no docs
// attached must still answer, saying plainly that nothing is loaded.
func TestDocsWithoutConfig(t *testing.T) {
	b := broker.New(nil, nil, nil)
	s := New(0, `{"type":"config"}`, nil, nil, nil, b, "", nil, nil, nil, nil, nil, nil, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, path := range []string{"/docs", "/docs/channels", "/docs/machines", "/docs/alerts", "/docs/protocol"} {
		body := getDocsPage(t, ts, path)
		if !strings.Contains(body, "<html") {
			t.Errorf("%s: body does not look like HTML", path)
		}
	}
	// The embedded protocol copy must be usable with no config at all — that is
	// the deployed-exe path.
	if body := getDocsPage(t, ts, "/docs/protocol"); !strings.Contains(body, "state_update") {
		t.Error("embedded protocol doc did not render")
	}
}

// TestRenderMarkdown exercises the small Markdown subset the protocol page uses.
func TestRenderMarkdown(t *testing.T) {
	got := renderMarkdown("# Title\n\nSome `code` and **bold**.\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n```\nraw <tag>\n```\n")
	for _, want := range []string{
		"<h2>Title</h2>",
		"<code>code</code>",
		"<strong>bold</strong>",
		"<th>A</th>",
		"<td>1</td>",
		"raw &lt;tag&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered markdown missing %q\ngot: %s", want, got)
		}
	}
	if strings.Contains(got, "<tag>") {
		t.Error("markdown renderer emitted unescaped HTML from a code block")
	}
}
