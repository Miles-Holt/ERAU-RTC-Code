package history

import (
	"testing"
	"time"
)

// t0 anchors every test's synthetic clock. Using a fixed wall-clock instant
// (rather than time.Now()) keeps the tests deterministic and fast — no
// time.Sleep, ever; every "later" timestamp is constructed by adding a
// duration to t0.
var t0 = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

func at(seconds float64) time.Time {
	return t0.Add(time.Duration(seconds * float64(time.Second)))
}

// unixAt is at(seconds) expressed as the float64 unix-second value Query
// takes for from/to — Record and Query talk in real wall-clock time, so
// query windows must be built from the same base as the recorded samples,
// not from a bare 0..N range.
func unixAt(seconds float64) float64 {
	return float64(at(seconds).UnixNano()) / 1e9
}

func TestQuery_SmoothSeriesRoundTrip(t *testing.T) {
	s := NewStore(time.Hour)
	// A steadily rising ramp, one sample per second for 10s: 0..9.
	for i := 0; i < 10; i++ {
		s.Record(at(float64(i)), map[string]float64{"OPT-01": float64(i)})
	}

	from := unixAt(0)
	to := unixAt(10)
	buckets := s.Query("OPT-01", from, to, 5) // 2s-wide buckets
	if len(buckets) != 5 {
		t.Fatalf("got %d buckets, want 5: %+v", len(buckets), buckets)
	}
	for i, b := range buckets {
		wantMin := float64(2 * i)
		wantMax := float64(2*i + 1)
		if b.Min != wantMin || b.Max != wantMax || b.Last != wantMax || b.N != 2 {
			t.Errorf("bucket %d = %+v, want min=%v max=%v last=%v n=2", i, b, wantMin, wantMax, wantMax)
		}
		wantT := from + float64(i)*2
		if b.T != wantT {
			t.Errorf("bucket %d T = %v, want %v", i, b.T, wantT)
		}
	}
}

func TestQuery_SpikyDiscreteSeriesRoundTrip(t *testing.T) {
	s := NewStore(time.Hour)
	// A toggling 0/1 series, matching a cmd-bool-shaped channel.
	vals := []float64{0, 0, 1, 1, 0, 1, 1, 1, 0, 0}
	for i, v := range vals {
		s.Record(at(float64(i)), map[string]float64{"NV-03-CMD": v})
	}

	buckets := s.Query("NV-03-CMD", unixAt(0), unixAt(10), 5) // 2s-wide buckets → pairs
	if len(buckets) != 5 {
		t.Fatalf("got %d buckets, want 5: %+v", len(buckets), buckets)
	}
	wantLast := []float64{0, 1, 1, 1, 0}
	wantMin := []float64{0, 1, 0, 1, 0}
	wantMax := []float64{0, 1, 1, 1, 0}
	for i, b := range buckets {
		if b.Last != wantLast[i] || b.Min != wantMin[i] || b.Max != wantMax[i] || b.N != 2 {
			t.Errorf("bucket %d = %+v, want last=%v min=%v max=%v n=2", i, b, wantLast[i], wantMin[i], wantMax[i])
		}
	}
}

func TestQuery_EmptyBucketsAreOmittedNotZeroFilled(t *testing.T) {
	s := NewStore(time.Hour)
	// Samples only in the first and last of 5 requested buckets — a gap in
	// the middle where nothing was recorded.
	s.Record(at(0.5), map[string]float64{"OPT-01": 1})
	s.Record(at(9.5), map[string]float64{"OPT-01": 2})

	from := unixAt(0)
	buckets := s.Query("OPT-01", from, unixAt(10), 5) // 2s-wide buckets
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2 (gap omitted): %+v", len(buckets), buckets)
	}
	if buckets[0].T != from || buckets[0].Last != 1 {
		t.Errorf("first bucket = %+v, want T=%v last=1", buckets[0], from)
	}
	if buckets[1].T != from+8 || buckets[1].Last != 2 {
		t.Errorf("second bucket = %+v, want T=%v last=2", buckets[1], from+8)
	}
}

