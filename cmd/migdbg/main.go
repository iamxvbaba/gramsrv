package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"telesrv/deploy"
	"telesrv/internal/store/postgres"
)

func main() {
	dsn := os.Args[1]
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, "postgres://telesrv:telesrv@127.0.0.1:5432/postgres?sslmode=disable")
	if err != nil {
		panic(err)
	}
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS telesrv_migcheck WITH (FORCE)")
	_, _ = admin.Exec(ctx, "CREATE DATABASE telesrv_migcheck OWNER telesrv")
	_ = admin.Close(ctx)
	time.Sleep(300 * time.Millisecond)

	if err := postgres.Migrate(dsn); err != nil {
		fmt.Println("MIGRATE_ERR:", err)
		os.Exit(1)
	}
	st, err := postgres.MigrateAndStatus(dsn)
	fmt.Println("STATUS:", st, "err=", err)

	src, err := iofs.New(deploy.Migrations, "migrations")
	if err != nil {
		panic(err)
	}
	versions := map[uint]bool{}
	v, err := src.First()
	if err != nil {
		panic(err)
	}
	for {
		versions[v] = true
		_, _, rerr := src.ReadUp(v)
		if rerr != nil {
			fmt.Println("READUP_ERR", v, rerr)
		}
		nv, nerr := src.Next(v)
		if nerr != nil {
			break
		}
		v = nv
	}
	fmt.Println("EMBED_COUNT:", len(versions), "MAX:", maxVersion(versions), "HAS_204:", versions[204])
}

func maxVersion(m map[uint]bool) uint {
	var best uint
	for v := range m {
		if v > best {
			best = v
		}
	}
	return best
}
