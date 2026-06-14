package store

import "testing"

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestChannelsCRUD(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateChannel(Channel{Type: "telegram", Config: `{"bot_token":"enc:abc","chat_id":"1"}`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListChannels()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if list[0].Type != "telegram" || !list[0].Enabled {
		t.Fatalf("unexpected channel %+v", list[0])
	}
	if err := s.UpdateChannel(Channel{ID: id, Type: "telegram", Config: `{"chat_id":"2"}`, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetChannel(id)
	if err != nil || !ok || got.Enabled || got.Config != `{"chat_id":"2"}` {
		t.Fatalf("after update: %+v ok=%v err=%v", got, ok, err)
	}
	if err := s.DeleteChannel(id); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListChannels(); len(list) != 0 {
		t.Fatalf("want empty after delete, got %v", list)
	}
}

func TestAlertThresholdsDefaultsAndSet(t *testing.T) {
	s := newTestStore(t)
	def, err := s.GetAlertThresholds()
	if err != nil {
		t.Fatal(err)
	}
	if def.SevenDayWarn != 75 || def.SevenDayCrit != 90 || def.FiveHourCrit != 95 || !def.ResetNotify {
		t.Fatalf("unexpected defaults %+v", def)
	}
	if err := s.SetAlertThresholds(AlertThresholds{SevenDayWarn: 60, SevenDayCrit: 80, FiveHourCrit: 99, ResetNotify: false}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAlertThresholds()
	if got.SevenDayWarn != 60 || got.SevenDayCrit != 80 || got.FiveHourCrit != 99 || got.ResetNotify {
		t.Fatalf("after set %+v", got)
	}
}
