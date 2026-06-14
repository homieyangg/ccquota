package update

import "testing"

func TestParseRelease(t *testing.T) {
	js := `{"tag_name":"v0.2.0","html_url":"https://x/releases/v0.2.0","assets":[
		{"name":"ccquota-linux-arm64","browser_download_url":"https://x/a"},
		{"name":"SHA256SUMS","browser_download_url":"https://x/s"}]}`
	r, err := parseRelease([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "v0.2.0" || r.Assets["ccquota-linux-arm64"] != "https://x/a" || r.Assets["SHA256SUMS"] != "https://x/s" {
		t.Fatalf("unexpected %+v", r)
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.1", "v0.2.0", false},
		{"dev", "v0.1.0", true},
		{"", "v0.1.0", true},
		{"v0.1.0", "", false},
		{"v0.9.0", "v0.10.0", true},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.latest); got != c.want {
			t.Errorf("Newer(%q,%q)=%v want %v", c.cur, c.latest, got, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	sums := "abc123  ccquota-linux-arm64\ndef456  ccquota-darwin-arm64\n"
	got, err := checksumFor(sums, "ccquota-darwin-arm64")
	if err != nil || got != "def456" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := checksumFor(sums, "nope"); err == nil {
		t.Fatal("want error for missing asset")
	}
}
