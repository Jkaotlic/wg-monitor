package callbacks

import (
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestSimpleAuditCache_VersionAudit(t *testing.T) {
	c := newSimpleAuditCache()
	if _, ok := c.GetVersionAudit(1); ok {
		t.Error("empty cache should miss")
	}
	in := wire.VersionAudit{AwgmgrVersion: "2.8.2"}
	c.PutVersionAudit(1, in)
	got, ok := c.GetVersionAudit(1)
	if !ok || got.AwgmgrVersion != "2.8.2" {
		t.Errorf("got=%+v ok=%v", got, ok)
	}
	if _, ok := c.GetVersionAudit(2); ok {
		t.Error("other user must miss")
	}
}

func TestSimpleAuditCache_FirmwareStatus(t *testing.T) {
	c := newSimpleAuditCache()
	if _, ok := c.GetFirmwareStatus(1); ok {
		t.Error("empty cache should miss")
	}
	c.PutFirmwareStatus(1, wire.FirmwareStatus{Current: "5.0.1"})
	got, ok := c.GetFirmwareStatus(1)
	if !ok || got.Current != "5.0.1" {
		t.Errorf("got=%+v ok=%v", got, ok)
	}
}
