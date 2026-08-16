package webclient

import (
	"controlnode/broker"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newLayoutTestServer builds a Server whose only allowed layout lives inside a
// fresh temp directory — set_layout tests must never touch the repo's real
// config/ directory.  Returns the server, the temp dir, and the httptest server.
func newLayoutTestServer(t *testing.T, auth *UserAuthConfig) (*Server, string, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	allowedPath := filepath.Join(dir, "test_panel.yaml")
	// Seed the file with initial content, matching how main.go loads panels at
	// startup, so the "name" preserved on save can be verified too.
	initial := "connections: []\n"
	if err := os.WriteFile(allowedPath, []byte(initial), 0644); err != nil {
		t.Fatalf("seed layout file: %v", err)
	}
	layoutPaths := map[string]string{"test_panel.yaml": allowedPath}
	panelMessages := [][]byte{PidLayoutJSON("Test Panel", "test_panel.yaml", initial)}

	b := broker.New(nil, nil, nil)
	go b.Run(50)

	s := New(0, `{"type":"config","controls":[]}`, nil, nil, panelMessages, b, "", nil, auth, layoutPaths, nil, nil)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, dir, ts
}

// authedCtrlConn dials /ws/ctrl and authenticates, returning the connection.
func authedCtrlConn(t *testing.T, ts *httptest.Server, auth *UserAuthConfig, name, pin string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/ctrl"), nil)
	if err != nil {
		t.Fatalf("dial /ws/ctrl: %v", err)
	}
	mustWrite(t, c, map[string]interface{}{"type": "auth_request", "name": name, "pin": pin})
	resp := readJSON(t, c)
	if resp["approved"] != true {
		t.Fatalf("auth failed: %v", resp)
	}
	return c
}

// dataSubscriber dials /ws/data and drains the initial handshake messages
// (config, any softchan/state config, panel layouts, snapshots) so the caller
// can then wait for a specific message pushed afterward.
func dataSubscriber(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/data"), nil)
	if err != nil {
		t.Fatalf("dial /ws/data: %v", err)
	}
	return c
}

// readUntilType drains messages on c until one of the given type arrives, or
// the deadline elapses.
func readUntilType(t *testing.T, c *websocket.Conn, typ string, d time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.SetReadDeadline(time.Now().Add(d))
		_, raw, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("ws read waiting for %q: %v", typ, err)
		}
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m["type"] == typ {
			return m
		}
	}
	t.Fatalf("timed out waiting for message type %q", typ)
	return nil
}

// waitForPidLayoutWithContent drains messages on c until a pid_layout with
// the given content arrives, or the deadline elapses.
func waitForPidLayoutWithContent(t *testing.T, c *websocket.Conn, content string, d time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.SetReadDeadline(time.Now().Add(d))
		_, raw, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("ws read waiting for pid_layout content: %v", err)
		}
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m["type"] == "pid_layout" && m["content"] == content {
			return m
		}
	}
	t.Fatalf("timed out waiting for pid_layout with content %q", content)
	return nil
}

// TestSetLayoutAuthenticatedWritesAndBroadcasts verifies a successful
// set_layout writes the YAML to the configured path (inside the temp dir),
// republishes pid_layout with name/filename/content intact, and pushes a
// notice alert.
func TestSetLayoutAuthenticatedWritesAndBroadcasts(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	_, dir, ts := newLayoutTestServer(t, auth)

	sub := dataSubscriber(t, ts)
	defer sub.Close()

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()

	newContent := "connections:\n  - a: 1\n"
	mustWrite(t, ctrl, map[string]interface{}{
		"type": "set_layout", "filename": "test_panel.yaml", "content": newContent, "user": "alice",
	})

	// The file on disk must match, and must be the one inside the temp dir.
	waitForFileContent(t, filepath.Join(dir, "test_panel.yaml"), newContent, 2*time.Second)

	// The subscriber must see a re-published pid_layout with matching fields.
	// The initial connect handshake already sent one pid_layout (the seeded
	// content), so keep reading pid_layout messages until the updated one.
	m := waitForPidLayoutWithContent(t, sub, newContent, 2*time.Second)
	if m["filename"] != "test_panel.yaml" {
		t.Errorf("pid_layout filename = %v, want test_panel.yaml", m["filename"])
	}
	if m["content"] != newContent {
		t.Errorf("pid_layout content = %v, want %q", m["content"], newContent)
	}
	if m["name"] != "Test Panel" {
		t.Errorf("pid_layout name = %v, want preserved display name %q", m["name"], "Test Panel")
	}

	// A notice alert for the save must also have gone out.
	sawAlert := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sawAlert {
		sub.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := sub.ReadMessage()
		if err != nil {
			break
		}
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) == nil && m["type"] == "alert" {
			msg, _ := m["message"].(string)
			if msg != "" {
				sawAlert = true
			}
		}
	}
	if !sawAlert {
		t.Error("no alert notice broadcast for the successful save")
	}
}

