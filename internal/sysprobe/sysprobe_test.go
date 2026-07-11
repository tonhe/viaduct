package sysprobe

import "testing"

func TestHasIPv4Transport(t *testing.T) {
	got1 := HasIPv4Transport()
	got2 := HasIPv4Transport()
	if got1 != got2 {
		t.Errorf("HasIPv4Transport not idempotent: %v then %v", got1, got2)
	}
}

func TestHasIPv6Transport_Idempotent(t *testing.T) {
	got1 := HasIPv6Transport()
	got2 := HasIPv6Transport()
	if got1 != got2 {
		t.Errorf("HasIPv6Transport not idempotent: %v then %v", got1, got2)
	}
}
