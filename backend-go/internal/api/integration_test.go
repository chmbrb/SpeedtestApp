package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/config"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests run against a real PostgreSQL that already has migrations 001-005 applied.
// They are skipped unless TEST_DATABASE_URL is set. They TRUNCATE speedtests, so they only run against a
// database explicitly marked disposable (see TestMain).
var (
	itPool *pgxpool.Pool
	itSrv  *Server
)

func TestMain(m *testing.M) {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		ctx := context.Background()
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			fmt.Fprintln(os.Stderr, "TEST_DATABASE_URL:", err)
			os.Exit(1)
		}
		// These tests TRUNCATE speedtests. A name pattern is not a safe guard ("speedtestdb" contains "test"),
		// so the database must have been explicitly marked as disposable with:
		//   COMMENT ON DATABASE <name> IS 'speedtest-integration-test';
		var marker *string
		if err := pool.QueryRow(ctx, `SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = current_database()`).Scan(&marker); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if marker == nil || *marker != "speedtest-integration-test" {
			fmt.Fprintln(os.Stderr, "refusing to run integration tests: this database is not marked as disposable.\n"+
				"To use a scratch database: COMMENT ON DATABASE <name> IS 'speedtest-integration-test';")
			os.Exit(1)
		}
		if err := testdb.ProcessLock(ctx); err != nil { // other packages' tests share this database
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		itPool = pool
		itSrv = NewServer(store.New(pool), NewAuthenticator(config.ParseTokens("laptop=tok-laptop,phone=tok-phone")),
			"America/Phoenix", slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	code := m.Run()
	if itPool != nil {
		itPool.Close()
	}
	os.Exit(code)
}

func needDB(t *testing.T) {
	t.Helper()
	if itPool == nil {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if _, err := itPool.Exec(context.Background(), `TRUNCATE speedtests CASCADE`); err != nil {
		t.Fatal(err)
	}
}

// seed inserts one Ookla result through the real insert function. Mbps values are exact multiples of 125000 B/s.
func seed(t *testing.T, ts string, device, ct, trigger string, dlMbps, ulMbps float64, loss *float64, latency float64) {
	t.Helper()
	lossJSON := ""
	if loss != nil {
		lossJSON = fmt.Sprintf(`,"packetLoss":%v`, *loss)
	}
	raw := fmt.Sprintf(`{"type":"result","timestamp":%q,"ping":{"jitter":1.5,"latency":%v},
		"download":{"bandwidth":%d},"upload":{"bandwidth":%d},"isp":"TestNet",
		"server":{"id":1,"name":"Srv","location":"Phoenix, AZ","country":"United States"},
		"result":{"id":"%s","url":"https://example.test/r"}%s}`,
		ts, latency, int64(dlMbps*125000), int64(ulMbps*125000), "1ee33b76-fb40-407c-babc-fbb6fcf7758d", lossJSON)
	if _, err := itSrv.store.InsertOokla(context.Background(), []byte(raw), device, ct, trigger); err != nil {
		t.Fatal(err)
	}
}

func f(v float64) *float64 { return &v }

func getJSON(t *testing.T, url string, v any) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, itSrv, "GET", url, "", "")
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", url, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("GET %s: %v\n%s", url, err, rec.Body)
	}
	return rec
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

type listResp struct {
	Items []struct {
		ID, Provider, Trigger, Device, ConnectionType string
		TestedAt                                      time.Time
		DownloadMbps, UploadMbps                      float64
		LatencyMs, PacketLossPct                      *float64
		Server                                        struct{ Name, Location *string }
	}
	NextCursor *string
}

