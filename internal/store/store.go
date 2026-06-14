package store

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type Account struct {
	ID           string
	Label        string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64 // unix seconds
}

// dsn 組裝 SQLite DSN，附加 busy_timeout pragma（5 秒）以避免並行寫入立即報錯。
func dsn(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_pragma=busy_timeout(5000)"
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  access_token TEXT NOT NULL,
  refresh_token TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE TABLE IF NOT EXISTS readings (
  account_id TEXT NOT NULL,
  ts INTEGER NOT NULL,
  seven_day REAL, five_hour REAL, sonnet REAL, opus REAL,
  seven_day_resets_at INTEGER, five_hour_resets_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_readings_acct_ts ON readings(account_id, ts);
CREATE TABLE IF NOT EXISTS events (
  account_id TEXT NOT NULL,
  ts INTEGER NOT NULL,
  type TEXT NOT NULL,
  detail TEXT
);
CREATE TABLE IF NOT EXISTS alert_state (
  account_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  window_key TEXT NOT NULL,
  fired_at INTEGER NOT NULL,
  PRIMARY KEY (account_id, kind, window_key)
);
CREATE TABLE IF NOT EXISTS user_cost (
  account_id TEXT NOT NULL,
  user TEXT NOT NULL,
  ts INTEGER NOT NULL,
  cost_usd REAL NOT NULL,
  tokens INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_cost_acct_ts ON user_cost(account_id, ts);
CREATE TABLE IF NOT EXISTS enrollments (
  token TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  user TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL,
  config TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
`)
	return err
}

// GetSetting 讀取 key 對應的設定值；ok=false 表示不存在。
func (s *Store) GetSetting(key string) (value string, ok bool, err error) {
	row := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key)
	if e := row.Scan(&value); e != nil {
		if e == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, e
	}
	return value, true, nil
}

// SetSetting 寫入或覆蓋一個設定值。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value,
	)
	return err
}

// UserCost 代表單一 user 在某段期間的累計成本與 token 數。
type UserCost struct {
	Cost   float64
	Tokens int64
}

// InsertUserCost 寫入一筆 user 成本紀錄。
func (s *Store) InsertUserCost(accountID, user string, ts int64, cost float64, tokens int64) error {
	_, err := s.db.Exec(
		`INSERT INTO user_cost (account_id, user, ts, cost_usd, tokens) VALUES (?, ?, ?, ?, ?)`,
		accountID, user, ts, cost, tokens,
	)
	return err
}

// UserPeriodCosts 回傳 accountID 自 sinceTS 以來，各 user 的累計成本與 token 數。
func (s *Store) UserPeriodCosts(accountID string, sinceTS int64) (map[string]UserCost, error) {
	rows, err := s.db.Query(
		`SELECT user, SUM(cost_usd), SUM(tokens) FROM user_cost
		 WHERE account_id=? AND ts>=? GROUP BY user`,
		accountID, sinceTS,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]UserCost)
	for rows.Next() {
		var u string
		var c UserCost
		if err := rows.Scan(&u, &c.Cost, &c.Tokens); err != nil {
			return nil, err
		}
		out[u] = c
	}
	return out, rows.Err()
}

// AccountPeriodCost 回傳 accountID 自 sinceTS 以來的總成本（USD）。
func (s *Store) AccountPeriodCost(accountID string, sinceTS int64) (float64, error) {
	var total float64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0) FROM user_cost WHERE account_id=? AND ts>=?`,
		accountID, sinceTS,
	).Scan(&total)
	return total, err
}

func (s *Store) UpsertAccount(a Account) error {
	_, err := s.db.Exec(`
INSERT INTO accounts (id,label,access_token,refresh_token,expires_at)
VALUES (?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  label=excluded.label, access_token=excluded.access_token,
  refresh_token=excluded.refresh_token, expires_at=excluded.expires_at`,
		a.ID, a.Label, a.AccessToken, a.RefreshToken, a.ExpiresAt)
	return err
}

func (s *Store) GetAccount(id string) (Account, error) {
	var a Account
	err := s.db.QueryRow(`SELECT id,label,access_token,refresh_token,expires_at FROM accounts WHERE id=?`, id).
		Scan(&a.ID, &a.Label, &a.AccessToken, &a.RefreshToken, &a.ExpiresAt)
	return a, err
}

func (s *Store) ListAccounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT id,label,access_token,refresh_token,expires_at FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Label, &a.AccessToken, &a.RefreshToken, &a.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type Reading struct {
	AccountID                          string
	TS                                 int64
	SevenDay, FiveHour, Sonnet, Opus   float64
	SevenDayResetsAt, FiveHourResetsAt int64
}

func (s *Store) InsertReading(r Reading) error {
	_, err := s.db.Exec(`
INSERT INTO readings (account_id,ts,seven_day,five_hour,sonnet,opus,seven_day_resets_at,five_hour_resets_at)
VALUES (?,?,?,?,?,?,?,?)`,
		r.AccountID, r.TS, r.SevenDay, r.FiveHour, r.Sonnet, r.Opus, r.SevenDayResetsAt, r.FiveHourResetsAt)
	return err
}

// LatestReading returns the newest reading for an account. ok=false if none.
func (s *Store) LatestReading(accountID string) (Reading, bool, error) {
	var r Reading
	err := s.db.QueryRow(`
SELECT account_id,ts,seven_day,five_hour,sonnet,opus,seven_day_resets_at,five_hour_resets_at
FROM readings WHERE account_id=? ORDER BY ts DESC LIMIT 1`, accountID).
		Scan(&r.AccountID, &r.TS, &r.SevenDay, &r.FiveHour, &r.Sonnet, &r.Opus, &r.SevenDayResetsAt, &r.FiveHourResetsAt)
	if err == sql.ErrNoRows {
		return Reading{}, false, nil
	}
	return r, err == nil, err
}

func (s *Store) InsertEvent(accountID string, ts int64, typ, detail string) error {
	_, err := s.db.Exec(`INSERT INTO events (account_id,ts,type,detail) VALUES (?,?,?,?)`,
		accountID, ts, typ, detail)
	return err
}

// History returns readings with ts >= sinceTS, ascending order.
func (s *Store) History(accountID string, sinceTS int64) ([]Reading, error) {
	rows, err := s.db.Query(`
SELECT account_id,ts,seven_day,five_hour,sonnet,opus,seven_day_resets_at,five_hour_resets_at
FROM readings WHERE account_id=? AND ts>=? ORDER BY ts ASC`, accountID, sinceTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reading
	for rows.Next() {
		var r Reading
		if err := rows.Scan(&r.AccountID, &r.TS, &r.SevenDay, &r.FiveHour, &r.Sonnet, &r.Opus, &r.SevenDayResetsAt, &r.FiveHourResetsAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AlertAlreadyFired 回傳該 (account, kind, windowKey) 組合是否已觸發過通知。
func (s *Store) AlertAlreadyFired(account, kind, windowKey string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM alert_state WHERE account_id=? AND kind=? AND window_key=?`,
		account, kind, windowKey,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateEnrollment 建立一筆一次性 enrollment token。
func (s *Store) CreateEnrollment(token, accountID, user string, expiresAt int64) error {
	_, err := s.db.Exec(
		`INSERT INTO enrollments (token, account_id, user, expires_at) VALUES (?, ?, ?, ?)`,
		token, accountID, user, expiresAt,
	)
	return err
}

// GetEnrollment 查詢 enrollment token；now > expires_at 時 ok=false。
func (s *Store) GetEnrollment(token string, now int64) (accountID, user string, ok bool, err error) {
	row := s.db.QueryRow(
		`SELECT account_id, user, expires_at FROM enrollments WHERE token=?`, token,
	)
	var expiresAt int64
	if e := row.Scan(&accountID, &user, &expiresAt); e != nil {
		if e == sql.ErrNoRows {
			return "", "", false, nil
		}
		return "", "", false, e
	}
	if now > expiresAt {
		return "", "", false, nil
	}
	return accountID, user, true, nil
}

// MarkAlertFired 記錄通知已發送（upsert；ts 為 unix seconds）。
func (s *Store) MarkAlertFired(account, kind, windowKey string, ts int64) error {
	_, err := s.db.Exec(
		`INSERT INTO alert_state (account_id, kind, window_key, fired_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(account_id, kind, window_key) DO UPDATE SET fired_at=excluded.fired_at`,
		account, kind, windowKey, ts,
	)
	return err
}

// Channel 是一個通知頻道設定。Config 是 JSON，secret 欄位已加密。
type Channel struct {
	ID        int64
	Type      string
	Config    string
	Enabled   bool
	CreatedAt int64
}

// CreateChannel 新增頻道，回傳新 id。
func (s *Store) CreateChannel(ch Channel) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO channels (type, config, enabled) VALUES (?, ?, ?)`,
		ch.Type, ch.Config, boolToInt(ch.Enabled),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateChannel 依 id 更新頻道。
func (s *Store) UpdateChannel(ch Channel) error {
	_, err := s.db.Exec(
		`UPDATE channels SET type=?, config=?, enabled=? WHERE id=?`,
		ch.Type, ch.Config, boolToInt(ch.Enabled), ch.ID,
	)
	return err
}

// ListChannels 回傳所有頻道（依 id）。
func (s *Store) ListChannels() ([]Channel, error) {
	rows, err := s.db.Query(`SELECT id, type, config, enabled, created_at FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var en int
		if err := rows.Scan(&c.ID, &c.Type, &c.Config, &en, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Enabled = en != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel 依 id 取單一頻道；ok=false 表示不存在。
func (s *Store) GetChannel(id int64) (Channel, bool, error) {
	var c Channel
	var en int
	err := s.db.QueryRow(`SELECT id, type, config, enabled, created_at FROM channels WHERE id=?`, id).
		Scan(&c.ID, &c.Type, &c.Config, &en, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return Channel{}, false, nil
	}
	if err != nil {
		return Channel{}, false, err
	}
	c.Enabled = en != 0
	return c, true, nil
}

// DeleteChannel 依 id 刪除頻道。
func (s *Store) DeleteChannel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM channels WHERE id=?`, id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// AlertThresholds 是告警門檻設定。
type AlertThresholds struct {
	SevenDayWarn float64
	SevenDayCrit float64
	FiveHourCrit float64
	ResetNotify  bool
}

// GetAlertThresholds 從 settings 讀門檻；未設定時回預設（75/90/95/on）。
func (s *Store) GetAlertThresholds() (AlertThresholds, error) {
	t := AlertThresholds{SevenDayWarn: 75, SevenDayCrit: 90, FiveHourCrit: 95, ResetNotify: true}
	if v, ok, err := s.GetSetting("alert_seven_day_warn"); err != nil {
		return t, err
	} else if ok {
		fmt.Sscanf(v, "%g", &t.SevenDayWarn)
	}
	if v, ok, err := s.GetSetting("alert_seven_day_crit"); err != nil {
		return t, err
	} else if ok {
		fmt.Sscanf(v, "%g", &t.SevenDayCrit)
	}
	if v, ok, err := s.GetSetting("alert_five_hour_crit"); err != nil {
		return t, err
	} else if ok {
		fmt.Sscanf(v, "%g", &t.FiveHourCrit)
	}
	if v, ok, err := s.GetSetting("alert_reset_notify"); err != nil {
		return t, err
	} else if ok {
		t.ResetNotify = v == "1"
	}
	return t, nil
}

// SetAlertThresholds 把門檻寫進 settings。
func (s *Store) SetAlertThresholds(t AlertThresholds) error {
	if err := s.SetSetting("alert_seven_day_warn", fmt.Sprintf("%g", t.SevenDayWarn)); err != nil {
		return err
	}
	if err := s.SetSetting("alert_seven_day_crit", fmt.Sprintf("%g", t.SevenDayCrit)); err != nil {
		return err
	}
	if err := s.SetSetting("alert_five_hour_crit", fmt.Sprintf("%g", t.FiveHourCrit)); err != nil {
		return err
	}
	return s.SetSetting("alert_reset_notify", boolToSetting(t.ResetNotify))
}

func boolToSetting(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// SeriesBucket 是某時間桶內的 user 成本/ token 加總。TS 為桶起點(epoch 秒)。
type SeriesBucket struct {
	TS     int64
	Cost   float64
	Tokens int64
}

// UserSeries 回傳 accountID/user 自 sinceTS 起、依 bucketSec 分桶的成本/ token 加總(只含非空桶,依時間升序)。
func (s *Store) UserSeries(accountID, user string, sinceTS, bucketSec int64) ([]SeriesBucket, error) {
	if bucketSec <= 0 {
		bucketSec = 600
	}
	rows, err := s.db.Query(
		`SELECT (ts/?)*? AS b, SUM(cost_usd), SUM(tokens)
		   FROM user_cost
		  WHERE account_id=? AND user=? AND ts>=?
		  GROUP BY b ORDER BY b`,
		bucketSec, bucketSec, accountID, user, sinceTS,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesBucket
	for rows.Next() {
		var b SeriesBucket
		if err := rows.Scan(&b.TS, &b.Cost, &b.Tokens); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DistinctUsers 回傳 accountID 有成本紀錄的不同 user 清單(依名稱)。
func (s *Store) DistinctUsers(accountID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT user FROM user_cost WHERE account_id=? ORDER BY user`, accountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser 刪除某 user 的成本紀錄與 enrollment(同一交易)。
func (s *Store) DeleteUser(accountID, user string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_cost WHERE account_id=? AND user=?`, accountID, user); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM enrollments WHERE account_id=? AND user=?`, accountID, user); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
