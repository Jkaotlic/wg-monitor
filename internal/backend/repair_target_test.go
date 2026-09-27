package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// MINI-05 (бэкенд): ход починки говорит, какой VPN-туннель чинится. Без
// этого экран не отличал «починку этого туннеля» от починки соседнего и
// рисовал «Чиню» не там.
func TestMiniappRepairStatusNamesTarget(t *testing.T) {
	d, routerID, tgUser := linkRepairDeps(t)
	h := NewMux(d)
	start := doRepair(t, h, http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tgUser,
		`{"check_name":"tunnel_awg11"}`)
	if start.Code != http.StatusAccepted {
		t.Fatalf("старт: %d %s", start.Code, start.Body.String())
	}
	st := doRepair(t, h, http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tgUser, "")
	if st.Code != http.StatusOK {
		t.Fatalf("ход: %d %s", st.Code, st.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(st.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["check_name"] != "tunnel_awg11" || resp["tunnel_id"] != "awg11" {
		t.Fatalf("цель починки не названа: %s", st.Body.String())
	}
}
