//go:build manual

package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// Ручная разведка на живом роутере через терминал awg-manager. ТОЛЬКО чтение:
// ни одна команда ниже ничего не меняет. Секреты не печатаются -- строки с
// token/secret/key/password вырезаются на самом роутере.
//
//	AWGM_URL=http://192.168.0.1:2222 go test -tags manual ./cmd/deploy/ -run LiveTerminalProbe -v
func TestLiveTerminalProbe(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	if base == "" {
		t.Skip("AWGM_URL not set")
	}
	c := &AWGMClient{BaseURL: base, APIKey: os.Getenv("AWGM_API_KEY")}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	script := `
echo "== uname =="; uname -a
echo "== wg-monitor processes =="; ps w 2>/dev/null | grep -i 'wg-monitor' | grep -v grep
echo "== init.d =="; ls /opt/etc/init.d/ 2>/dev/null
echo "== /opt/etc/wg-monitor =="; ls -la /opt/etc/wg-monitor 2>/dev/null
echo "== listening sockets =="; netstat -ltn 2>/dev/null | head -30
echo "== config (без секретов) =="; sed -e 's/\(token\|secret\|key\|password\)[^ ]*:.*/\1: ***/I' /opt/etc/wg-monitor/config.yaml 2>/dev/null | head -30
echo "== keendns/webproxy hints =="; ls /opt/etc/ 2>/dev/null | head -40
`
	res, err := c.RunTerminalScriptWithLogin(ctx, script, os.Getenv("ROUTER_USER"), os.Getenv("ROUTER_PASS"))
	t.Logf("err=%v\n%s", err, res.Output)
}
