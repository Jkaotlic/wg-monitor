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

// GeoFile is one entry of GET /api/hydraroute/geo-files. A router can carry two
// sets at once -- awg-manager's own and the external HydraRoute ones -- so the
// same tag may live in several files.
type GeoFile struct {
	Type     string `json:"type"` // geosite | geoip
	Path     string `json:"path"`
	TagCount int    `json:"tagCount"`
	External bool   `json:"external"`
}

// GeoTag is one tag of a geo file; Count is the number of entries behind it
// (domain patterns for geosite, networks for geoip).
type GeoTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// GeoFiles calls GET /api/hydraroute/geo-files.
// Shape taken from a live awg-manager (workrouter, 2026-10-27):
// {"success":true,"data":[{"type":"geoip","path":"...","tagCount":311,...}]}.
func (c *Client) GeoFiles(ctx context.Context) ([]GeoFile, error) {
	var env struct {
		Success bool      `json:"success"`
		Error   string    `json:"error"`
		Data    []GeoFile `json:"data"`
	}
	if err := c.get(ctx, "/api/hydraroute/geo-files", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr geo-files: success=false: %s", env.Error)
	}
	return env.Data, nil
}

// GeoTags calls GET /api/hydraroute/geo-tags?path=<file> -- the tags of one
// geo file: {"success":true,"data":[{"name":"ADOBE","count":33},...]}.
func (c *Client) GeoTags(ctx context.Context, path string) ([]GeoTag, error) {
	var env struct {
		Success bool     `json:"success"`
		Error   string   `json:"error"`
		Data    []GeoTag `json:"data"`
	}
	q := url.Values{}
	q.Set("path", path)
	if err := c.getLimited(ctx, "/api/hydraroute/geo-tags?"+q.Encode(), &env, geoExpandMaxBody); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr geo-tags %s: success=false: %s", path, env.Error)
	}
	return env.Data, nil
}
