package fasttime

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Time time.Time

func (t *Time) UnmarshalJSON(data []byte) error {
	clean := strings.Trim(string(data), `"`)
	if clean == "" || clean == "null" {
		*t = Time{}
		return nil
	}
	ts, err := TS2int(clean)
	if err != nil {
		return err
	}
	*t = Time(time.UnixMicro(ts))
	return nil
}

func (t Time) MarshalJSON() ([]byte, error) {
	ts := time.Time(t).UnixMicro()
	return []byte(`"` + Int2TS(ts) + `"`), nil
}

// SlackString returns the time as a slack timestamp (i.e. "1234567890.123456"),
// or "" for the zero time, which Slack sends for a conversation never read.
func (t Time) SlackString() string {
	if time.Time(t).IsZero() {
		return ""
	}
	return Int2TS(time.Time(t).UnixMicro())
}

var ErrNotATimestamp = errors.New("not a slack timestamp")

// TS2int converts a slack timestamp to an int64 by stripping the dot and
// converting the string to an int64.  It is useful for fast comparison.
func TS2int(ts string) (int64, error) {
	if ts == "" {
		return 0, nil
	}
	i := strings.IndexByte(ts, '.')
	if i == -1 {
		return 0, fmt.Errorf("%w: %q", ErrNotATimestamp, ts)
	}
	return strconv.ParseInt(ts[:i]+ts[i+1:], 10, 64)
}

// Int2TS converts an int64 to a slack timestamp by inserting a dot in the
// right place.
func Int2TS(ts int64) string {
	const cut = 6
	s := strconv.FormatInt(ts, 10)
	l := len(s)
	if l < cut+1 {
		return ""
	}
	lo := s[l-cut:]
	hi := s[:l-cut]
	return hi + "." + lo
}
