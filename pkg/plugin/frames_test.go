package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func frameFor(t *testing.T, query string) (interface{ Rows() int }, bool, error) {
	t.Helper()
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), query, 1000)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	return FrameFromRows(rows, "A", 1000)
}

func TestFrameFromRows_AllSupportedTypes(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), `SELECT
		TIMESTAMP '2026-01-01 10:20:30' AS c_ts,
		TIMESTAMPTZ '2026-01-01 10:20:30+00' AS c_tstz,
		CAST(1 AS TINYINT) AS c_i8,
		CAST(2 AS SMALLINT) AS c_i16,
		CAST(3 AS INTEGER) AS c_i32,
		CAST(4 AS BIGINT) AS c_i64,
		CAST(5 AS HUGEINT) AS c_i128,
		CAST(1.5 AS FLOAT) AS c_f32,
		CAST(2.5 AS DOUBLE) AS c_f64,
		CAST(3.25 AS DECIMAL(18,3)) AS c_dec,
		'hello' AS c_str,
		true AS c_bool`, 1000)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	frame, truncated, err := FrameFromRows(rows, "A", 1000)
	if err != nil {
		t.Fatalf("FrameFromRows: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if got := len(frame.Fields); got != 12 {
		t.Fatalf("got %d fields, want 12", got)
	}
	if frame.Rows() != 1 {
		t.Fatalf("got %d rows, want 1", frame.Rows())
	}
	// Spot-check representative conversions.
	ts := frame.Fields[0].At(0).(*time.Time)
	if !ts.Equal(time.Date(2026, 1, 1, 10, 20, 30, 0, time.UTC)) {
		t.Fatalf("timestamp = %v", ts)
	}
	if v := *frame.Fields[6].At(0).(*int64); v != 5 {
		t.Fatalf("hugeint = %d, want 5", v)
	}
	if v := *frame.Fields[9].At(0).(*float64); v != 3.25 {
		t.Fatalf("decimal = %v, want 3.25", v)
	}
	if v := *frame.Fields[10].At(0).(*string); v != "hello" {
		t.Fatalf("string = %q", v)
	}
	if v := *frame.Fields[11].At(0).(*bool); !v {
		t.Fatal("bool = false, want true")
	}
}

func TestFrameFromRows_Nulls(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(),
		`SELECT CAST(NULL AS BIGINT) AS a, CAST(NULL AS VARCHAR) AS b, CAST(NULL AS TIMESTAMP) AS c`, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	frame, _, err := FrameFromRows(rows, "A", 10)
	if err != nil {
		t.Fatalf("FrameFromRows: %v", err)
	}
	for i, f := range frame.Fields {
		if f.At(0) != nil {
			// nullable fields hold typed nil pointers; ensure they're nil
			switch v := f.At(0).(type) {
			case *int64:
				if v != nil {
					t.Fatalf("field %d not null", i)
				}
			case *string:
				if v != nil {
					t.Fatalf("field %d not null", i)
				}
			case *time.Time:
				if v != nil {
					t.Fatalf("field %d not null", i)
				}
			}
		}
	}
}

func TestFrameFromRows_HugeintOutOfRange(t *testing.T) {
	_, _, err := frameFor(t, "SELECT 170141183460469231731687303715884105727::HUGEINT AS big")
	if err == nil || !strings.Contains(err.Error(), "big") {
		t.Fatalf("want out-of-range error naming column 'big', got %v", err)
	}
}

func TestFrameFromRows_UnsupportedTypeNamesColumn(t *testing.T) {
	for _, q := range []string{
		"SELECT [1, 2, 3] AS my_list",
		"SELECT {'a': 1} AS my_struct",
		"SELECT map([1],[2]) AS my_map",
	} {
		_, _, err := frameFor(t, q)
		if err == nil || !strings.Contains(err.Error(), "my_") {
			t.Fatalf("query %q: want unsupported-type error naming the column, got %v", q, err)
		}
	}
}

func TestFrameFromRows_Truncation(t *testing.T) {
	e := newLocalEngine(t)
	rows, err := e.Query(context.Background(), "SELECT i FROM range(100) t(i)", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	frame, truncated, err := FrameFromRows(rows, "A", 10)
	if err != nil {
		t.Fatalf("FrameFromRows: %v", err)
	}
	if !truncated {
		t.Fatal("want truncated=true")
	}
	if frame.Rows() != 10 {
		t.Fatalf("got %d rows, want 10", frame.Rows())
	}
}
