// Package migrations embeds the numbered SQL schema migrations, so the
// rateguard binary can apply them without the files being shipped next to
// it. Each change is an NNNNNN_name.up.sql / .down.sql pair; an applied
// migration is never edited.
package migrations

import "embed"

// FS holds every migration file in this directory. An embed.FS is
// read-only, so sharing it package-wide carries no mutable state.
//
//go:embed *.sql
var FS embed.FS
