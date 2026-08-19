// Package history retains a bounded, per-channel rolling window of raw
// (t, value) samples in memory, so /api/history (controlnode/webclient) can
// serve server-side downsampled buckets instead of the browser buffering —
// and re-fetching on every panel open — every raw point itself.
//
// Today's control node has zero historical retention anywhere: broker.Broker
// only tracks the latest value per channel, and the browser's chart buffer
// only knows what has streamed by since a channel became active. This
// package is the fix on the server side of that gap — it does not decide
// what the browser does with the data, only makes "give me the last N
// minutes of OPT-01, downsampled to 400 points" answerable at all.
package history

import (
	"sync"
	"time"
)

// DefaultRetention is how far back the store keeps samples. Chosen to
// comfortably exceed the browser's maximum chart zoom window (1200s / 20min
// — see WebClient/js/graph.js attachScrollZoom's `Math.min(1200, ...)`
// clamp), with slack for a query issued right at that edge.
const DefaultRetention = 25 * time.Minute

// sample is one raw (t, value) point. t is Unix seconds, matching the "t"
// field on the broker's data/DataEvent path so no conversion happens on the
// hot ingestion path.
type sample struct {
	t float64
	v float64
}

// Store holds a rolling window of raw samples per channel, in memory only —
// nothing here is persisted across a restart, which matches the rest of the
// control node's telemetry model (the browser has never had history across
// a restart either).
type Store struct {
	// mu guards channels. Record is brief — a handful of map entries per
	// call — so a plain RWMutex over the whole map is fine at this data
	// rate: finer per-channel locking would be premature for tens of Hz
	// and a few dozen channels.
	mu       sync.RWMutex
	retain   time.Duration
	channels map[string][]sample
}

// NewStore creates a Store that retains samples for at most `retain`. A
// non-positive retain falls back to DefaultRetention rather than silently
// keeping nothing (or everything forever).
func NewStore(retain time.Duration) *Store {
	if retain <= 0 {
		retain = DefaultRetention
	}
	return &Store{
		retain:   retain,
		channels: make(map[string][]sample),
	}
}

// Record appends one timestamped batch of channel values. Call this with
// EVERY raw sample batch as it arrives at the broker (broker.dataIn), not
// the decimated broadcast tick, so history resolution is bounded by the
// DAQ's actual sample rate rather than broadcastRateHz — bucketing can only
// be as fine as what was actually recorded.
//
// Record is always called with non-decreasing t from a single goroutine in
// practice (the broker's Run loop), so eviction is a simple forward scan
// from the head of each touched channel's slice — no sort, no binary search
// needed. A caller that violates monotonic t (e.g. a test rewinding the
// clock) will not corrupt the store, but eviction may leave slightly more
// than `retain` of history behind until t catches back up.
func (s *Store) Record(t time.Time, values map[string]float64) {
	if len(values) == 0 {
		return
	}
	tf := float64(t.UnixNano()) / 1e9
	cutoff := tf - s.retain.Seconds()

	s.mu.Lock()
	defer s.mu.Unlock()
	for refDes, v := range values {
		buf := append(s.channels[refDes], sample{t: tf, v: v})

		// Evict everything older than the retention window. A plain
		// re-slice is the right idiom here, not a manual compaction
		// loop: the backing array's unused prefix capacity is bounded
		// (at most one retention window's worth per channel) and gets
		// reclaimed naturally as later appends grow the slice. It is
		// not a real leak — do not "fix" this into a copy.
		head := 0
		for head < len(buf) && buf[head].t < cutoff {
			head++
		}
		if head > 0 {
			buf = buf[head:]
		}
		s.channels[refDes] = buf
	}
}

// Bucket is one aggregated output point covering [T, T+width).
type Bucket struct {
	T    float64 `json:"t"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Last float64 `json:"last"`
	N    int     `json:"n"`
}

// Query divides [from, to) into `buckets` equal-width buckets and returns one
// Bucket per NON-EMPTY bucket — empty buckets are omitted entirely, not
// zero-filled. Callers should treat a gap in the returned slice as "no
// data", same as the browser's existing buildChartData already tolerates for
// its own live buffer. Buckets are returned in time order.
//
// A short/zoomed-in window naturally degenerates to ~raw points without a
// separate code path: once the requested bucket width drops below the
// channel's real sample spacing, most buckets end up with 0 or 1 samples,
// and a 1-sample bucket has min==max==last==that sample. This is deliberate
// — do not add a separate "give me raw" branch.
//
// buckets <= 0 is treated as 1. to <= from returns nil. An unknown refDes
// returns nil, not a panic.
func (s *Store) Query(refDes string, from, to float64, buckets int) []Bucket {
	if to <= from {
		return nil
	}
	if buckets <= 0 {
		buckets = 1
	}

	s.mu.RLock()
	buf := s.channels[refDes]
	// Copy the slice header under the lock; samples themselves are never
	// mutated in place (Record only appends/re-slices), so reading the
	// snapshot's elements after unlocking is safe.
	snap := buf
	s.mu.RUnlock()
	if len(snap) == 0 {
		return nil
	}

	width := (to - from) / float64(buckets)

	out := make([]Bucket, 0, buckets)
	var cur Bucket
	haveCur := false
	curIdx := -1 // which output bucket index cur belongs to

	// Single forward pass, advancing i monotonically — O(n) total across
	// every bucket, not O(n*buckets): the samples are already in time
	// order (Record only appends), so once a sample has been placed past
	// a given bucket's boundary, no earlier bucket ever needs revisiting.
	i := 0
	for i < len(snap) && snap[i].t < from {
		i++
	}
	for ; i < len(snap); i++ {
		sv := snap[i]
		if sv.t >= to {
			break
		}
		idx := int((sv.t - from) / width)
		if idx >= buckets {
			idx = buckets - 1
		}
		if idx < 0 {
			continue // shouldn't happen given the from-skip above
		}
		if !haveCur || idx != curIdx {
			if haveCur {
				out = append(out, cur)
			}
			cur = Bucket{
				T:    from + float64(idx)*width,
				Min:  sv.v,
				Max:  sv.v,
				Last: sv.v,
				N:    1,
			}
			curIdx = idx
			haveCur = true
			continue
		}
		if sv.v < cur.Min {
			cur.Min = sv.v
		}
		if sv.v > cur.Max {
			cur.Max = sv.v
		}
		cur.Last = sv.v
		cur.N++
	}
	if haveCur {
		out = append(out, cur)
	}
	return out
}
