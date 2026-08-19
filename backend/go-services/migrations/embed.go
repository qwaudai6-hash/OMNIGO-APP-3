package migrations

import "embed"

// Migrations contains all golang-migrate SQL files.
//
//go:embed *.sql
var Migrations embed.FS
