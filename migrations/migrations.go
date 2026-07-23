// Package migrations embeds the numbered schema-migration SQL files into the
// server binary so they ship with every deploy instead of relying on someone
// manually psql-ing them against Railway afterwards. The Dockerfile's final
// stage only copies the compiled binary (see backend/Dockerfile) — it never
// copies this source directory — so without embedding, any migration added
// here never reaches production until it's applied by hand.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
