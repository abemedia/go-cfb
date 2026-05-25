package filetime_test

import (
	"math"
	"testing"
	"time"

	"github.com/abemedia/go-cfb/internal/filetime"
)

const (
	epochDeltaSecs = 11644473600
	ticksPerSec    = 10_000_000
	maxUnixSec     = math.MaxUint64/ticksPerSec - epochDeltaSecs - 1
)

func TestDecode(t *testing.T) {
	tests := []struct {
		name string
		ft   uint64
		want time.Time
	}{
		{"zero", 0, time.Time{}},
		{"unix epoch", 116444736000000000, time.Unix(0, 0).UTC()},
		{"sub-second", 116444736000000001, time.Unix(0, 100).UTC()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filetime.Decode(tt.ft)
			if !got.Equal(tt.want) {
				t.Errorf("Decode(%d) = %v, want %v", tt.ft, got, tt.want)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	tests := []struct {
		name    string
		t       time.Time
		want    uint64
		wantErr bool
	}{
		{"zero", time.Time{}, 0, false},
		{"unix epoch", time.Unix(0, 0).UTC(), 116444736000000000, false},
		{"sub-second", time.Unix(0, 100).UTC(), 116444736000000001, false},
		{"lower bound", time.Unix(-epochDeltaSecs, 0).UTC(), 0, false},
		{"upper bound", time.Unix(maxUnixSec, 0).UTC(), uint64(maxUnixSec+epochDeltaSecs) * ticksPerSec, false},
		{"before lower bound", time.Unix(-epochDeltaSecs-1, 0).UTC(), 0, true},
		{"after upper bound", time.Unix(maxUnixSec+1, 0).UTC(), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := filetime.Encode(tt.t)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Encode(%v) err = %v, wantErr %v", tt.t, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Encode(%v) = %d, want %d", tt.t, got, tt.want)
			}
		})
	}
}
