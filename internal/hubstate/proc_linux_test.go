package hubstate

import "testing"

func TestParseStarttimeCommWithParens(t *testing.T) {
	stat := "123 (a) b (c d) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 424242 23 24"
	got, err := parseStarttime(stat)
	if err != nil || got != 424242 {
		t.Fatalf("got %d, %v", got, err)
	}
	if _, err := parseStarttime("1 (x) S 1 2"); err == nil {
		t.Fatal("want error on short stat")
	}
}
