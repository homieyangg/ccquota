package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenCreatesParentDir 確保 Open 會自動建立 DB 檔的上層目錄，
// 避免目錄不存在時報 SQLite CANTOPEN(14)。
func TestOpenCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sub", "ccquota.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open 應自動建立上層目錄，卻失敗: %v", err)
	}
	defer s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("DB 檔未建立: %v", err)
	}
}

func TestUpsertAndGetAccount(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a := Account{ID: "acct1", Label: "main", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1000}
	if err := s.UpsertAccount(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount("acct1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "main" || got.RefreshToken != "rt" || got.ExpiresAt != 1000 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	list, err := s.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 account, got %d", len(list))
	}
}

func TestReadingsAndEvents(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	_ = s.UpsertAccount(Account{ID: "a", Label: "a", AccessToken: "x", RefreshToken: "y", ExpiresAt: 1})

	r := Reading{AccountID: "a", TS: 100, SevenDay: 14, FiveHour: 8, SevenDayResetsAt: 999, FiveHourResetsAt: 555}
	if err := s.InsertReading(r); err != nil {
		t.Fatal(err)
	}
	last, ok, err := s.LatestReading("a")
	if err != nil || !ok {
		t.Fatalf("latest err=%v ok=%v", err, ok)
	}
	if last.SevenDay != 14 || last.SevenDayResetsAt != 999 {
		t.Fatalf("bad latest: %+v", last)
	}
	if _, ok, _ := s.LatestReading("missing"); ok {
		t.Fatal("expected no reading for missing account")
	}
	if err := s.InsertEvent("a", 101, "reset", `{"from":18,"to":6}`); err != nil {
		t.Fatal(err)
	}
}

func TestHistory(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	_ = s.UpsertAccount(Account{ID: "a", Label: "a", AccessToken: "x", RefreshToken: "y", ExpiresAt: 1})
	for _, ts := range []int64{100, 200, 300} {
		_ = s.InsertReading(Reading{AccountID: "a", TS: ts, SevenDay: float64(ts)})
	}
	hist, err := s.History("a", 150)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("want 2 readings (ts>=150), got %d", len(hist))
	}
	if hist[0].TS != 200 || hist[1].TS != 300 {
		t.Fatalf("wrong order or values: %+v", hist)
	}
}

func TestAlertState(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 首次查詢尚未 fired
	fired, err := s.AlertAlreadyFired("acct1", "weekly", "1700000000:warn")
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Fatal("expected not fired")
	}

	// 標記已 fired
	if err := s.MarkAlertFired("acct1", "weekly", "1700000000:warn", 1700000001); err != nil {
		t.Fatal(err)
	}

	// 再查應為 true
	fired, err = s.AlertAlreadyFired("acct1", "weekly", "1700000000:warn")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("expected fired")
	}

	// 不同 account 同 key → false
	fired, err = s.AlertAlreadyFired("acct2", "weekly", "1700000000:warn")
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Fatal("different account should not be fired")
	}

	// 同 account 不同 window_key → false（新視窗 → re-arm）
	fired, err = s.AlertAlreadyFired("acct1", "weekly", "1800000000:warn")
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Fatal("new window_key should not be fired")
	}

	// upsert 覆蓋同一 key 不報錯
	if err := s.MarkAlertFired("acct1", "weekly", "1700000000:warn", 1700000999); err != nil {
		t.Fatal(err)
	}
}

func TestUserCostRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 插入兩筆 alice、一筆 bob，不同 ts
	if err := s.InsertUserCost("acct1", "alice", 1000, 0.05, 500); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertUserCost("acct1", "alice", 1100, 0.03, 300); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertUserCost("acct1", "bob", 1050, 0.10, 1000); err != nil {
		t.Fatal(err)
	}
	// 另一個 account，不應混入
	if err := s.InsertUserCost("acct2", "alice", 1000, 9.99, 99999); err != nil {
		t.Fatal(err)
	}

	// UserPeriodCosts：since=0 應含所有紀錄
	costs, err := s.UserPeriodCosts("acct1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) != 2 {
		t.Fatalf("want 2 users, got %d: %v", len(costs), costs)
	}
	alice := costs["alice"]
	if alice.Tokens != 800 {
		t.Fatalf("alice tokens: want 800, got %d", alice.Tokens)
	}
	wantCost := 0.08
	if alice.Cost < wantCost-1e-9 || alice.Cost > wantCost+1e-9 {
		t.Fatalf("alice cost: want %.4f, got %.4f", wantCost, alice.Cost)
	}
	if costs["bob"].Tokens != 1000 {
		t.Fatalf("bob tokens: want 1000, got %d", costs["bob"].Tokens)
	}

	// since=1060 → 只有 alice ts=1100 符合
	filtered, err := s.UserPeriodCosts("acct1", 1060)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 {
		t.Fatalf("want 1 user after ts filter, got %d", len(filtered))
	}
	if filtered["alice"].Tokens != 300 {
		t.Fatalf("filtered alice tokens: want 300, got %d", filtered["alice"].Tokens)
	}

	// AccountPeriodCost
	total, err := s.AccountPeriodCost("acct1", 0)
	if err != nil {
		t.Fatal(err)
	}
	wantTotal := 0.18
	if total < wantTotal-1e-9 || total > wantTotal+1e-9 {
		t.Fatalf("account total: want %.4f, got %.4f", wantTotal, total)
	}

	// 空帳號應回傳 0 而非 error
	empty, err := s.AccountPeriodCost("nobody", 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty != 0 {
		t.Fatalf("empty account cost: want 0, got %f", empty)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 不存在時 ok=false
	_, ok, err := s.GetSetting("admin_password_hash")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected ok=false for missing key")
	}

	// 寫入後可讀回
	if err := s.SetSetting("admin_password_hash", "hexhash"); err != nil {
		t.Fatal(err)
	}
	val, ok, err := s.GetSetting("admin_password_hash")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true after set")
	}
	if val != "hexhash" {
		t.Fatalf("want 'hexhash', got %q", val)
	}

	// 覆蓋同一 key
	if err := s.SetSetting("admin_password_hash", "newhash"); err != nil {
		t.Fatal(err)
	}
	val2, _, _ := s.GetSetting("admin_password_hash")
	if val2 != "newhash" {
		t.Fatalf("after override: want 'newhash', got %q", val2)
	}
}

func TestBudgetBaselineRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 不存在時回 0
	if v, err := s.BudgetHWM("main"); err != nil || v != 0 {
		t.Fatalf("missing hwm 應為 0,得 %v err=%v", v, err)
	}
	if v, err := s.LastWeekBudget("main"); err != nil || v != 0 {
		t.Fatalf("missing lastweek 應為 0,得 %v err=%v", v, err)
	}

	// 寫入後讀回,per-account 各自獨立
	if err := s.SetBudgetHWM("main", 123.5, 96); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastWeekBudget("main", 99.25); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.BudgetHWM("main"); v != 123.5 {
		t.Fatalf("hwm 應 123.5,得 %v", v)
	}
	if v, _ := s.BudgetHWMPct("main"); v != 96 {
		t.Fatalf("hwm_pct 應 96,得 %v", v)
	}
	if v, _ := s.LastWeekBudget("main"); v != 99.25 {
		t.Fatalf("lastweek 應 99.25,得 %v", v)
	}
	if v, _ := s.BudgetHWM("other"); v != 0 {
		t.Fatalf("別的帳號不該共用,得 %v", v)
	}
}

func TestAlertMessageRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if _, ok, err := s.GetAlertMessage("main", "w1", "tg:1"); err != nil || ok {
		t.Fatalf("missing 應 ok=false,得 ok=%v err=%v", ok, err)
	}
	if err := s.UpsertAlertMessage("main", "w1", "tg:1", "555", "warn"); err != nil {
		t.Fatal(err)
	}
	m, ok, _ := s.GetAlertMessage("main", "w1", "tg:1")
	if !ok || m.Ref != "555" || m.Tier != "warn" {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
	// 升級覆蓋 tier,ref 不變
	_ = s.UpsertAlertMessage("main", "w1", "tg:1", "555", "crit")
	m2, _, _ := s.GetAlertMessage("main", "w1", "tg:1")
	if m2.Tier != "crit" {
		t.Fatalf("升級後 tier 應 crit,得 %q", m2.Tier)
	}
}

func TestEnrollmentRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const now = int64(1000)

	// 建立 enrollment
	if err := s.CreateEnrollment("tok1", "acct1", "alice", now+3600); err != nil {
		t.Fatal(err)
	}

	// 正常取得
	acctID, user, ok, err := s.GetEnrollment("tok1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if acctID != "acct1" || user != "alice" {
		t.Fatalf("wrong values: acctID=%q user=%q", acctID, user)
	}

	// 不存在的 token
	_, _, ok2, err2 := s.GetEnrollment("no-such-token", now)
	if err2 != nil {
		t.Fatal(err2)
	}
	if ok2 {
		t.Fatal("expected ok=false for missing token")
	}

	// 已過期
	_, _, ok3, err3 := s.GetEnrollment("tok1", now+7200)
	if err3 != nil {
		t.Fatal(err3)
	}
	if ok3 {
		t.Fatal("expected ok=false for expired token")
	}
}
