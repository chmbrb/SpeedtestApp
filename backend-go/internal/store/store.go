// Package store is the PostgreSQL access layer. Aggregation happens in SQL.
package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrTooManyPoints = errors.New("too many points")
)

// MaxPoints caps a timeseries response so a wide range with a fine interval cannot exhaust memory.
const MaxPoints = 20000

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Filter selects results. Empty slices and nil times mean "no restriction".
type Filter struct {
	From, To        *time.Time // From inclusive, To exclusive
	Providers       []string
	Devices         []string
	ConnectionTypes []string
	Triggers        []string
}

// Cursor is the keyset position after the last row of a page (results are newest first).
type Cursor struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

const whereFilter = `
	($1::timestamptz IS NULL OR tested_at >= $1)
	AND ($2::timestamptz IS NULL OR tested_at < $2)
	AND ($3::text[] IS NULL OR provider = ANY($3))
	AND ($4::text[] IS NULL OR device = ANY($4))
	AND ($5::text[] IS NULL OR connection_type = ANY($5))
	AND ($6::text[] IS NULL OR "trigger" = ANY($6))`

func (f Filter) args() []any {
	return []any{f.From, f.To, nilIfEmpty(f.Providers), nilIfEmpty(f.Devices),
		nilIfEmpty(f.ConnectionTypes), nilIfEmpty(f.Triggers)}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

const resultCols = `id::text, provider, "trigger", device, connection_type, tested_at,
	download_mbps, upload_mbps, latency_ms, jitter_ms, packet_loss_pct,
	isp, server_name, server_location, server_country, NULLIF(result_url, '')`

func scanResult(row pgx.Row, extra ...any) (model.Result, error) {
	var r model.Result
	dest := []any{&r.ID, &r.Provider, &r.Trigger, &r.Device, &r.ConnectionType, &r.TestedAt,
		&r.DownloadMbps, &r.UploadMbps, &r.LatencyMs, &r.JitterMs, &r.PacketLossPct,
		&r.ISP, &r.Server.Name, &r.Server.Location, &r.Server.Country, &r.ResultURL}
	err := row.Scan(append(dest, extra...)...)
	r.TestedAt = r.TestedAt.UTC()
	return r, err
}

// ListResults returns up to limit results newest first, plus the cursor for the next page (nil at the end).
func (s *Store) ListResults(ctx context.Context, f Filter, limit int, after *Cursor) ([]model.Result, *Cursor, error) {
	args := append(f.args(), nil, nil)
	if after != nil {
		args[6], args[7] = after.T, after.ID
	}
	args = append(args, limit+1)
	rows, err := s.pool.Query(ctx, `SELECT `+resultCols+` FROM speedtests WHERE`+whereFilter+`
		AND ($7::timestamptz IS NULL OR (tested_at, id) < ($7, $8::uuid))
		ORDER BY tested_at DESC, id DESC LIMIT $9`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := []model.Result{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(items) <= limit {
		return items, nil, nil
	}
	items = items[:limit]
	last := items[len(items)-1]
	return items, &Cursor{T: last.TestedAt, ID: last.ID}, nil
}

func (s *Store) GetResult(ctx context.Context, id string) (model.ResultDetail, error) {
	if !uuidRe.MatchString(id) {
		return model.ResultDetail{}, ErrNotFound
	}
	var raw []byte
	r, err := scanResult(s.pool.QueryRow(ctx, `SELECT `+resultCols+`, raw_data FROM speedtests WHERE id = $1::uuid`, id), &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ResultDetail{}, ErrNotFound
	}
	return model.ResultDetail{Result: r, Raw: raw}, err
}

func statCols(col string) string {
	return fmt.Sprintf(`avg(%[1]s), min(%[1]s), max(%[1]s),
		percentile_cont(0.5) WITHIN GROUP (ORDER BY %[1]s), percentile_cont(0.95) WITHIN GROUP (ORDER BY %[1]s)`, col)
}

var metricsSQL = strings.Join([]string{
	statCols("download_mbps"), statCols("upload_mbps"), statCols("latency_ms"), statCols("packet_loss_pct"),
}, ",\n")

func statDest(s *model.Stat) []any { return []any{&s.Avg, &s.Min, &s.Max, &s.P50, &s.P95} }

func metricsDest(m *model.Metrics) []any {
	var d []any
	for _, s := range []*model.Stat{&m.DownloadMbps, &m.UploadMbps, &m.LatencyMs, &m.PacketLossPct} {
		d = append(d, statDest(s)...)
	}
	return d
}

func (s *Store) Summary(ctx context.Context, f Filter) (model.Summary, error) {
	var out model.Summary
	dest := append([]any{&out.Count, &out.First, &out.Last}, metricsDest(&out.Metrics)...)
	err := s.pool.QueryRow(ctx, `SELECT count(*), min(tested_at), max(tested_at), `+metricsSQL+
		` FROM speedtests WHERE`+whereFilter, f.args()...).Scan(dest...)
	if out.First != nil {
		t := out.First.UTC()
		out.First = &t
	}
	if out.Last != nil {
		t := out.Last.UTC()
		out.Last = &t
	}
	return out, err
}

var intervalUnit = map[string]string{
	"hour": "hour", "day": "day", "week": "week", "month": "month", "quarter": "quarter", "year": "year",
}

var groupCols = map[string]string{"provider": "provider", "device": "device", "connectionType": "connection_type"}

// Timeseries buckets results in the given IANA time zone (weeks start on Monday). interval "raw"
// returns every result as its own point. Series are ordered by key, points by time.
func (s *Store) Timeseries(ctx context.Context, f Filter, interval string, groupBy []string, tz string) (model.Timeseries, error) {
	bucket, extraGroup := "tested_at", ", id"
	args := append(f.args(), MaxPoints+1) // $7 = LIMIT; $8 = time zone, only referenced when bucketing
	if interval != "raw" {
		unit, ok := intervalUnit[interval]
		if !ok {
			return model.Timeseries{}, fmt.Errorf("unknown interval %q", interval)
		}
		bucket = fmt.Sprintf(`(date_trunc('%s', tested_at AT TIME ZONE $8) AT TIME ZONE $8)`, unit)
		extraGroup = ""
		args = append(args, tz)
	}
	sel := map[string]string{"provider": "NULL::text", "device": "NULL::text", "connectionType": "NULL::text"}
	for _, g := range groupBy {
		col, ok := groupCols[g]
		if !ok {
			return model.Timeseries{}, fmt.Errorf("unknown groupBy %q", g)
		}
		sel[g] = col + "::text"
	}
	query := fmt.Sprintf(`SELECT %s, %s, %s, %s AS bucket, count(*), %s
		FROM speedtests WHERE%s
		GROUP BY 1, 2, 3, 4%s ORDER BY 1, 2, 3, 4 LIMIT $7`,
		sel["provider"], sel["device"], sel["connectionType"], bucket, metricsSQL, whereFilter, extraGroup)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return model.Timeseries{}, err
	}
	defer rows.Close()

	out := model.Timeseries{Interval: interval, Timezone: tz, Series: []model.Series{}}
	total := 0
	var cur *model.Series
	for rows.Next() {
		var key model.SeriesKey
		var p model.TimeseriesPoint
		dest := append([]any{&key.Provider, &key.Device, &key.ConnectionType, &p.Bucket, &p.Count}, metricsDest(&p.Metrics)...)
		if err := rows.Scan(dest...); err != nil {
			return model.Timeseries{}, err
		}
		if total++; total > MaxPoints {
			return model.Timeseries{}, ErrTooManyPoints
		}
		p.Bucket = p.Bucket.UTC()
		if cur == nil || !sameKey(cur.Key, key) {
			out.Series = append(out.Series, model.Series{Key: key, Points: []model.TimeseriesPoint{}})
			cur = &out.Series[len(out.Series)-1]
		}
		cur.Points = append(cur.Points, p)
	}
	return out, rows.Err()
}

func (s *Store) Devices(ctx context.Context) ([]model.Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT device,
		       array_agg(DISTINCT connection_type ORDER BY connection_type),
		       array_agg(DISTINCT provider ORDER BY provider),
		       count(*), max(tested_at)
		FROM speedtests GROUP BY device ORDER BY max(tested_at) DESC, device`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Device{}
	for rows.Next() {
		var d model.Device
		if err := rows.Scan(&d.Name, &d.ConnectionTypes, &d.Providers, &d.TestCount, &d.LastTestAt); err != nil {
			return nil, err
		}
		d.LastTestAt = d.LastTestAt.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// InsertOokla stores an Ookla CLI JSON result and returns the new id.
func (s *Store) InsertOokla(ctx context.Context, raw []byte, device, connectionType, trigger string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT speedtests_insert($1::jsonb, $2, $3, $4)::text`,
		string(raw), device, connectionType, trigger).Scan(&id)
	return id, err
}

// InsertNDT7 stores an NDT7 summary (docs/openapi.yaml: Ndt7Summary). clientIP may be empty.
func (s *Store) InsertNDT7(ctx context.Context, raw []byte, device, connectionType, trigger, clientIP string) (string, error) {
	var ip *string
	if clientIP != "" {
		ip = &clientIP
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT speedtests_insert_ndt7($1::jsonb, $2, $3, $4, $5::inet)::text`,
		string(raw), device, connectionType, trigger, ip).Scan(&id)
	return id, err
}

func sameKey(a, b model.SeriesKey) bool {
	eq := func(x, y *string) bool {
		if x == nil || y == nil {
			return x == y
		}
		return *x == *y
	}
	return eq(a.Provider, b.Provider) && eq(a.Device, b.Device) && eq(a.ConnectionType, b.ConnectionType)
}
