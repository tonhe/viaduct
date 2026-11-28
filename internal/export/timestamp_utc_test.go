package export

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// A report's timestamp is in UTC whatever the host's time zone, so that every
// timestamp in a multi-target export is in the same zone as the envelope's
// (viaduct#108 item 9).
func TestBuildReport_TimestampIsUTC(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("test", 5*3600+30*60)
	t.Cleanup(func() { time.Local = saved })

	before := time.Now()
	r := BuildReport("v", "target.example", net.ParseIP("192.0.2.1"), "udp", hop.NewTable(30), nil, nil, 0)
	after := time.Now()

	if r.Timestamp.Location() != time.UTC {
		t.Errorf("timestamp zone = %v, want UTC", r.Timestamp.Location())
	}
	if r.Timestamp.Before(before) || r.Timestamp.After(after) {
		t.Errorf("timestamp %v is not the time the report was built (%v to %v)", r.Timestamp, before, after)
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(doc.Timestamp, "Z") {
		t.Errorf("JSON timestamp = %q, want it to end in Z", doc.Timestamp)
	}
}
