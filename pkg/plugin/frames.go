package plugin

import (
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// column kinds we can represent as Grafana fields (all nullable)
type colKind int

const (
	kindTime colKind = iota
	kindInt
	kindFloat
	kindString
	kindBool
)

// kindForDBType maps a DuckDB DatabaseTypeName to a field kind, or errors for
// nested/unsupported types. DECIMAL arrives parameterized, e.g. "DECIMAL(18,3)".
func kindForDBType(col, dbType string) (colKind, error) {
	t := strings.ToUpper(dbType)
	switch {
	case strings.HasSuffix(t, "[]"),
		strings.HasPrefix(t, "STRUCT"), strings.HasPrefix(t, "MAP"),
		strings.HasPrefix(t, "LIST"), strings.HasPrefix(t, "UNION"),
		strings.HasPrefix(t, "ARRAY"), t == "BLOB":
		return 0, fmt.Errorf("column %q has unsupported DuckDB type %s; cast it to a scalar type in your SQL", col, dbType)
	case strings.HasPrefix(t, "TIMESTAMP"), t == "DATETIME", t == "DATE":
		return kindTime, nil
	case t == "TINYINT", t == "SMALLINT", t == "INTEGER", t == "INT", t == "BIGINT",
		t == "HUGEINT", t == "UTINYINT", t == "USMALLINT", t == "UINTEGER":
		return kindInt, nil
	case t == "FLOAT", t == "REAL", t == "DOUBLE", strings.HasPrefix(t, "DECIMAL"):
		return kindFloat, nil
	case t == "VARCHAR", t == "UUID", strings.HasPrefix(t, "ENUM"):
		return kindString, nil
	case t == "BOOLEAN":
		return kindBool, nil
	default:
		return 0, fmt.Errorf("column %q has unsupported DuckDB type %s", col, dbType)
	}
}

func newField(name string, k colKind) *data.Field {
	switch k {
	case kindTime:
		return data.NewField(name, nil, []*time.Time{})
	case kindInt:
		return data.NewField(name, nil, []*int64{})
	case kindFloat:
		return data.NewField(name, nil, []*float64{})
	case kindBool:
		return data.NewField(name, nil, []*bool{})
	default:
		return data.NewField(name, nil, []*string{})
	}
}

// convertValue turns a scanned driver value into the pointer type of its field.
func convertValue(col string, k colKind, v any) (any, error) {
	if v == nil {
		switch k {
		case kindTime:
			return (*time.Time)(nil), nil
		case kindInt:
			return (*int64)(nil), nil
		case kindFloat:
			return (*float64)(nil), nil
		case kindBool:
			return (*bool)(nil), nil
		default:
			return (*string)(nil), nil
		}
	}
	switch k {
	case kindTime:
		if t, ok := v.(time.Time); ok {
			u := t.UTC()
			return &u, nil
		}
	case kindInt:
		switch n := v.(type) {
		case int8:
			i := int64(n)
			return &i, nil
		case int16:
			i := int64(n)
			return &i, nil
		case int32:
			i := int64(n)
			return &i, nil
		case int64:
			return &n, nil
		case uint8:
			i := int64(n)
			return &i, nil
		case uint16:
			i := int64(n)
			return &i, nil
		case uint32:
			i := int64(n)
			return &i, nil
		case *big.Int: // HUGEINT
			if !n.IsInt64() {
				return nil, fmt.Errorf("column %q: HUGEINT value %s outside int64 range", col, n.String())
			}
			i := n.Int64()
			return &i, nil
		}
	case kindFloat:
		switch f := v.(type) {
		case float32:
			g := float64(f)
			return &g, nil
		case float64:
			return &f, nil
		case duckdb.Decimal:
			g := f.Float64()
			return &g, nil
		}
	case kindString:
		if s, ok := v.(string); ok {
			return &s, nil
		}
		if b, ok := v.([]byte); ok {
			s := string(b)
			return &s, nil
		}
	case kindBool:
		if b, ok := v.(bool); ok {
			return &b, nil
		}
	}
	return nil, fmt.Errorf("column %q: unexpected scan value type %T", col, v)
}

// FrameFromRows converts DuckDB rows into a single Grafana frame, appending at
// most maxRows rows. The returned bool is true when at least one extra row
// existed (the engine queries with LIMIT maxRows+1 so this is detectable).
func FrameFromRows(rows *sql.Rows, refID string, maxRows int64) (*data.Frame, bool, error) {
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, false, fmt.Errorf("read column types: %w", err)
	}
	kinds := make([]colKind, len(colTypes))
	fields := make([]*data.Field, len(colTypes))
	for i, ct := range colTypes {
		k, err := kindForDBType(ct.Name(), ct.DatabaseTypeName())
		if err != nil {
			return nil, false, err
		}
		kinds[i] = k
		fields[i] = newField(ct.Name(), k)
	}
	frame := data.NewFrame(refID, fields...)
	frame.RefID = refID

	scan := make([]any, len(colTypes))
	scanPtrs := make([]any, len(colTypes))
	for i := range scan {
		scanPtrs[i] = &scan[i]
	}
	var count int64
	truncated := false
	for rows.Next() {
		if count == maxRows {
			truncated = true
			break
		}
		if err := rows.Scan(scanPtrs...); err != nil {
			return nil, false, fmt.Errorf("scan row %d: %w", count, err)
		}
		for i, v := range scan {
			cv, err := convertValue(colTypes[i].Name(), kinds[i], v)
			if err != nil {
				return nil, false, err
			}
			frame.Fields[i].Append(cv)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return frame, truncated, nil
}
