// Package filetime converts between Windows FILETIME values and [time.Time].
//
// A FILETIME is the number of 100-nanosecond ticks since 1601-01-01 UTC,
// stored as an unsigned 64-bit integer. The representable range spans roughly
// year 1601 to year 60056.
package filetime

import (
	"errors"
	"math"
	"time"
)

const (
	epochDeltaSecs = 11644473600 // 1601-01-01 to the Unix epoch
	ticksPerSec    = 10_000_000
	nanosPerTick   = 100
	maxUnixSec     = math.MaxUint64/ticksPerSec - epochDeltaSecs - 1 // year ~60056
)

// Decode converts a FILETIME tick count to a UTC [time.Time].
// A zero ft returns the zero time.
func Decode(ft uint64) time.Time {
	if ft == 0 {
		return time.Time{}
	}
	sec, rem := ft/ticksPerSec, ft%ticksPerSec
	return time.Unix(int64(sec)-epochDeltaSecs, int64(rem)*nanosPerTick).UTC()
}

// Encode converts t to FILETIME ticks. A zero t encodes as 0 (unset).
// Returns an error if t falls outside the representable range.
func Encode(t time.Time) (uint64, error) {
	if t.IsZero() {
		return 0, nil
	}
	sec, nsec := t.Unix(), int64(t.Nanosecond())
	if sec < -epochDeltaSecs {
		return 0, errors.New("cannot encode timestamp before year 1601")
	}
	if sec > maxUnixSec {
		return 0, errors.New("cannot encode timestamp after year 60056")
	}
	return uint64(sec+epochDeltaSecs)*ticksPerSec + uint64(nsec/nanosPerTick), nil
}
