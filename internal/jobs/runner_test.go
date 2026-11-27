package jobs

import "testing"

func TestRetentionDays(t *testing.T) {
	cases := map[string]int{
		"":      90,
		"30":    30,
		"1":     1,
		"0":     90,
		"-5":    90,
		"junk":  90,
		"99999": 3650,
	}
	for detail, want := range cases {
		if got := retentionDays(detail); got != want {
			t.Errorf("retentionDays(%q) = %d, want %d", detail, got, want)
		}
	}
}
