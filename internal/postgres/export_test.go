package postgres

import (
	"maps"

	"github.com/jmoiron/sqlx"
)

// DBForTest exposes the connection only to external integration tests.
func DBForTest(s *Store) *sqlx.DB { return s.db }

// AllowedForTest gives test bots an independent copy of the configured allowlist.
func AllowedForTest(s *Store) map[int64]bool { return maps.Clone(s.allowed) }