func TestIntegrationPaginationAndFilters(t *testing.T) {
	needDB(t)
	// 7 rows; two share a timestamp to exercise the (tested_at, id) tiebreak
	seed(t, "2026-06-01T10:00:00Z", "server", "wired", "scheduled", 100, 10, f(0), 10)
	seed(t, "2026-06-01T11:00:00Z", "server", "wired", "scheduled", 200, 20, nil, 11)
	seed(t, "2026-06-01T12:00:00Z", "server", "wired", "scheduled", 300, 30, f(1.5), 12)
	seed(t, "2026-06-01T12:00:00Z", "laptop", "wireless", "scheduled", 50, 5, nil, 40)
	seed(t, "2026-06-02T09:00:00Z", "laptop", "wireless", "on_demand", 60, 6, nil, 41)
	seed(t, "2026-06-02T10:00:00Z", "server", "wired", "on_demand", 400, 40, nil, 13)
	seed(t, "2026-06-03T10:00:00Z", "server", "wired", "scheduled", 500, 50, nil, 14)

	var all listResp
	getJSON(t, "/api/v1/results?limit=500", &all)
	if len(all.Items) != 7 || all.NextCursor != nil {
		t.Fatalf("got %d items, cursor %v", len(all.Items), all.NextCursor)
	}
	for i := 1; i < len(all.Items); i++ {
		if all.Items[i].TestedAt.After(all.Items[i-1].TestedAt) {
			t.Fatalf("not newest first at %d", i)
		}
	}
	first := all.Items[0]
	if first.Provider != "ookla" || !near(first.DownloadMbps, 500) || first.Server.Name == nil || *first.Server.Name != "Srv" {
		t.Errorf("first item: %+v", first)
	}

	// walk pages of 3 and compare with the unpaged list: no gaps, no duplicates, same order
	var walked []string
	cursor, pages := "", 0
	for {
		url := "/api/v1/results?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		var page listResp
		getJSON(t, url, &page)
		pages++
		for _, it := range page.Items {
			walked = append(walked, it.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if pages > 5 {
			t.Fatal("pagination does not terminate")
		}
	}
	if pages != 3 || len(walked) != 7 {
		t.Fatalf("pages=%d walked=%d", pages, len(walked))
	}
	for i, id := range walked {
		if all.Items[i].ID != id {
			t.Errorf("page walk differs at %d", i)
		}
	}

	count := func(q string) int {
		var r listResp
		getJSON(t, "/api/v1/results?limit=500&"+q, &r)
		return len(r.Items)
	}
	for q, want := range map[string]int{
		"device=laptop":                                          2,
		"device=laptop,server":                                   7,
		"connectionType=wireless":                                2,
		"connectionType=wired&trigger=on_demand":                 1,
		"trigger=on_demand":                                      2,
		"provider=ndt7":                                          0,
		"provider=ookla":                                         7,
		"from=2026-06-02T00:00:00Z":                              3,
		"to=2026-06-02T00:00:00Z":                                4,
		"from=2026-06-01T12:00:00Z&to=2026-06-01T12:00:01Z":      2, // from inclusive, to exclusive
		"from=2026-06-01T12:00:00Z&to=2026-06-01T12:00:00Z":      0,
		"device=nobody":                                          0,
		"from=2026-06-01T05:00:00-07:00&to=2026-06-01T13:00:00Z": 2, // 05:00-07:00 == 12:00Z; offsets are honoured
	} {
		if got := count(q); got != want {
			t.Errorf("?%s -> %d rows, want %d", q, got, want)
		}
	}
}

func TestIntegrationSummary(t *testing.T) {
	needDB(t)
	seed(t, "2026-06-01T10:00:00Z", "server", "wired", "scheduled", 100, 10, f(0), 10)
	seed(t, "2026-06-01T11:00:00Z", "server", "wired", "scheduled", 200, 20, f(2), 20)
	seed(t, "2026-06-01T12:00:00Z", "server", "wired", "scheduled", 300, 30, nil, 30)

	var sum struct {
		Count                    int
		First, Last              *time.Time
		DownloadMbps, UploadMbps map[string]*float64
		LatencyMs, PacketLossPct map[string]*float64
	}
	getJSON(t, "/api/v1/stats/summary", &sum)
	if sum.Count != 3 || sum.First.UTC().Hour() != 10 || sum.Last.UTC().Hour() != 12 {
		t.Fatalf("count/first/last: %+v", sum)
	}
	for k, want := range map[string]float64{"avg": 200, "min": 100, "max": 300, "p50": 200, "p95": 290} {
		if sum.DownloadMbps[k] == nil || !near(*sum.DownloadMbps[k], want) {
			t.Errorf("download %s = %v, want %v", k, sum.DownloadMbps[k], want)
		}
	}
	if !near(*sum.LatencyMs["avg"], 20) || !near(*sum.UploadMbps["max"], 30) {
		t.Errorf("latency/upload: %v %v", sum.LatencyMs, sum.UploadMbps)
	}
	// packet loss ignores the row with no value: avg of 0 and 2
	if !near(*sum.PacketLossPct["avg"], 1) || !near(*sum.PacketLossPct["max"], 2) {
		t.Errorf("packet loss should skip nulls: %v", sum.PacketLossPct)
	}

	// no matching rows: count 0 and every stat null (not an error)
	var empty struct {
		Count        int
		First        *time.Time
		DownloadMbps map[string]*float64
	}
	getJSON(t, "/api/v1/stats/summary?device=nobody", &empty)
	if empty.Count != 0 || empty.First != nil || empty.DownloadMbps["avg"] != nil || len(empty.DownloadMbps) != 5 {
		t.Errorf("empty summary: %+v", empty)
	}
}

type tsResp struct {
	Interval, Timezone string
	Series             []struct {
		Key    map[string]string
		Points []struct {
			Bucket       time.Time
			Count        int
			DownloadMbps map[string]*float64
		}
	}
}

func TestIntegrationTimeseries(t *testing.T) {
	needDB(t)
	// Phoenix is UTC-7 all year. 06:00Z on Jun 1 is 23:00 on May 31 locally; 08:00Z is 01:00 Jun 1.
	seed(t, "2026-06-01T06:00:00Z", "server", "wired", "scheduled", 100, 10, nil, 10)
	seed(t, "2026-06-01T08:00:00Z", "server", "wired", "scheduled", 300, 30, nil, 10)
	seed(t, "2026-06-01T20:00:00Z", "server", "wired", "scheduled", 500, 50, nil, 10)
	seed(t, "2026-06-01T08:00:00Z", "laptop", "wireless", "on_demand", 40, 4, nil, 10)

	var day tsResp
	getJSON(t, "/api/v1/stats/timeseries?interval=day&timezone=America/Phoenix&device=server", &day)
	if day.Interval != "day" || day.Timezone != "America/Phoenix" || len(day.Series) != 1 {
		t.Fatalf("%+v", day)
	}
	pts := day.Series[0].Points
	if len(pts) != 2 {
		t.Fatalf("want 2 daily buckets, got %+v", pts)
	}
	// local midnight May 31 = 07:00Z, local midnight Jun 1 = 07:00Z the next day
	if got := pts[0].Bucket.UTC().Format(time.RFC3339); got != "2026-05-31T07:00:00Z" || pts[0].Count != 1 {
		t.Errorf("bucket 0 = %s count %d", got, pts[0].Count)
	}
	if got := pts[1].Bucket.UTC().Format(time.RFC3339); got != "2026-06-01T07:00:00Z" || pts[1].Count != 2 || !near(*pts[1].DownloadMbps["avg"], 400) {
		t.Errorf("bucket 1 = %s count %d avg %v", got, pts[1].Count, pts[1].DownloadMbps["avg"])
	}

	// same data in UTC: all three fall on Jun 1
	var utc tsResp
	getJSON(t, "/api/v1/stats/timeseries?interval=day&timezone=UTC&device=server", &utc)
	if len(utc.Series[0].Points) != 1 || utc.Series[0].Points[0].Count != 3 {
		t.Errorf("UTC day buckets: %+v", utc.Series[0].Points)
	}
	// default timezone comes from server config (America/Phoenix in this test server)
	var def tsResp
	getJSON(t, "/api/v1/stats/timeseries?device=server", &def)
	if def.Timezone != "America/Phoenix" || def.Interval != "day" {
		t.Errorf("defaults: %s %s", def.Timezone, def.Interval)
	}

	// default grouping is provider+device+connectionType -> two series, keys carry only grouped fields
	var grouped tsResp
	getJSON(t, "/api/v1/stats/timeseries?interval=day", &grouped)
	if len(grouped.Series) != 2 || grouped.Series[0].Key["device"] != "laptop" || grouped.Series[1].Key["device"] != "server" ||
		grouped.Series[0].Key["provider"] != "ookla" || grouped.Series[0].Key["connectionType"] != "wireless" {
		t.Errorf("grouped: %+v", grouped.Series)
	}
	var byDevice tsResp
	getJSON(t, "/api/v1/stats/timeseries?interval=day&groupBy=device&timezone=UTC", &byDevice)
	if len(byDevice.Series) != 2 || len(byDevice.Series[0].Key) != 1 {
		t.Errorf("groupBy=device keys: %+v", byDevice.Series)
	}
	var one tsResp
	getJSON(t, "/api/v1/stats/timeseries?interval=day&groupBy=provider&timezone=UTC", &one)
	if len(one.Series) != 1 || one.Series[0].Points[0].Count != 4 {
		t.Errorf("groupBy=provider should merge devices: %+v", one.Series)
	}

	// raw: one point per result
	var raw tsResp
	getJSON(t, "/api/v1/stats/timeseries?interval=raw&groupBy=device&device=server", &raw)
	if len(raw.Series) != 1 || len(raw.Series[0].Points) != 3 {
		t.Fatalf("raw: %+v", raw)
	}
	for _, p := range raw.Series[0].Points {
		if p.Count != 1 || *p.DownloadMbps["avg"] != *p.DownloadMbps["max"] {
			t.Errorf("raw point: %+v", p)
		}
	}

	// hour/week/month/quarter/year all execute
	for _, iv := range []string{"hour", "week", "month", "quarter", "year"} {
		var r tsResp
		getJSON(t, "/api/v1/stats/timeseries?interval="+iv, &r)
		if len(r.Series) == 0 {
			t.Errorf("interval %s returned no series", iv)
		}
	}
	// no data -> empty series list, not null
	rec := do(t, itSrv, "GET", "/api/v1/stats/timeseries?device=nobody", "", "")
	if !strings.Contains(rec.Body.String(), `"series":[]`) {
		t.Errorf("empty series must be []: %s", rec.Body)
	}
}

func TestIntegrationTimeseriesTooManyPoints(t *testing.T) {
	needDB(t)
	_, err := itPool.Exec(context.Background(), `
		INSERT INTO speedtests (provider, device, connection_type, tested_at, download_mbps, upload_mbps, raw_data)
		SELECT 'ookla', 'bulk', 'wired', timestamptz '2026-01-01' + g * interval '1 second', 1, 1, '{}'
		FROM generate_series(1, 20001) g`)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, itSrv, "GET", "/api/v1/stats/timeseries?interval=raw&device=bulk", "", "")
	if rec.Code != 400 {
		t.Fatalf("status %d", rec.Code)
	}
	problem(t, rec)
	var ok tsResp
	getJSON(t, "/api/v1/stats/timeseries?interval=day&device=bulk&timezone=UTC", &ok)
	if ok.Series[0].Points[0].Count == 0 {
		t.Error("coarser interval over the same data must work")
	}
}

func TestIntegrationDevicesAndGet(t *testing.T) {
	needDB(t)
	seed(t, "2026-06-01T10:00:00Z", "server", "wired", "scheduled", 100, 10, nil, 10)
	seed(t, "2026-06-02T10:00:00Z", "laptop", "wireless", "on_demand", 50, 5, nil, 40)
	seed(t, "2026-06-03T10:00:00Z", "laptop", "wired", "on_demand", 90, 9, nil, 40)

	var devs struct {
		Items []struct {
			Name                       string
			ConnectionTypes, Providers []string
			TestCount                  int
			LastTestAt                 time.Time
		}
	}
	getJSON(t, "/api/v1/devices", &devs)
	if len(devs.Items) != 2 || devs.Items[0].Name != "laptop" || devs.Items[0].TestCount != 2 ||
		strings.Join(devs.Items[0].ConnectionTypes, ",") != "wired,wireless" || devs.Items[1].Name != "server" ||
		devs.Items[0].LastTestAt.UTC().Day() != 3 {
		t.Errorf("devices: %+v", devs.Items)
	}

	var list listResp
	getJSON(t, "/api/v1/results?limit=1", &list)
	var one struct {
		ID     string
		Raw    map[string]any
		Server map[string]*string
	}
	getJSON(t, "/api/v1/results/"+list.Items[0].ID, &one)
	if one.ID != list.Items[0].ID || one.Raw["type"] != "result" || *one.Server["country"] != "United States" {
		t.Errorf("detail: %+v", one)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "not-a-uuid", "1' OR '1'='1"} {
		rec := do(t, itSrv, "GET", "/api/v1/results/"+strings.ReplaceAll(id, " ", "%20"), "", "")
		if rec.Code != 404 {
			t.Errorf("id %q -> %d, want 404", id, rec.Code)
		}
		problem(t, rec)
	}
}

func TestIntegrationIngestEndToEnd(t *testing.T) {
	needDB(t)
	post := func(token, body string) *httptest.ResponseRecorder {
		return do(t, itSrv, "POST", "/api/v1/results", token, body)
	}
	rec := post("tok-phone", envelope("ookla", "wireless", goodOokla))
	if rec.Code != 201 {
		t.Fatalf("ookla: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		ID, Provider, Trigger, Device, ConnectionType string
		DownloadMbps                                  float64
		PacketLossPct                                 *float64
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Device != "phone" || created.ConnectionType != "wireless" || created.Provider != "ookla" ||
		created.Trigger != "scheduled" || !near(created.DownloadMbps, 928.221008) || created.PacketLossPct == nil {
		t.Errorf("created ookla: %+v", created)
	}

	rec = post("tok-laptop", `{"provider":"ndt7","connectionType":"wireless","trigger":"on_demand","result":`+goodNDT7+`}`)
	if rec.Code != 201 {
		t.Fatalf("ndt7: %d %s", rec.Code, rec.Body)
	}
	var nd struct {
		Provider, Trigger, Device string
		DownloadMbps, UploadMbps  float64
		LatencyMs, PacketLossPct  *float64
		JitterMs                  *float64
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &nd)
	if nd.Provider != "ndt7" || nd.Trigger != "on_demand" || nd.Device != "laptop" || !near(nd.DownloadMbps, 87.5) ||
		!near(*nd.LatencyMs, 18.4) || !near(*nd.PacketLossPct, 0.31) || nd.JitterMs != nil {
		t.Errorf("created ndt7: %+v", nd)
	}

	// both providers are visible and filterable afterwards; summaries never need the caller to mix them
	var n listResp
	getJSON(t, "/api/v1/results?provider=ndt7", &n)
	if len(n.Items) != 1 {
		t.Errorf("ndt7 rows: %d", len(n.Items))
	}
	var det struct{ Raw map[string]any }
	getJSON(t, "/api/v1/results/"+created.ID, &det)
	if det.Raw["isp"] != nil || det.Raw["type"] != "result" {
		t.Errorf("raw payload not preserved: %v", det.Raw)
	}

	var cnt int
	_ = itPool.QueryRow(context.Background(), `select count(*) from speedtests`).Scan(&cnt)
	if cnt != 2 {
		t.Errorf("rows = %d, want 2", cnt)
	}
	cnt = 3 // the later ingests in this test add rows; counts are re-read below

	// JSON numbers written as floats ("1.0") are still whole bandwidths; the fuzzer found these were rejected
	rec = post("tok-laptop", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z",
		"download":{"bandwidth":116027626.0,"bytes":1.0e9,"elapsed":9116.0},"upload":{"bandwidth":0.0},"server":{"id":64582.0,"port":8080.0}}`))
	if rec.Code != 201 {
		t.Errorf("float-formatted integers: %d %s", rec.Code, rec.Body)
	}
	// extreme values never reach the database, so aggregates keep working afterwards
	if rec := post("tok-laptop", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1e300},"upload":{"bandwidth":1}}`)); rec.Code != 400 {
		t.Errorf("absurd bandwidth: %d", rec.Code)
	}
	var sum struct{ Count int }
	getJSON(t, "/api/v1/stats/summary", &sum)
	getJSON(t, "/api/v1/stats/timeseries", &tsResp{})
	_ = itPool.QueryRow(context.Background(), `select count(*) from speedtests`).Scan(&cnt)
	if sum.Count != cnt {
		t.Errorf("summary count %d != rows %d", sum.Count, cnt)
	}
	// the database itself also refuses absurd values (defence in depth if a writer bypasses the API)
	if _, err := itPool.Exec(context.Background(), `UPDATE speedtests SET download_mbps = 1e300`); err == nil {
		t.Error("check constraint should reject 1e300 Mbps")
	}

	// an empty result URL is reported as null, never ""
	rec = post("tok-laptop", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"url":""}}`))
	var blank struct{ ResultURL *string }
	_ = json.Unmarshal(rec.Body.Bytes(), &blank)
	if rec.Code != 201 || blank.ResultURL != nil {
		t.Errorf("empty url: %d %s", rec.Code, rec.Body)
	}
	cnt++

	// SQL-injection-looking device filter is just a value
	var inj listResp
	getJSON(t, "/api/v1/results?device='%3B%20DROP%20TABLE%20speedtests%3B--", &inj)
	_ = itPool.QueryRow(context.Background(), `select count(*) from speedtests`).Scan(&cnt)
	var after int
	_ = itPool.QueryRow(context.Background(), `select count(*) from speedtests`).Scan(&after)
	if len(inj.Items) != 0 || after != cnt {
		t.Errorf("injection check: items=%d rows=%d", len(inj.Items), cnt)
	}
}

func TestIntegrationHealth(t *testing.T) {
	needDB(t)
	rec := do(t, itSrv, "GET", "/api/v1/health", "", "")
	if rec.Code != http.StatusOK {
		t.Errorf("health: %d %s", rec.Code, rec.Body)
	}
}
