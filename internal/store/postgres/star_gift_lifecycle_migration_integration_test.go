package postgres

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"telesrv/deploy"
)

// knownMigrationGaps are the legacy empty slots: 0123 never existed. golang-migrate stores only
// a single schema_migrations.version, and any migration below it is never executed again, so
// opening a new empty slot means permanently locking that migration out -- any empty slot other
// than the ones in this explicit list is an incident.
var knownMigrationGaps = map[uint]struct{}{123: {}}

// embeddedMigrationVersions uses deploy.Migrations as the single source of truth, builds the
// version-to-filename mapping by suffix, and pins down duplicate versions while it is at it.
func embeddedMigrationVersions(t *testing.T, suffix string) map[uint]string {
	t.Helper()

	entries, err := deploy.Migrations.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	out := make(map[uint]string, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}

		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("migration %q has no version prefix", name)
		}

		version, err := strconv.ParseUint(prefix, 10, strconv.IntSize)
		if err != nil {
			t.Fatalf("migration %q has a non-numeric version prefix: %v", name, err)
		}

		if previous, duplicate := out[uint(version)]; duplicate {
			t.Fatalf("migrations %q and %q share version %d", previous, name, version)
		}

		out[uint(version)] = name
	}

	if len(out) == 0 {
		t.Fatalf("no embedded %s migrations found", suffix)
	}

	return out
}

// headMigrationVersion derives the expected version number from the embedded migration scripts.
// A hard-coded version number breaks with every added migration (this test case long sat at 185),
// so deploy.Migrations is the single source of truth.
func headMigrationVersion(t *testing.T) uint {
	t.Helper()

	var head uint
	for version := range embeddedMigrationVersions(t, ".up.sql") {
		if version > head {
			head = version
		}
	}

	return head
}

// A check that only looks at the largest file name would miss the genuinely dangerous shape: an
// empty slot left in the middle, waiting to be filled in later. Once a real deployment has reached
// a later version, the migration filled in afterward is forever below schema_migrations.version;
// it will neither execute nor error. So this pins the entire sequence: no duplicate versions,
// every up has a down, and they increase one by one.
func TestEmbeddedMigrationsAreContiguousAndReversible(t *testing.T) {
	ups := embeddedMigrationVersions(t, ".up.sql")
	downs := embeddedMigrationVersions(t, ".down.sql")

	versions := make([]uint, 0, len(ups))
	for version := range ups {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	if versions[0] != 1 {
		t.Fatalf("migration series starts at %q, want version 1", ups[versions[0]])
	}

	for index, version := range versions {
		if _, ok := downs[version]; !ok {
			t.Fatalf("migration %q has no down script", ups[version])
		}

		if index == 0 {
			continue
		}

		previous := versions[index-1]
		// These published timestamp versions follow the original numbered chain.
		// Pin every predecessor so future additions cannot silently fill a gap.
		timestampPredecessors := map[uint]uint{
			20260924140000: 223,
			20260924150000: 20260924140000,
			20260926010000: 20260924150000,
			20260927010000: 20260926010000,
			20260928010000: 20260927010000,
			20260928020000: 20260928010000,
			20260928030000: 20260928020000,
			20261012010000: 20260928030000,
			20261013010000: 20261012010000,
			20261015010000: 20261013010000,
			20261016010000: 20261015010000,
			20261017010000: 20261016010000,
			20261024010000: 20261017010003,
			20261025020000: 20261024010000,
			20261029010000: 20261025020000,
			20261029010001: 20261029010000,
			20261031010000: 20261029010002,
		}
		if expected, ok := timestampPredecessors[version]; ok {
			if previous != expected {
				t.Fatalf("migration %d predecessor = %d, want %d", version, previous, expected)
			}
			continue
		}
		for missing := previous + 1; missing < version; missing++ {
			if _, allowed := knownMigrationGaps[missing]; allowed {
				continue
			}

			t.Fatalf("migration series skips %04d between %q and %q: golang-migrate keeps a single "+
				"schema_migrations.version, so a migration landed there later would never run",
				missing, ups[previous], ups[version])
		}
	}

	for version, name := range downs {
		if _, ok := ups[version]; !ok {
			t.Fatalf("down migration %q has no up script", name)
		}
	}

	for gap := range knownMigrationGaps {
		if name, filled := ups[gap]; filled {
			t.Fatalf("migration %q filled historic gap %04d: drop it from knownMigrationGaps", name, gap)
		}
	}
}

func TestStarGiftLifecycleMigrationsApply(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}

	head := headMigrationVersion(t)

	status, err := MigrateAndStatus(dsn)
	if err != nil {
		t.Fatalf("migrate star gift lifecycle schema: %v", err)
	}

	if status.Dirty || status.Empty || status.Version != head {
		t.Fatalf("migration status = %+v, want clean version %d", status, head)
	}
}
