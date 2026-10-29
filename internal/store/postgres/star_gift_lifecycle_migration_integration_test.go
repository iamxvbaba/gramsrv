package postgres

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"telesrv/deploy"
)

// knownMigrationGaps 是历史遗留的空号：0123 从来没有存在过。golang-migrate 只保存一个
// schema_migrations.version，凡是低于它的迁移都不会再被执行，所以新开一个空号等于把
// 那条迁移永久丢在门外——除这份明示清单以外的任何空号都是事故。
var knownMigrationGaps = map[uint]struct{}{123: {}}

// embeddedMigrationVersions 以 deploy.Migrations 为唯一事实来源，按后缀取出版本号到文
// 件名的映射，顺手钉住重号。
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

// headMigrationVersion 从嵌入的迁移脚本推导期望版本号。硬编码版本号每加一条迁移
// 就会失效（本用例曾长期停留在 185），因此以 deploy.Migrations 为唯一事实来源。
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

// 只看最大文件名的检查会漏掉真正危险的形态：中间留一个空号，等着以后补。真实部署一
// 旦跑到后面的版本，补进来的那条就永远低于 schema_migrations.version，既不会执行也不
// 会报错。所以这里钉的是整条序列——不重号、每条 up 都有 down、逐一递增。
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
