package service

import "testing"

func TestBuildSubscriptionURL(t *testing.T) {
	got, err := BuildSubscriptionURL("https://77.110.97.23:2053/panel", 2096, "sub-id")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://77.110.97.23:2096/sub/sub-id" {
		t.Fatalf("got %q", got)
	}
}
