package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Sibuxiangx/nicedroid/internal/config"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open initializes the database and applies migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS upstream_keys (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	name          TEXT NOT NULL DEFAULT '',
	key           TEXT NOT NULL UNIQUE,
	enabled       INTEGER NOT NULL DEFAULT 1,
	weight        INTEGER NOT NULL DEFAULT 1,
	last_status   TEXT NOT NULL DEFAULT '',
	last_checked  TIMESTAMP,
	created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS client_keys (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL DEFAULT '',
	key         TEXT NOT NULL UNIQUE,
	enabled     INTEGER NOT NULL DEFAULT 1,
	rpm_limit   INTEGER NOT NULL DEFAULT 0,
	daily_tokens INTEGER NOT NULL DEFAULT 0,
	models      TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS request_logs (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	ts             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	client_key     TEXT NOT NULL DEFAULT '',
	requested_model TEXT NOT NULL DEFAULT '',
	used_model     TEXT NOT NULL DEFAULT '',
	provider       TEXT NOT NULL DEFAULT '',
	pool           TEXT NOT NULL DEFAULT '',
	status         INTEGER NOT NULL DEFAULT 0,
	latency_ms     INTEGER NOT NULL DEFAULT 0,
	ttft_ms        INTEGER NOT NULL DEFAULT 0,
	prompt_tokens  INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
	cache_write_tokens INTEGER NOT NULL DEFAULT 0,
	credits        INTEGER NOT NULL DEFAULT 0,
	tps            REAL NOT NULL DEFAULT 0,
	stream         INTEGER NOT NULL DEFAULT 0,
	error          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_request_logs_ts ON request_logs(ts);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Lightweight migrations for databases created by earlier versions.
	cols := []struct{ name, def string }{
		{"ttft_ms", "INTEGER NOT NULL DEFAULT 0"},
		{"cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"tps", "REAL NOT NULL DEFAULT 0"},
		{"credits", "INTEGER NOT NULL DEFAULT 0"},
		{"provider", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, col := range cols {
		if !s.hasColumn("request_logs", col.name) {
			if _, err := s.db.Exec("ALTER TABLE request_logs ADD COLUMN " + col.name + " " + col.def); err != nil {
				return err
			}
		}
	}
	// One-time repair of TPS values written before the generation-window fix.
	// Non-streaming requests used to subtract a TTFT that equals the full
	// latency, leaving a 1-5ms "generation window" and absurd TPS values.
	if v, _ := s.GetSetting("tps_repair_v1", ""); v != "1" {
		if _, err := s.db.Exec(`UPDATE request_logs SET tps = CASE
			WHEN completion_tokens <= 0 OR latency_ms <= 0 THEN 0
			WHEN stream = 1 AND ttft_ms > 0 AND (latency_ms - ttft_ms) >= 50
				THEN CAST(completion_tokens AS REAL) / ((latency_ms - ttft_ms) / 1000.0)
			ELSE CAST(completion_tokens AS REAL) / (latency_ms / 1000.0)
		END`); err != nil {
			return err
		}
		if err := s.SetSetting("tps_repair_v1", "1"); err != nil {
			return err
		}
	}
	// One-time backfill of Factory credit estimates for rows logged before the
	// credits column existed.
	if v, _ := s.GetSetting("credits_backfill_v1", ""); v != "1" {
		if err := s.backfillCredits(); err != nil {
			return err
		}
		if err := s.SetSetting("credits_backfill_v1", "1"); err != nil {
			return err
		}
	}
	return nil
}

// backfillCredits recomputes estimated credits for historical log rows using
// the current model cost parameters.
func (s *Store) backfillCredits() error {
	rows, err := s.db.Query(`SELECT id, used_model, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens
		FROM request_logs WHERE credits = 0 AND (completion_tokens > 0 OR prompt_tokens > 0)`)
	if err != nil {
		return err
	}
	type row struct {
		id                         int64
		model                      string
		prompt, completion, cr, cw int
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.model, &r.prompt, &r.completion, &r.cr, &r.cw); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range batch {
		credits := config.EstimateCreditsForUsage(r.model, r.prompt, r.cr, r.cw, r.completion)
		if credits <= 0 {
			continue
		}
		if _, err := s.db.Exec(`UPDATE request_logs SET credits = ? WHERE id = ?`, credits, r.id); err != nil {
			return err
		}
	}
	return nil
}

// hasColumn reports whether a table already has a column.
func (s *Store) hasColumn(table, col string) bool {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == col {
			return true
		}
	}
	return false
}

// ---------- upstream keys ----------

// UpstreamKey is a Factory API key (fk-...) used to call the gateway.
type UpstreamKey struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Key         string     `json:"key"`
	Enabled     bool       `json:"enabled"`
	Weight      int        `json:"weight"`
	LastStatus  string     `json:"last_status"`
	LastChecked *time.Time `json:"last_checked"`
	CreatedAt   time.Time  `json:"created_at"`
}

// ListUpstreamKeys returns all upstream keys.
func (s *Store) ListUpstreamKeys() ([]UpstreamKey, error) {
	rows, err := s.db.Query(`SELECT id,name,key,enabled,weight,last_status,last_checked,created_at FROM upstream_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpstreamKey
	for rows.Next() {
		var k UpstreamKey
		var last sql.NullTime
		if err := rows.Scan(&k.ID, &k.Name, &k.Key, &k.Enabled, &k.Weight, &k.LastStatus, &last, &k.CreatedAt); err != nil {
			return nil, err
		}
		if last.Valid {
			k.LastChecked = &last.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AddUpstreamKey inserts a new upstream key.
func (s *Store) AddUpstreamKey(name, key string, weight int) (int64, error) {
	if weight <= 0 {
		weight = 1
	}
	res, err := s.db.Exec(`INSERT INTO upstream_keys(name,key,weight) VALUES(?,?,?)`, name, key, weight)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateUpstreamKey patches mutable fields.
func (s *Store) UpdateUpstreamKey(id int64, name *string, enabled *bool, weight *int) error {
	if name != nil {
		if _, err := s.db.Exec(`UPDATE upstream_keys SET name=? WHERE id=?`, *name, id); err != nil {
			return err
		}
	}
	if enabled != nil {
		if _, err := s.db.Exec(`UPDATE upstream_keys SET enabled=? WHERE id=?`, *enabled, id); err != nil {
			return err
		}
	}
	if weight != nil {
		if _, err := s.db.Exec(`UPDATE upstream_keys SET weight=? WHERE id=?`, *weight, id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUpstreamKey removes a key.
func (s *Store) DeleteUpstreamKey(id int64) error {
	_, err := s.db.Exec(`DELETE FROM upstream_keys WHERE id=?`, id)
	return err
}

// SetUpstreamKeyStatus records a health-check result.
func (s *Store) SetUpstreamKeyStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE upstream_keys SET last_status=?, last_checked=CURRENT_TIMESTAMP WHERE id=?`, status, id)
	return err
}

// EnabledUpstreamKeys returns enabled keys ordered by weight desc.
func (s *Store) EnabledUpstreamKeys() ([]UpstreamKey, error) {
	all, err := s.ListUpstreamKeys()
	if err != nil {
		return nil, err
	}
	var out []UpstreamKey
	for _, k := range all {
		if k.Enabled {
			out = append(out, k)
		}
	}
	return out, nil
}

// ---------- client keys ----------

// ClientKey is a downstream API key (sk-...) clients use against this server.
type ClientKey struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Key         string    `json:"key"`
	Enabled     bool      `json:"enabled"`
	RPMLimit    int       `json:"rpm_limit"`
	DailyTokens int       `json:"daily_tokens"`
	Models      string    `json:"models"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListClientKeys returns all client keys.
func (s *Store) ListClientKeys() ([]ClientKey, error) {
	rows, err := s.db.Query(`SELECT id,name,key,enabled,rpm_limit,daily_tokens,models,created_at FROM client_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientKey
	for rows.Next() {
		var k ClientKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Key, &k.Enabled, &k.RPMLimit, &k.DailyTokens, &k.Models, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// CreateClientKey inserts a client key.
func (s *Store) CreateClientKey(name string, rpm, dailyTokens int, models string) (ClientKey, error) {
	key, err := GenerateClientKey()
	if err != nil {
		return ClientKey{}, err
	}
	res, err := s.db.Exec(`INSERT INTO client_keys(name,key,rpm_limit,daily_tokens,models) VALUES(?,?,?,?,?)`, name, key, rpm, dailyTokens, models)
	if err != nil {
		return ClientKey{}, err
	}
	id, _ := res.LastInsertId()
	return ClientKey{ID: id, Name: name, Key: key, Enabled: true, RPMLimit: rpm, DailyTokens: dailyTokens, Models: models}, nil
}

// UpdateClientKey patches mutable fields.
func (s *Store) UpdateClientKey(id int64, name *string, enabled *bool, rpm *int, dailyTokens *int, models *string) error {
	set := ""
	args := []any{}
	add := func(frag string, v any) {
		if set != "" {
			set += ","
		}
		set += frag
		args = append(args, v)
	}
	if name != nil {
		add("name=?", *name)
	}
	if enabled != nil {
		add("enabled=?", *enabled)
	}
	if rpm != nil {
		add("rpm_limit=?", *rpm)
	}
	if dailyTokens != nil {
		add("daily_tokens=?", *dailyTokens)
	}
	if models != nil {
		add("models=?", *models)
	}
	if set == "" {
		return nil
	}
	args = append(args, id)
	_, err := s.db.Exec(`UPDATE client_keys SET `+set+` WHERE id=?`, args...)
	return err
}

// DeleteClientKey removes a client key.
func (s *Store) DeleteClientKey(id int64) error {
	_, err := s.db.Exec(`DELETE FROM client_keys WHERE id=?`, id)
	return err
}

// LookupClientKey finds an enabled client key by value.
func (s *Store) LookupClientKey(key string) (ClientKey, bool, error) {
	row := s.db.QueryRow(`SELECT id,name,key,enabled,rpm_limit,daily_tokens,models,created_at FROM client_keys WHERE key=?`, key)
	var k ClientKey
	if err := row.Scan(&k.ID, &k.Name, &k.Key, &k.Enabled, &k.RPMLimit, &k.DailyTokens, &k.Models, &k.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClientKey{}, false, nil
		}
		return ClientKey{}, false, err
	}
	return k, k.Enabled, nil
}

// GenerateClientKey returns a new random downstream key.
func GenerateClientKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-nicedroid-" + hex.EncodeToString(b), nil
}

// ---------- request logs ----------

// RequestLog is one proxied request.
type RequestLog struct {
	ID               int64     `json:"id"`
	TS               time.Time `json:"ts"`
	ClientKey        string    `json:"client_key"`
	RequestedModel   string    `json:"requested_model"`
	UsedModel        string    `json:"used_model"`
	Provider         string    `json:"provider"`
	Pool             string    `json:"pool"`
	Status           int       `json:"status"`
	LatencyMs        int       `json:"latency_ms"`
	TTFTMs           int       `json:"ttft_ms"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CacheReadTokens  int       `json:"cache_read_tokens"`
	CacheWriteTokens int       `json:"cache_write_tokens"`
	Credits          int64     `json:"credits"`
	TPS              float64   `json:"tps"`
	Stream           bool      `json:"stream"`
	Error            string    `json:"error"`
}

// InsertLog writes a request log row. TPS is derived from completion tokens
// over effective generation time (latency minus time-to-first-token for
// streaming requests when that window is meaningful). Credits are estimated
// with the Droid CLI formula unless the caller supplied a value.
func (s *Store) InsertLog(l RequestLog) error {
	l.TPS = ComputeTPS(l.CompletionTokens, l.LatencyMs, l.TTFTMs, l.Stream)
	if l.Credits == 0 {
		l.Credits = config.EstimateCreditsForUsage(l.UsedModel, l.PromptTokens, l.CacheReadTokens, l.CacheWriteTokens, l.CompletionTokens)
	}
	_, err := s.db.Exec(`INSERT INTO request_logs(client_key,requested_model,used_model,provider,pool,status,latency_ms,ttft_ms,prompt_tokens,completion_tokens,cache_read_tokens,cache_write_tokens,credits,tps,stream,error)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.ClientKey, l.RequestedModel, l.UsedModel, l.Provider, l.Pool, l.Status, l.LatencyMs, l.TTFTMs, l.PromptTokens, l.CompletionTokens, l.CacheReadTokens, l.CacheWriteTokens, l.Credits, l.TPS, l.Stream, l.Error)
	return err
}