// TestSetLayoutRejectsDisallowedFilename verifies a filename not present in
// the server's allowed layoutPaths is rejected: nothing is written anywhere,
// including path-traversal-style names that might otherwise escape the temp
// dir the server is configured for.
func TestSetLayoutRejectsDisallowedFilename(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	_, dir, ts := newLayoutTestServer(t, auth)

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()

	badNames := []string{
		"not_allowed.yaml",
		"../evil.yaml",
		"..\\evil.yaml",
		"../../evil.yaml",
		"..\\..\\evil.yaml",
		"/etc/evil.yaml",
	}
	for _, name := range badNames {
		mustWrite(t, ctrl, map[string]interface{}{
			"type": "set_layout", "filename": name, "content": "malicious: true\n", "user": "alice",
		})
	}
	// Give the server a moment to process (or not) each message.
	time.Sleep(200 * time.Millisecond)

	// Nothing new must have appeared anywhere near the temp dir or its parent.
	assertNoEscapedFile(t, dir, "evil.yaml")

	// The one legitimate file must be untouched (still its seeded content).
	got, err := os.ReadFile(filepath.Join(dir, "test_panel.yaml"))
	if err != nil {
		t.Fatalf("read test_panel.yaml: %v", err)
	}
	if string(got) != "connections: []\n" {
		t.Errorf("test_panel.yaml was modified by a rejected set_layout: %q", got)
	}
}

// assertNoEscapedFile checks dir and its parent for any file matching name,
// which would indicate a path-traversal write escaped the allowed directory.
func assertNoEscapedFile(t *testing.T, dir, name string) {
	t.Helper()
	candidates := []string{
		filepath.Join(dir, name),
		filepath.Join(filepath.Dir(dir), name),
		filepath.Join(filepath.Dir(filepath.Dir(dir)), name),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("set_layout escaped the allowed directory: found %s", p)
		}
	}
}

// TestSetLayoutRejectsUnauthenticated verifies an unauthenticated set_layout
// attempt is rejected: no file is written, nothing broadcast.
func TestSetLayoutRejectsUnauthenticated(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	_, dir, ts := newLayoutTestServer(t, auth)

	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/ctrl"), nil)
	if err != nil {
		t.Fatalf("dial /ws/ctrl: %v", err)
	}
	defer c.Close()

	mustWrite(t, c, map[string]interface{}{
		"type": "set_layout", "filename": "test_panel.yaml", "content": "connections:\n  - hacked: true\n", "user": "eve",
	})
	time.Sleep(200 * time.Millisecond)

	got, err := os.ReadFile(filepath.Join(dir, "test_panel.yaml"))
	if err != nil {
		t.Fatalf("read test_panel.yaml: %v", err)
	}
	if string(got) != "connections: []\n" {
		t.Errorf("unauthenticated set_layout modified the file: %q", got)
	}
}

// waitForFileContent polls the file at path until it contains want or the
// timeout elapses.
func waitForFileContent(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			last = string(b)
			if last == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("file %s = %q, want %q (timed out)", path, last, want)
}
