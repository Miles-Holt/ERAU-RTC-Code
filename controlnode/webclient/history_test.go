package webclient

import (
	"controlnode/broker"
	"controlnode/config"
	"controlnode/history"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// historyTestControls covers the five cases isDiscreteChannel /
// isDiscreteRefDes must tell apart:
//   - PT-01:     a plain sensor (continuous)
//   - OV-05-CMD: a cmd-bool command (discrete)
//   - NV-03-CMD/-FB: an IO-CMD_IO-FB valve pair (FB discrete)
//   - TV-01-CMD/-FB: a POS-CMD_POS-FB valve pair (FB continuous)
//
// SM-fuelSeq-STATE is deliberately NOT one of these controls — it must be
// classified discrete purely by the smStateRefDes regex, proving that path
// works standalone with no matching config.Control at all.
func historyTestControls() []config.Control {
	return []config.Control{
		{
			RefDes: "PT-01",
			Type:   "pressure",
			Channels: []config.Channel{
				{RefDes: "PT-01", Role: ""},
			},
		},
		{
			RefDes: "OV-05",
			Type:   "digitalOut",
			Channels: []config.Channel{
				{RefDes: "OV-05-CMD", Role: "cmd-bool"},
			},
		},
		{
			RefDes:  "NV-03",
			Type:    "valve",
			SubType: "IO-CMD_IO-FB",
			Channels: []config.Channel{
				{RefDes: "NV-03-CMD", Role: "cmd-bool"},
				{RefDes: "NV-03-FB", Role: ""},
			},
		},
		{
			RefDes:  "TV-01",
			Type:    "valve",
			SubType: "POS-CMD_POS-FB",
			Channels: []config.Channel{
				{RefDes: "TV-01-CMD", Role: "cmd-pct"},
				{RefDes: "TV-01-FB", Role: ""},
			},
		},
	}
}

// newHistoryTestServer builds a Server wired to a real history.Store and the
// controls above, over httptest. Pass store=nil to exercise the
// no-history-wired (503) path.
func newHistoryTestServer(t *testing.T, store *history.Store) (*Server, *httptest.Server) {
	t.Helper()
	b := broker.New(nil, nil, nil)
	go b.Run(50)
	s := New(0, `{"type":"config","controls":[]}`, nil, nil, nil, b, "", nil, nil, nil, nil, nil,
		store, historyTestControls())
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// getHistory issues a GET to /api/history built from the given query values
// and returns the raw status code and body.
func getHistory(t *testing.T, ts *httptest.Server, query string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/history?" + query)
	if err != nil {
		t.Fatalf("GET /api/history?%s: %v", query, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, body
}

func TestHistory_DiscreteClassification(t *testing.T) {
	store := history.NewStore(time.Hour)
	_, ts := newHistoryTestServer(t, store)

	cases := []struct {
		refDes string
		want   bool
	}{
		{"PT-01", false},           // plain sensor
		{"OV-05-CMD", true},        // cmd-bool command
		{"NV-03-FB", true},         // IO-CMD_IO-FB valve pair's FB
		{"TV-01-FB", false},        // POS-CMD_POS-FB valve pair's FB
		{"SM-fuelSeq-STATE", true}, // state-machine index, no config.Control at all
	}

	q := ""
	for i, c := range cases {
		if i > 0 {
			q += "&"
		}
		q += "refDes=" + c.refDes
	}
	q += "&from=0&to=100&buckets=10"

	status, body := getHistory(t, ts, q)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var resp historyResponseJSON
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	for _, c := range cases {
		ch, ok := resp.Channels[c.refDes]
		if !ok {
			t.Errorf("%s: missing from response", c.refDes)
			continue
		}
		if ch.Discrete != c.want {
			t.Errorf("%s: discrete = %v, want %v", c.refDes, ch.Discrete, c.want)
		}
	}
}

func TestHistory_BucketAggregationKnownDataset(t *testing.T) {
	store := history.NewStore(time.Hour)
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		store.Record(base.Add(time.Duration(i)*time.Second), map[string]float64{"PT-01": float64(i)})
	}
	_, ts := newHistoryTestServer(t, store)

	from := float64(base.Unix())
	to := from + 10
	q := fmt.Sprintf("refDes=PT-01&from=%f&to=%f&buckets=5", from, to)
	status, body := getHistory(t, ts, q)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var resp historyResponseJSON
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	ch := resp.Channels["PT-01"]
	if len(ch.Buckets) != 5 {
		t.Fatalf("got %d buckets, want 5: %+v", len(ch.Buckets), ch.Buckets)
	}
	for i, b := range ch.Buckets {
		wantMin := float64(2 * i)
		wantMax := float64(2*i + 1)
		if b.Min != wantMin || b.Max != wantMax || b.Last != wantMax || b.N != 2 {
			t.Errorf("bucket %d = %+v, want min=%v max=%v last=%v n=2", i, b, wantMin, wantMax, wantMax)
		}
	}
}

func TestHistory_MultiRefDesReturnsAllChannels(t *testing.T) {
	store := history.NewStore(time.Hour)
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.Record(base, map[string]float64{"PT-01": 1, "OV-05-CMD": 1})
	_, ts := newHistoryTestServer(t, store)

	from := float64(base.Unix()) - 1
	to := float64(base.Unix()) + 1
	q := fmt.Sprintf("refDes=PT-01&refDes=OV-05-CMD&from=%f&to=%f&buckets=1", from, to)
	status, body := getHistory(t, ts, q)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var resp historyResponseJSON
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if len(resp.Channels) != 2 {
		t.Fatalf("got %d channels, want 2: %+v", len(resp.Channels), resp.Channels)
	}
	if _, ok := resp.Channels["PT-01"]; !ok {
		t.Error("missing PT-01")
	}
	if _, ok := resp.Channels["OV-05-CMD"]; !ok {
		t.Error("missing OV-05-CMD")
	}
}

func TestHistory_BadRequests(t *testing.T) {
	store := history.NewStore(time.Hour)
	_, ts := newHistoryTestServer(t, store)

	cases := []struct {
		name  string
		query string
	}{
		{"no refDes", "from=0&to=10&buckets=5"},
		{"no from", "refDes=PT-01&to=10&buckets=5"},
		{"no to", "refDes=PT-01&from=0&buckets=5"},
		{"malformed from", "refDes=PT-01&from=abc&to=10&buckets=5"},
		{"malformed to", "refDes=PT-01&from=0&to=xyz&buckets=5"},
		{"from >= to", "refDes=PT-01&from=10&to=10&buckets=5"},
		{"no buckets", "refDes=PT-01&from=0&to=10"},
		{"buckets = 0", "refDes=PT-01&from=0&to=10&buckets=0"},
		{"buckets negative", "refDes=PT-01&from=0&to=10&buckets=-1"},
		{"buckets too large", "refDes=PT-01&from=0&to=10&buckets=2001"},
		{"buckets not an integer", "refDes=PT-01&from=0&to=10&buckets=1.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := getHistory(t, ts, c.query)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", status, body)
			}
		})
	}
}

func TestHistory_UnknownRefDesReturns200WithEmptyBuckets(t *testing.T) {
	store := history.NewStore(time.Hour)
	_, ts := newHistoryTestServer(t, store)

	status, body := getHistory(t, ts, "refDes=NO-SUCH-CHANNEL&from=0&to=10&buckets=5")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", status, body)
	}
	var resp historyResponseJSON
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	ch, ok := resp.Channels["NO-SUCH-CHANNEL"]
	if !ok {
		t.Fatal("missing NO-SUCH-CHANNEL from response")
	}
	if len(ch.Buckets) != 0 {
		t.Errorf("buckets = %+v, want empty", ch.Buckets)
	}
	if ch.Discrete {
		t.Errorf("discrete = true, want false for an unrecognized name")
	}
}

func TestHistory_NilStoreReturns503(t *testing.T) {
	_, ts := newHistoryTestServer(t, nil)

	status, body := getHistory(t, ts, "refDes=PT-01&from=0&to=10&buckets=5")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", status, body)
	}
}
