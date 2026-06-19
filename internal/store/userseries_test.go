package store

import "testing"

func TestUserSeriesBuckets(t *testing.T) {
	s := newTestStore(t)
	// 同一桶(bucket=600s)內兩筆會加總;不同桶分開。
	s.InsertUserCost("main", "gary", 1000, 1.0, 100)
	s.InsertUserCost("main", "gary", 1100, 2.0, 200) // 與上一筆同桶(1000/600==1100/600==1)
	s.InsertUserCost("main", "gary", 1700, 0.5, 50)  // 1700/600==2,另一桶
	s.InsertUserCost("main", "primo", 1000, 9.0, 900)

	rows, err := s.UserSeries("main", "gary", 0, 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 buckets, got %d (%+v)", len(rows), rows)
	}
	if rows[0].TS != 600 || rows[0].Cost != 3.0 || rows[0].Tokens != 300 {
		t.Fatalf("bucket0 = %+v", rows[0])
	}
	if rows[1].TS != 1200 || rows[1].Cost != 0.5 || rows[1].Tokens != 50 {
		t.Fatalf("bucket1 = %+v", rows[1])
	}
}

func TestDistinctUsers(t *testing.T) {
	s := newTestStore(t)
	s.InsertUserCost("main", "gary", 1000, 1, 1)
	s.InsertUserCost("main", "primo", 1000, 1, 1)
	s.InsertUserCost("main", "gary", 2000, 1, 1)
	us, err := s.DistinctUsers("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 2 {
		t.Fatalf("want 2 users, got %v", us)
	}
}

func TestDistinctUsersSince(t *testing.T) {
	s := newTestStore(t)
	s.InsertUserCost("main", "old", 100, 1, 1)    // 早於視窗,排除
	s.InsertUserCost("main", "gary", 1000, 1, 1)   // 早於視窗,排除
	s.InsertUserCost("main", "primo", 5000, 1, 1)  // 視窗內
	us, err := s.DistinctUsersSince("main", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 || us[0] != "primo" {
		t.Fatalf("want [primo], got %v", us)
	}
}

func TestDeleteUser(t *testing.T) {
	s := newTestStore(t)
	s.InsertUserCost("main", "gary", 1000, 1, 1)
	s.CreateEnrollment("tok1", "main", "gary", 9999999999)
	if err := s.DeleteUser("main", "gary"); err != nil {
		t.Fatal(err)
	}
	if us, _ := s.DistinctUsers("main"); len(us) != 0 {
		t.Fatalf("user_cost not cleared: %v", us)
	}
	if _, _, ok, _ := s.GetEnrollment("tok1", 1); ok {
		t.Fatal("enrollment not cleared")
	}
}