func TestRecord_EvictsSamplesOlderThanRetention(t *testing.T) {
	retain := 5 * time.Second
	s := NewStore(retain)

	// Seed samples at t=0..4 (5 samples), all within the window as of t=4.
	for i := 0; i <= 4; i++ {
		s.Record(at(float64(i)), map[string]float64{"OPT-01": float64(i)})
	}
	if got := s.Query("OPT-01", unixAt(0), unixAt(5), 1); len(got) == 0 || got[0].N != 5 {
		t.Fatalf("before eviction: got %+v, want a single bucket with n=5", got)
	}

	// Advance well past the retention window. Samples at t=0..4 must be
	// evicted once a new Record call runs the eviction scan for this
	// channel; only t=20 itself remains.
	s.Record(at(20), map[string]float64{"OPT-01": 99})

	got := s.Query("OPT-01", unixAt(0), unixAt(21), 1)
	if len(got) != 1 {
		t.Fatalf("after eviction: got %d buckets, want 1 (only the fresh sample): %+v", len(got), got)
	}
	if got[0].N != 1 || got[0].Last != 99 {
		t.Errorf("after eviction: bucket = %+v, want n=1 last=99 (old samples evicted)", got[0])
	}
}

func TestQuery_BucketsLessThanOneTreatedAsOne(t *testing.T) {
	s := NewStore(time.Hour)
	s.Record(at(0), map[string]float64{"OPT-01": 1})
	s.Record(at(5), map[string]float64{"OPT-01": 2})

	for _, n := range []int{0, -1, -100} {
		got := s.Query("OPT-01", unixAt(0), unixAt(10), n)
		if len(got) != 1 {
			t.Fatalf("buckets=%d: got %d buckets, want 1: %+v", n, len(got), got)
		}
		if got[0].Min != 1 || got[0].Max != 2 || got[0].N != 2 {
			t.Errorf("buckets=%d: bucket = %+v, want min=1 max=2 n=2", n, got[0])
		}
	}
}

func TestQuery_ToBeforeOrEqualFromReturnsNil(t *testing.T) {
	s := NewStore(time.Hour)
	s.Record(at(0), map[string]float64{"OPT-01": 1})

	if got := s.Query("OPT-01", unixAt(10), unixAt(10), 5); got != nil {
		t.Errorf("to==from: got %+v, want nil", got)
	}
	if got := s.Query("OPT-01", unixAt(10), unixAt(5), 5); got != nil {
		t.Errorf("to<from: got %+v, want nil", got)
	}
}

func TestQuery_UnknownRefDesReturnsNilNotPanic(t *testing.T) {
	s := NewStore(time.Hour)
	s.Record(at(0), map[string]float64{"OPT-01": 1})

	got := s.Query("NO-SUCH-CHANNEL", unixAt(0), unixAt(10), 5)
	if got != nil {
		t.Errorf("unknown refDes: got %+v, want nil", got)
	}
}

// TestQuery_ShortWindowDegeneratesToRawPoints pins down the deliberate
// design decision documented on Query: there is no separate "give me raw
// points" code path. When the requested bucket width is narrower than the
// actual sample spacing, most buckets naturally end up holding exactly one
// sample, and for a 1-sample bucket min==max==last==that sample — i.e. it
// reads exactly like a raw point would, without any special-casing.
func TestQuery_ShortWindowDegeneratesToRawPoints(t *testing.T) {
	s := NewStore(time.Hour)
	// Samples 1 second apart.
	for i := 0; i < 5; i++ {
		s.Record(at(float64(i)), map[string]float64{"OPT-01": 100 + float64(i)})
	}

	// Ask for a 5s window in 5 buckets — 1s-wide buckets, matching the
	// sample spacing exactly, so every bucket should hold exactly 1 sample.
	buckets := s.Query("OPT-01", unixAt(0), unixAt(5), 5)
	if len(buckets) != 5 {
		t.Fatalf("got %d buckets, want 5: %+v", len(buckets), buckets)
	}
	for i, b := range buckets {
		want := 100 + float64(i)
		if b.N != 1 {
			t.Errorf("bucket %d: N = %d, want 1 (raw-equivalent)", i, b.N)
		}
		if b.Min != want || b.Max != want || b.Last != want {
			t.Errorf("bucket %d = %+v, want min=max=last=%v (raw-equivalent)", i, b, want)
		}
	}
}
