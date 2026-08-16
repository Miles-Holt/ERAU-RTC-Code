package main

import (
	"controlnode/broker"
	"controlnode/webclient"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbeddedWebClientServes verifies the production serving path: the WebClient
// baked into the binary (via //go:embed static) is served correctly when no
// -webroot override is given.  This is what the shipped controlnode.exe uses, and
// it is distinct from the -webroot path exercised by the webclient package tests.
func TestEmbeddedWebClientServes(t *testing.T) {
	embedded, err := fs.Sub(staticFiles, "static")
	if err != nil {
		t.Fatalf("fs.Sub(static): %v", err)
	}

	// Sanity: the embed must actually contain index.html (catches an empty or
	// missing static/ dir at build time).
	if _, err := fs.Stat(embedded, "index.html"); err != nil {
		t.Fatalf("embedded FS missing index.html — was build.bat run to populate static/? (%v)", err)
	}

	b := broker.New(nil, nil, nil)
	// webRoot="" forces the embedded FS path.
	s := webclient.New(0, `{"type":"config","controls":[]}`, nil, nil, nil, b, "", embedded, nil, nil, nil, nil)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/index.html")
	if err != nil {
		t.Fatalf("GET /index.html: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /index.html status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<html") {
		t.Error("served index.html does not look like HTML")
	}

	// Every js/ script referenced by index.html must also be present in the embed.
	for _, path := range scriptSrcs(string(body)) {
		if _, err := fs.Stat(embedded, path); err != nil {
			t.Errorf("index.html references %q but it is not in the embedded FS: %v", path, err)
		}
	}
}

// scriptSrcs extracts js/ script paths from index.html.
func scriptSrcs(html string) []string {
	var out []string
	for _, line := range strings.Split(html, "\n") {
		const marker = `src="`
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		rest := line[i+len(marker):]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			continue
		}
		if src := rest[:j]; strings.HasPrefix(src, "js/") {
			out = append(out, src)
		}
	}
	return out
}
