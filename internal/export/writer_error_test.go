package export

// Every writer hands back the error of the writer it is given, for a report
// of any size (viaduct#284). Before, the CSV writers returned nil when the
// report fitted their 4 KB buffer, and the DOT writers returned nil always, so
// an export to a full disk was reported as done.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

var errBrokenWriter = errors.New("no space left on device")

// brokenWriter accepts limit bytes and fails every write that would pass it.
// It counts the writes it refused.
type brokenWriter struct {
	limit   int
	written int
	refused int
}

func (b *brokenWriter) Write(p []byte) (int, error) {
	if b.written+len(p) > b.limit {
		b.refused++
		return 0, errBrokenWriter
	}
	b.written += len(p)
	return len(p), nil
}

// sizedReport is a report with the given number of hops: a node with a
// hostname and an AS number, then a silent hop, in turn.
func sizedReport(target string, hops int) Report {
	r := Report{Target: target}
	for ttl := 1; ttl <= hops; ttl++ {
		if ttl%2 == 0 {
			r.Hops = append(r.Hops, Hop{TTL: ttl, ProbesSent: 7})
			continue
		}
		r.Hops = append(r.Hops, Hop{TTL: ttl, ProbesSent: 7, LossKind: "measured", Nodes: []Node{{
			IP: fmt.Sprintf("192.0.2.%d", ttl%250+1), Hostname: "a-hostname.example", ASN: 64500, ASOrg: "EXAMPLE-NET",
		}}})
	}
	return r
}

// exportWriters is the six writers, each as a function of a report.
var exportWriters = []struct {
	name  string
	write func(io.Writer, Report) error
}{
	{"WriteJSON", WriteJSON},
	{"WriteCSV", WriteCSV},
	{"WriteDOT", WriteDOT},
	{"WriteJSONMulti", func(w io.Writer, r Report) error {
		return WriteJSONMulti(w, MultiReport{Targets: []Report{r, r}})
	}},
	{"WriteCSVMulti", func(w io.Writer, r Report) error {
		return WriteCSVMulti(w, MultiReport{Targets: []Report{r, r}})
	}},
	{"WriteDOTMulti", func(w io.Writer, r Report) error {
		return WriteDOTMulti(w, MultiReport{Targets: []Report{r, r}})
	}},
}

// A writer that fails makes the export fail, whatever the format, whatever the
// size of the report, and wherever in the output the failure comes: at the
// first byte, part of the way through, or one byte from the end.
func TestWriters_ReturnTheWriterError(t *testing.T) {
	sizes := []struct {
		name string
		hops int
	}{
		{"an empty report", 0},
		{"a small report (4 hops)", 4},
		{"a large report (300 hops)", 300},
	}
	for _, wr := range exportWriters {
		for _, size := range sizes {
			r := sizedReport("target.example", size.hops)
			var whole bytes.Buffer
			if err := wr.write(&whole, r); err != nil {
				t.Fatalf("%s, %s, to a buffer: %v", wr.name, size.name, err)
			}
			total := whole.Len()
			if total == 0 {
				t.Fatalf("%s, %s: wrote nothing to a buffer", wr.name, size.name)
			}
			for _, limit := range []int{0, total / 2, total - 1} {
				t.Run(fmt.Sprintf("%s/%s/fails after %d of %d bytes", wr.name, size.name, limit, total), func(t *testing.T) {
					w := &brokenWriter{limit: limit}
					err := wr.write(w, r)
					if !errors.Is(err, errBrokenWriter) {
						t.Errorf("error = %v, want the writer's error (%v)", err, errBrokenWriter)
					}
				})
			}
		}
	}
}

// A writer that takes every byte gives no error, and the output is the same
// whether it is taken whole or a byte at a time.
func TestWriters_NoErrorWhenTheWriterTakesEverything(t *testing.T) {
	for _, wr := range exportWriters {
		for _, hops := range []int{0, 4, 300} {
			r := sizedReport("target.example", hops)
			var whole bytes.Buffer
			if err := wr.write(&whole, r); err != nil {
				t.Fatalf("%s, %d hops: %v", wr.name, hops, err)
			}
			exact := &brokenWriter{limit: whole.Len()}
			if err := wr.write(exact, r); err != nil {
				t.Errorf("%s, %d hops, to a writer with room for exactly the output: %v", wr.name, hops, err)
			}
			if exact.written != whole.Len() || exact.refused != 0 {
				t.Errorf("%s, %d hops: wrote %d of %d bytes, %d writes refused", wr.name, hops, exact.written, whole.Len(), exact.refused)
			}
		}
	}
}

// Once the writer has failed, the DOT writers stop writing to it: one refused
// write, not one for every line that follows.
func TestWriteDOT_StopsWritingAfterTheFirstError(t *testing.T) {
	r := sizedReport("target.example", 300)
	for _, wr := range exportWriters {
		if wr.name != "WriteDOT" && wr.name != "WriteDOTMulti" {
			continue
		}
		w := &brokenWriter{limit: 200}
		if err := wr.write(w, r); !errors.Is(err, errBrokenWriter) {
			t.Fatalf("%s: error = %v, want the writer's error", wr.name, err)
		}
		if w.refused != 1 {
			t.Errorf("%s: %d writes reached the writer after it failed, want 1 (the one that failed)", wr.name, w.refused)
		}
	}
}

// firstErrWriter keeps the first error and lets nothing through after it. A
// write the underlying writer cuts short without an error counts as one.
func TestFirstErrWriter(t *testing.T) {
	t.Run("passes writes through until one fails", func(t *testing.T) {
		under := &brokenWriter{limit: 5}
		f := &firstErrWriter{w: under}
		if n, err := f.Write([]byte("abc")); n != 3 || err != nil {
			t.Fatalf("first write = %d, %v; want 3, nil", n, err)
		}
		if f.err != nil {
			t.Fatalf("err = %v after a write that succeeded", f.err)
		}
		if n, err := f.Write([]byte("defg")); n != 0 || !errors.Is(err, errBrokenWriter) {
			t.Fatalf("write past the limit = %d, %v; want 0 and the writer's error", n, err)
		}
		// Room again in the underlying writer makes no difference.
		under.limit = 100
		if n, err := f.Write([]byte("h")); n != 0 || !errors.Is(err, errBrokenWriter) {
			t.Errorf("write after the failure = %d, %v; want 0 and the first error", n, err)
		}
		if !errors.Is(f.err, errBrokenWriter) {
			t.Errorf("err = %v, want the first error kept", f.err)
		}
		if under.written != 3 || under.refused != 1 {
			t.Errorf("the underlying writer took %d bytes and refused %d writes; want 3 and 1", under.written, under.refused)
		}
	})
	t.Run("a short write without an error is an error", func(t *testing.T) {
		f := &firstErrWriter{w: shortWriter{}}
		if n, err := f.Write([]byte("abcdef")); n != 2 || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("write = %d, %v; want 2 and io.ErrShortWrite", n, err)
		}
		if !errors.Is(f.err, io.ErrShortWrite) {
			t.Errorf("err = %v, want io.ErrShortWrite kept", f.err)
		}
	})
}

// shortWriter takes two bytes of every write and reports no error.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		return 2, nil
	}
	return len(p), nil
}
