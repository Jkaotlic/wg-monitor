package backend

import "embed"

//go:embed miniapp_static/*
var miniappStaticFS embed.FS
