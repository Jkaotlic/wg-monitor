package provision

import "testing"

// Locked только смотрит: пробный захват мог бы ответить «занято» чужому мастеру.
func TestStoreLockedDoesNotTakeLock(t *testing.T) {
	s := NewStore()
	if s.Locked("роутер") {
		t.Fatal("замок никто не брал")
	}
	if !s.TryLock("роутер") {
		t.Fatal("Locked взял замок")
	}
	if !s.Locked("роутер") {
		t.Fatal("замок занят, а Locked не видит")
	}
	s.Unlock("роутер")
}
