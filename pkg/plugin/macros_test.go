package plugin

import (
	"testing"
	"time"
)

func TestExpandMacros(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 2, 12, 30, 45, 0, time.UTC)
	fromLit := "TIMESTAMP '2026-01-01 00:00:00.000'"
	toLit := "TIMESTAMP '2026-01-02 12:30:45.000'"

	cases := []struct {
		name, in, want string
	}{
		{"timeFilter",
			"SELECT * FROM t WHERE $__timeFilter(ts)",
			"SELECT * FROM t WHERE ts >= " + fromLit + " AND ts <= " + toLit},
		{"timeFilter with spaces",
			"WHERE $__timeFilter( ts )",
			"WHERE ts >= " + fromLit + " AND ts <= " + toLit},
		{"timeFilter qualified column",
			"WHERE $__timeFilter(e.created_at)",
			"WHERE e.created_at >= " + fromLit + " AND e.created_at <= " + toLit},
		{"timeFrom bare", "WHERE ts > $__timeFrom", "WHERE ts > " + fromLit},
		{"timeFrom parens", "WHERE ts > $__timeFrom()", "WHERE ts > " + fromLit},
		{"timeTo bare", "WHERE ts < $__timeTo", "WHERE ts < " + toLit},
		{"timeTo parens", "WHERE ts < $__timeTo()", "WHERE ts < " + toLit},
		{"multiple occurrences",
			"$__timeFilter(a) AND $__timeFilter(b)",
			"a >= " + fromLit + " AND a <= " + toLit + " AND b >= " + fromLit + " AND b <= " + toLit},
		{"no macros untouched",
			"SELECT 1 -- $__notAMacro",
			"SELECT 1 -- $__notAMacro"},
		{"non-UTC input normalized to UTC", // from/to converted below
			"$__timeFrom", "WHERE-INDEPENDENT"}, // replaced in the subtest
	}
	for _, c := range cases[:len(cases)-1] {
		t.Run(c.name, func(t *testing.T) {
			got := ExpandMacros(c.in, from, to)
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}

	t.Run("non-UTC input normalized to UTC", func(t *testing.T) {
		ist := time.FixedZone("IST", 5*3600+1800)
		got := ExpandMacros("$__timeFrom", from.In(ist), to)
		if got != fromLit {
			t.Fatalf("got %q want %q", got, fromLit)
		}
	})
}
