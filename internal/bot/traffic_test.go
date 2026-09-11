package bot

import (
	"testing"
	"vpn-bot/internal/client/panel"
)

func TestFormatTraffic(t *testing.T) {
	cases := []struct {
		v    *panel.ClientTraffic
		want string
	}{{&panel.ClientTraffic{Up: 734 * 1024 * 1024, Total: 1024 * 1024 * 1024}, "734 MB / 1.00 GB"}, {&panel.ClientTraffic{Up: 4 * 1024 * 1024 * 1024, Down: 335544320, Total: 0}, "4.31 GB / ∞"}, {&panel.ClientTraffic{Total: -1}, "0 MB / ∞"}}
	for _, tc := range cases {
		if got := formatTraffic(tc.v); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}
