package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type Settings struct {
	AccountName      string
	MaxRows          int64
	ConnectionString string
}

func LoadSettings(s backend.DataSourceInstanceSettings) (Settings, error) {
	var jd struct {
		AccountName string `json:"accountName"`
		MaxRows     int64  `json:"maxRows"`
	}
	if len(s.JSONData) > 0 {
		if err := json.Unmarshal(s.JSONData, &jd); err != nil {
			return Settings{}, fmt.Errorf("parse datasource jsonData: %w", err)
		}
	}
	out := Settings{
		AccountName:      jd.AccountName,
		MaxRows:          jd.MaxRows,
		ConnectionString: s.DecryptedSecureJSONData["connectionString"],
	}
	if out.MaxRows <= 0 {
		out.MaxRows = 1_000_000
	}
	return out, nil
}

// redact removes secrets from an error before it can reach logs or the panel.
func redact(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return errors.New(msg)
}

type Datasource struct {
	engine   *Engine
	settings Settings
}

var (
	_ backend.QueryDataHandler      = (*Datasource)(nil)
	_ backend.CheckHealthHandler    = (*Datasource)(nil)
	_ instancemgmt.InstanceDisposer = (*Datasource)(nil)
)

func NewDatasource(ctx context.Context, s backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	settings, err := LoadSettings(s)
	if err != nil {
		return nil, err
	}
	ext, err := FindExtension()
	if err != nil {
		return nil, fmt.Errorf("azure extension unavailable (plugin packaging problem): %w", err)
	}
	engine, err := NewEngine(ctx, ext, settings.ConnectionString)
	if err != nil {
		return nil, redact(err, settings.ConnectionString)
	}
	return &Datasource{engine: engine, settings: settings}, nil
}

func (d *Datasource) Dispose() {
	if d.engine != nil {
		d.engine.Close()
	}
}

type queryModel struct {
	RawSQL string `json:"rawSql"`
	Format string `json:"format"`
}

func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	resp := backend.NewQueryDataResponse()
	for _, q := range req.Queries {
		resp.Responses[q.RefID] = d.query(ctx, q)
	}
	return resp, nil
}

func (d *Datasource) query(ctx context.Context, q backend.DataQuery) backend.DataResponse {
	var qm queryModel
	if err := json.Unmarshal(q.JSON, &qm); err != nil {
		return backend.ErrDataResponse(backend.StatusBadRequest, "invalid query JSON: "+err.Error())
	}
	if strings.TrimSpace(qm.RawSQL) == "" {
		return backend.DataResponse{} // empty query, empty response
	}
	sqlText := ExpandMacros(qm.RawSQL, q.TimeRange.From, q.TimeRange.To)

	rows, err := d.engine.Query(ctx, sqlText, d.settings.MaxRows)
	if err != nil {
		// DuckDB SQL errors pass through verbatim (minus secrets) — they are
		// the most useful debugging signal the user has.
		return backend.DataResponse{Error: redact(err, d.settings.ConnectionString)}
	}
	defer rows.Close()

	frame, truncated, err := FrameFromRows(rows, q.RefID, d.settings.MaxRows)
	if err != nil {
		return backend.DataResponse{Error: redact(err, d.settings.ConnectionString)}
	}
	if truncated {
		if frame.Meta == nil {
			frame.SetMeta(&data.FrameMeta{})
		}
		frame.Meta.Notices = append(frame.Meta.Notices, data.Notice{
			Severity: data.NoticeSeverityWarning,
			Text: fmt.Sprintf("Row limit reached: showing first %d rows. Refine the query or raise maxRows in the datasource settings.",
				d.settings.MaxRows),
		})
	}

	if qm.Format == "timeseries" {
		wide, err := data.LongToWide(frame, nil)
		if err != nil {
			return backend.DataResponse{Error: fmt.Errorf(
				"time series format needs a sorted time column plus value columns (long format): %w", err)}
		}
		if frame.Meta != nil {
			wide.SetMeta(frame.Meta)
		}
		frame = wide
	}
	return backend.DataResponse{Frames: data.Frames{frame}}
}

func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	fail := func(msg string) (*backend.CheckHealthResult, error) {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: msg}, nil
	}
	if d.settings.ConnectionString == "" {
		return fail("No connection string configured. Set it in the datasource settings.")
	}
	// Probe a container that should not exist: a 404-class error proves auth
	// worked; a 403/auth-class error means the credentials were rejected.
	err := d.engine.Exec(ctx,
		"SELECT count(*) FROM glob('az://grafana-health-probe-nonexistent/*')")
	if err == nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusOk,
			Message: "Connected to Azure Blob Storage."}, nil
	}
	msg := redact(err, d.settings.ConnectionString).Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "404") || strings.Contains(lower, "not exist") ||
		strings.Contains(lower, "containernotfound") || strings.Contains(lower, "no files found"):
		return &backend.CheckHealthResult{Status: backend.HealthStatusOk,
			Message: "Connected to Azure Blob Storage (auth accepted)."}, nil
	case strings.Contains(lower, "403") || strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "authorization") || strings.Contains(lower, "signature"):
		return fail("Azure rejected the credentials. Check the connection string. Detail: " + msg)
	default:
		return fail("Could not reach the storage account (network/endpoint problem?). Detail: " + msg)
	}
}
