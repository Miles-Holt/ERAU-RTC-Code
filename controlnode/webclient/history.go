package webclient

import (
	"controlnode/config"
	"controlnode/history"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// isDiscreteChannel mirrors WebClient/js/graph.js's graphChannelIsDiscrete
// (items 14/15/04) EXACTLY — same two config-channel cases, same reasoning:
// a channel that only ever takes a small fixed set of values must be
// bucketed by its LAST sample, not min/max, because "the value in between"
// doesn't mean anything for it (a state that never existed / a diagonal
// ramp between CMD=0 and CMD=1). The two branches below are:
//   - a boolean command: ch.Role == "cmd-bool"
//   - the boolean side of a valve's IO-CMD_IO-FB pair: role is "" or
//     "sensor" AND ctrl.Type == "valve" AND ctrl.SubType (case-insensitive)
//     contains "IO-FB" — NOT "POS-FB", which is a percentage.
//
// SM-<NAME>-STATE softchannels are NOT config.Control channels at all (see
// softchan.RegisterStateMachineChannels) so they can never be found here —
// see smStateRefDes / isDiscreteRefDes below, which handle them by regex
// separately, matching WebClient/js/graph.js's graphStateChannelMachine.
func isDiscreteChannel(ctrl config.Control, ch config.Channel) bool {
	if ch.Role == "cmd-bool" {
		return true
	}
	if (ch.Role == "" || ch.Role == "sensor") && ctrl.Type == "valve" {
		return strings.Contains(strings.ToUpper(ctrl.SubType), "IO-FB")
	}
	return false
}

// smStateRefDes matches an auto-generated state-machine index channel
// (SM-<NAME>-STATE). These never appear in config.Control — they are
// registered directly on the softchan store — so they can't go through
// isDiscreteChannel above; a request naming one is classified by name alone,
// same as graphStateChannelMachine does client-side.
var smStateRefDes = regexp.MustCompile(`^SM-.+-STATE$`)

// buildDiscreteChannelSet precomputes the config-channel half of the
// discreteness lookup once at Server construction, so a request doesn't
// rescan every control on every call.
func buildDiscreteChannelSet(controls []config.Control) map[string]bool {
	set := make(map[string]bool)
	for _, ctrl := range controls {
		for _, ch := range ctrl.Channels {
			if isDiscreteChannel(ctrl, ch) {
				set[ch.RefDes] = true
			}
		}
	}
	return set
}

// isDiscreteRefDes answers the discreteness question for one refDes at
// request time: the precomputed config-channel set first, then the
// state-machine name pattern for the channels that set can never contain.
func (s *Server) isDiscreteRefDes(refDes string) bool {
	if s.discreteChannels[refDes] {
		return true
	}
	return smStateRefDes.MatchString(refDes)
}

// historyChannelJSON is one entry of the "channels" map in the /api/history
// response.
type historyChannelJSON struct {
	Discrete bool             `json:"discrete"`
	Buckets  []history.Bucket `json:"buckets"`
}

// historyResponseJSON is the full /api/history response body.
type historyResponseJSON struct {
	Channels map[string]historyChannelJSON `json:"channels"`
}

// handleHistory serves GET /api/history?refDes=A&refDes=B&from=<unixSec>&to=<unixSec>&buckets=<N>
// — repeat refDes for multiple channels in one round trip (this is the whole
// point: a side panel or graph cell with several channels should not need
// one request per channel). See historyResponseJSON / historyChannelJSON for
// the response shape.
//
// Required, and 400 with a plain-text reason if missing/invalid:
//   - at least one refDes
//   - from, to as float unix-second strings, from < to
//   - buckets as an integer in [1, 2000]
//
// An unknown/mistyped refDes is NOT a 400 — it comes back with an empty
// buckets list, deliberately identical to how a real, configured channel
// with no recorded samples yet behaves. This is a deliberate simplification
// (documented in the worklog), not an oversight: validating refDes against
// the full known-channel set (config channels + every softchan) would need
// a second, separately-maintained name set for no real safety benefit, since
// a bad name already degrades gracefully to "no data".
//
// No auth: this is read-only telemetry on the same anonymous-read model as
// /ws/data, not a control surface.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		// A Server can legitimately be built without history wiring (many
		// tests do this deliberately, via New(..., nil, nil)) — that must
		// stay a working server for everything else, so this route alone
		// degrades rather than panicking on a nil store.
		http.Error(w, "history not available", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	refDesList := q["refDes"]
	if len(refDesList) == 0 {
		http.Error(w, "at least one refDes parameter is required", http.StatusBadRequest)
		return
	}

	fromStr, toStr := q.Get("from"), q.Get("to")
	if fromStr == "" || toStr == "" {
		http.Error(w, "from and to are required (unix seconds)", http.StatusBadRequest)
		return
	}
	from, err := strconv.ParseFloat(fromStr, 64)
	if err != nil {
		http.Error(w, "from must be a number (unix seconds)", http.StatusBadRequest)
		return
	}
	to, err := strconv.ParseFloat(toStr, 64)
	if err != nil {
		http.Error(w, "to must be a number (unix seconds)", http.StatusBadRequest)
		return
	}
	if from >= to {
		http.Error(w, "from must be less than to", http.StatusBadRequest)
		return
	}

	bucketsStr := q.Get("buckets")
	if bucketsStr == "" {
		http.Error(w, "buckets is required", http.StatusBadRequest)
		return
	}
	buckets, err := strconv.Atoi(bucketsStr)
	if err != nil || buckets < 1 || buckets > 2000 {
		http.Error(w, "buckets must be an integer in [1, 2000]", http.StatusBadRequest)
		return
	}

	resp := historyResponseJSON{Channels: make(map[string]historyChannelJSON, len(refDesList))}
	for _, refDes := range refDesList {
		b := s.history.Query(refDes, from, to, buckets)
		if b == nil {
			// Query returns nil for "no samples" (unknown channel or simply
			// nothing recorded yet in-window); the wire contract wants an
			// empty array there, not a JSON null.
			b = []history.Bucket{}
		}
		resp.Channels[refDes] = historyChannelJSON{
			Discrete: s.isDiscreteRefDes(refDes),
			Buckets:  b,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("webclient: marshal /api/history response: %v", err)
	}
}
