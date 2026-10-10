package deploy

import (
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestMigrationsHaveUniqueVersions(t *testing.T) {
	source, err := iofs.New(Migrations, "migrations")
	if err != nil {
		t.Fatalf("initialize embedded migrations: %v", err)
	}
	t.Cleanup(func() {
		_ = source.Close()
	})
}
