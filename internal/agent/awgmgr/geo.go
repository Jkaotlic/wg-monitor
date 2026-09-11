package awgmgr

import (
	"context"
	"fmt"
	"net/url"
)

// geoExpandMaxBody -- потолок тела ответа geo-expand. Общий потолок get
// (1 МиБ) крупные списки категорий перерастают, а обрезанный JSON -- это
// ошибка разбора, то есть «роутер не раскрыл список» там, где он его раскрыл.
const geoExpandMaxBody = 4 << 20

// GeoExpand calls GET /api/hydraroute/geo-expand?kind=<kind>&tag=<tag> and
// returns the lines of the named geo list as awg-manager stores them: for
// geosite these are v2ray-style patterns (".claude.ai", "full:x", "keyword:x",
// "regexp:x", "domain:x").
//
// Shape taken from a live awg-manager 2.18.2 (2026-09-11):
// {"success":true,"data":{"count":8,"lines":[".anthropic.com",...],"path":"..."}}.
func (c *Client) GeoExpand(ctx context.Context, kind, tag string) ([]string, error) {
	var env struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Count int      `json:"count"`
			Lines []string `json:"lines"`
			Path  string   `json:"path"`
		} `json:"data"`
	}
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("tag", tag)
	if err := c.getLimited(ctx, "/api/hydraroute/geo-expand?"+q.Encode(), &env, geoExpandMaxBody); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr geo-expand %s:%s: success=false: %s", kind, tag, env.Error)
	}
	return env.Data.Lines, nil
}
