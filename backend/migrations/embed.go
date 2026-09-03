package migrations

import "embed"

// SQL holds numbered schema files applied on API startup in filename order.
//
//go:embed *.sql
var SQL embed.FS
