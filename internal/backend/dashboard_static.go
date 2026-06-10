package backend

import "embed"

//go:embed dashboard_static/*
//go:embed dashboard_static/vendor/*
var dashboardStaticFS embed.FS
