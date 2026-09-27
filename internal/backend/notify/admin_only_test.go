package notify

import (
	"context"
	"testing"
)

// Весть только админу (REV-02): ровно один адресат -- админ из конфига;
// админ не настроен -- никому.
func TestAdminOnly_SendsToAdminAlone(t *testing.T) {
	s := &fakeSender{}
	if err := NewAdminOnly(s, 777).SendAdmin(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if len(s.sent) != 1 || s.sent[0] != 777 {
		t.Fatalf("sent=%v", s.sent)
	}
	s2 := &fakeSender{}
	if err := NewAdminOnly(s2, 0).SendAdmin(context.Background(), "x"); err != nil || len(s2.sent) != 0 {
		t.Fatalf("без админа: sent=%v err=%v", s2.sent, err)
	}
}
