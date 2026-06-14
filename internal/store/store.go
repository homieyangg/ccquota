package store

import (
	"database/sql"
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
`)
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
