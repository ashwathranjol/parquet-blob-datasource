package plugin

import (
	"regexp"
	"strings"
	"time"
)

// Grafana macros supported in v1: $__timeFilter(col), $__timeFrom, $__timeTo
// (with or without trailing parens). All literals are UTC — the engine also
// runs with TimeZone='UTC', so naive TIMESTAMP literals compare correctly
// against both TIMESTAMP and TIMESTAMPTZ columns.

var (
	timeFilterRe = regexp.MustCompile(`\$__timeFilter\(\s*([^)]+?)\s*\)`)
	timeFromRe   = regexp.MustCompile(`\$__timeFrom(\(\))?`)
	timeToRe     = regexp.MustCompile(`\$__timeTo(\(\))?`)
)

func tsLiteral(t time.Time) string {
	return "TIMESTAMP '" + t.UTC().Format("2006-01-02 15:04:05.000") + "'"
}

func ExpandMacros(rawSQL string, from, to time.Time) string {
	fromLit, toLit := tsLiteral(from), tsLiteral(to)
	out := timeFilterRe.ReplaceAllStringFunc(rawSQL, func(m string) string {
		col := strings.TrimSpace(timeFilterRe.FindStringSubmatch(m)[1])
		return col + " >= " + fromLit + " AND " + col + " <= " + toLit
	})
	out = timeFromRe.ReplaceAllString(out, fromLit)
	out = timeToRe.ReplaceAllString(out, toLit)
	return out
}