// minGenerationWindowMs is the smallest latency-minus-TTFT window trusted for
// TPS math. Below it the subtraction is dominated by response serialization
// noise (non-streaming requests record TTFT at the end of the whole response),
// and dividing by such a sliver produces absurd values.
const minGenerationWindowMs = 50

// ComputeTPS calculates output tokens per second of effective generation time.
// Only streaming requests subtract TTFT, and only when the remaining window is
// at least minGenerationWindowMs; everything else uses total latency.
func ComputeTPS(completionTokens, latencyMs, ttftMs int, stream bool) float64 {
	if completionTokens <= 0 || latencyMs <= 0 {
		return 0
	}
	genMs := latencyMs
	if stream && ttftMs > 0 && latencyMs-ttftMs >= minGenerationWindowMs {
		genMs = latencyMs - ttftMs
	}
	return float64(completionTokens) / (float64(genMs) / 1000.0)
}

// LogFilter narrows log queries.
type LogFilter struct {
	Limit      int
	Offset     int
	Model      string
	Client     string
	Since      *time.Time
	ErrorsOnly bool
}

// ListLogs returns recent request logs plus the total matching count.
func (s *Store) ListLogs(f LogFilter) ([]RequestLog, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	if f.Model != "" {
		where += " AND (requested_model=? OR used_model=?)"
		args = append(args, f.Model, f.Model)
	}
	if f.Client != "" {
		where += " AND client_key=?"
		args = append(args, f.Client)
	}
	if f.Since != nil {
		where += " AND ts >= ?"
		args = append(args, *f.Since)
	}
	if f.ErrorsOnly {
		where += " AND status >= 400"
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM request_logs `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id,ts,client_key,requested_model,used_model,provider,pool,status,latency_ms,ttft_ms,prompt_tokens,completion_tokens,cache_read_tokens,cache_write_tokens,credits,tps,stream,error
		FROM request_logs ` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []RequestLog
	for rows.Next() {
		var l RequestLog
		if err := rows.Scan(&l.ID, &l.TS, &l.ClientKey, &l.RequestedModel, &l.UsedModel, &l.Provider, &l.Pool, &l.Status, &l.LatencyMs, &l.TTFTMs, &l.PromptTokens, &l.CompletionTokens, &l.CacheReadTokens, &l.CacheWriteTokens, &l.Credits, &l.TPS, &l.Stream, &l.Error); err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// Stats summarizes recent traffic.
type Stats struct {
	TotalRequests    int           `json:"total_requests"`
	ErrorRequests    int           `json:"error_requests"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	CacheReadTokens  int           `json:"cache_read_tokens"`
	CacheWriteTokens int           `json:"cache_write_tokens"`
	Credits          int64         `json:"credits"`        // estimated Factory credits
	CacheHitRate     float64       `json:"cache_hit_rate"` // cache_read / total input, 0..1
	AvgTPS           float64       `json:"avg_tps"`        // mean TPS across requests with output
	AvgTTFTMs        float64       `json:"avg_ttft_ms"`
	AvgLatencyMs     float64       `json:"avg_latency_ms"`
	ByModel          []ModelStat   `json:"by_model"`
	Hourly           []HourlyPoint `json:"hourly"`
	TPSeries         []TPSPoint    `json:"tps_series"`
}

// ModelStat is per-model aggregation.
type ModelStat struct {
	Model            string  `json:"model"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	Credits          int64   `json:"credits"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
	AvgTPS           float64 `json:"avg_tps"`
}

// HourlyPoint is one bucket of traffic.
type HourlyPoint struct {
	Hour     string `json:"hour"`
	Requests int    `json:"requests"`
	Errors   int    `json:"errors"`
}

// TPSPoint is one bucket of throughput.
type TPSPoint struct {
	Hour         string  `json:"hour"`
	AvgTPS       float64 `json:"avg_tps"`
	TTFTMs       float64 `json:"avg_ttft_ms"`
	CacheHitRate float64 `json:"cache_hit_rate"`
}

// GetStats aggregates traffic for the last `hours` hours.
func (s *Store) GetStats(hours int) (Stats, error) {
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	var st Stats
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status>=400),0), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
			COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0), COALESCE(SUM(credits),0),
			COALESCE(AVG(latency_ms),0), COALESCE(AVG(CASE WHEN tps>0 THEN tps END),0), COALESCE(AVG(CASE WHEN ttft_ms>0 THEN ttft_ms END),0)
		FROM request_logs WHERE ts >= ?`, since).Scan(&st.TotalRequests, &st.ErrorRequests, &st.PromptTokens, &st.CompletionTokens,
		&st.CacheReadTokens, &st.CacheWriteTokens, &st.Credits, &st.AvgLatencyMs, &st.AvgTPS, &st.AvgTTFTMs)
	if err != nil {
		return st, err
	}
	totalInput := st.PromptTokens
	if totalInput > 0 {
		st.CacheHitRate = float64(st.CacheReadTokens) / float64(totalInput)
	}

	rows, err := s.db.Query(`SELECT used_model, COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
			COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0), COALESCE(SUM(credits),0),
			COALESCE(AVG(CASE WHEN tps>0 THEN tps END),0)
		FROM request_logs WHERE ts >= ? GROUP BY used_model ORDER BY COUNT(*) DESC LIMIT 20`, since)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var ms ModelStat
		if err := rows.Scan(&ms.Model, &ms.Requests, &ms.PromptTokens, &ms.CompletionTokens, &ms.CacheReadTokens, &ms.CacheWriteTokens, &ms.Credits, &ms.AvgTPS); err != nil {
			rows.Close()
			return st, err
		}
		if ms.PromptTokens > 0 {
			ms.CacheHitRate = float64(ms.CacheReadTokens) / float64(ms.PromptTokens)
		}
		st.ByModel = append(st.ByModel, ms)
	}
	rows.Close()

	hrows, err := s.db.Query(`SELECT strftime('%Y-%m-%dT%H:00', ts) AS h, COUNT(*), COALESCE(SUM(status>=400),0)
		FROM request_logs WHERE ts >= ? GROUP BY h ORDER BY h`, since)
	if err != nil {
		return st, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var p HourlyPoint
		if err := hrows.Scan(&p.Hour, &p.Requests, &p.Errors); err != nil {
			return st, err
		}
		st.Hourly = append(st.Hourly, p)
	}
	hrows.Close()

	trows, err := s.db.Query(`SELECT strftime('%Y-%m-%dT%H:00', ts) AS h,
			COALESCE(AVG(CASE WHEN tps>0 THEN tps END),0),
			COALESCE(AVG(CASE WHEN ttft_ms>0 THEN ttft_ms END),0),
			COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(prompt_tokens),0)
		FROM request_logs WHERE ts >= ? GROUP BY h ORDER BY h`, since)
	if err != nil {
		return st, err
	}
	defer trows.Close()
	for trows.Next() {
		var p TPSPoint
		var cacheRead, prompt int
		if err := trows.Scan(&p.Hour, &p.AvgTPS, &p.TTFTMs, &cacheRead, &prompt); err != nil {
			return st, err
		}
		if prompt > 0 {
			p.CacheHitRate = float64(cacheRead) / float64(prompt)
		}
		st.TPSeries = append(st.TPSeries, p)
	}
	return st, nil
}

// CleanupLogs deletes logs older than retention days.
func (s *Store) CleanupLogs(retentionDays int) error {
	_, err := s.db.Exec(`DELETE FROM request_logs WHERE ts < datetime('now', ?)`, fmt.Sprintf("-%d days", retentionDays))
	return err
}

// ---------- settings ----------

// GetSetting reads a setting with a default.
func (s *Store) GetSetting(key, def string) (string, error) {
	row := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key)
	var v string
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return def, nil
		}
		return def, err
	}
	return v, nil
}

// SetSetting writes a setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key=?`, key)
	return err
}

// ---------- provider overrides ----------

// GetProviderOverride returns the pinned provider for a model, or "".
func (s *Store) GetProviderOverride(modelID string) (string, error) {
	return s.GetSetting("provider:"+modelID, "")
}

// SetProviderOverride pins a provider for a model; an empty value clears it.
func (s *Store) SetProviderOverride(modelID, provider string) error {
	if provider == "" {
		return s.DeleteSetting("provider:" + modelID)
	}
	return s.SetSetting("provider:"+modelID, provider)
}
