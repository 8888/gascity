package sqlite

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/benchmarks/coordstore"
)

// TestExplainFilterScanAndReady is a diagnostic harness for the ga-2s6sz
// spike: it seeds the same row counts as RealWorldWorkload, runs
// EXPLAIN QUERY PLAN on the queries that fail the 10ms p99 target
// (FilterScan main, Ready CTE) plus the queries that pass (Mail-poll
// ephemeral, FilterScan with fewer predicates) as controls, and times
// 100 single-threaded executions of each. Gated behind
// COORDSTORE_EQP=1 so the normal test suite is unaffected.
func TestExplainFilterScanAndReady(t *testing.T) {
	if os.Getenv("COORDSTORE_EQP") == "" {
		t.Skip("set COORDSTORE_EQP=1 to run EXPLAIN QUERY PLAN diagnostic")
	}
	dir := t.TempDir()
	ctx := context.Background()

	a := NewWithDriver(DefaultDriverName, FullSyncPragmas, "eqp")
	if err := a.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = a.Close() }()

	seeder := coordstore.NewSeeder(0x1234abcd)
	seed, err := seeder.Seed(ctx, a, coordstore.RealWorldWorkload)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	t.Logf("seeded: %d main_open, %d main_closed, %d wisps, %d deps",
		len(seed.MainOpenIDs), len(seed.MainClosedIDs), len(seed.WispOpenIDs), len(seed.DepEdges))

	path := dir + "/store.db"
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, "ANALYZE;"); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	type query struct {
		Name string
		SQL  string
		Args []any
	}
	queries := []query{
		{
			Name: "FilterScan-failing (Status=open, Type=session, Assignee=X)",
			SQL: `SELECT r.id,r.title,r.status,r.type,r.priority,r.created_at,r.assignee,r.parent_id
			      FROM records r WHERE 1=1 AND r.status=? AND r.type=? AND r.assignee=?`,
			Args: []any{"open", "session", "mayor"},
		},
		{
			Name: "Ready-failing (CTE + NOT IN subquery)",
			SQL: `WITH blocked AS (
			          SELECT DISTINCT d.issue_id
			          FROM deps d
			          JOIN records b ON b.id = d.depends_on_id
			          WHERE b.status IN ('open','in_progress')
			      )
			      SELECT r.id,r.title,r.status,r.type,r.priority,r.created_at,r.assignee,r.parent_id
			      FROM records r
			      WHERE r.status IN ('open','in_progress')
			        AND r.type NOT IN (?,?,?,?,?,?,?,?,?)
			        AND r.id NOT IN (SELECT issue_id FROM blocked)
			        AND r.assignee=?`,
			Args: []any{
				"merge-request", "gate", "molecule", "step", "message",
				"session", "agent", "role", "rig", "mayor",
			},
		},
		{
			Name: "Mail-poll-passing (Type=message, Status=open, Assignee=X)",
			SQL: `SELECT id,title,status,type,created_at,assignee,parent_id,expires_at
			      FROM ephemeral
			      WHERE 1=1 AND type=? AND status=? AND assignee=?`,
			Args: []any{"message", "open", "mayor"},
		},
	}

	for _, q := range queries {
		t.Logf("\n=== %s ===", q.Name)
		t.Logf("SQL:\n%s", strings.TrimSpace(q.SQL))

		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q.SQL, q.Args...)
		if err != nil {
			t.Errorf("EQP: %v", err)
			continue
		}
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Errorf("EQP scan: %v", err)
				continue
			}
			t.Logf("  [%d/%d] %s", id, parent, detail)
		}
		_ = rows.Close()

		// Single-threaded latency baseline (no concurrent writers, no
		// checkpoint pressure — just the planner's chosen execution cost).
		const N = 200
		start := time.Now()
		var rowCount int
		for i := 0; i < N; i++ {
			r, err := db.QueryContext(ctx, q.SQL, q.Args...)
			if err != nil {
				t.Errorf("exec: %v", err)
				continue
			}
			c := 0
			for r.Next() {
				c++
			}
			_ = r.Close()
			if i == 0 {
				rowCount = c
			}
		}
		elapsed := time.Since(start)
		t.Logf("Timing: %d execs in %s — avg %s/exec, first-result rowcount=%d",
			N, elapsed, elapsed/N, rowCount)
	}
}
