package postgres

import (
	"context"
	"telesrv/deploy"
	"testing"
)

func TestDonationDefaultsMigrationPreservesConfiguredChains(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	suffix := randomSuffix(t)
	for _, row := range []struct{ key, url string }{{"empty", ""}, {"seed", "http://127.0.0.1:7545"}, {"configured", "https://chain.example/rpc"}} {
		_, err = tx.Exec(ctx, `INSERT INTO donation_chains(chain_key,name,chain_id,rpc_url,native_symbol,native_decimals,enabled) VALUES($1,'test',1,$2,'ETH',18,true)`, "intake-"+row.key+suffix, row.url)
		if err != nil {
			t.Fatal(err)
		}
	}
	sql, err := deploy.Migrations.ReadFile("migrations/0263_donation_chains_off_by_default.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"empty", "seed", "configured"} {
			var enabled bool
			if err = tx.QueryRow(ctx, `SELECT enabled FROM donation_chains WHERE chain_key=$1`, "intake-"+key+suffix).Scan(&enabled); err != nil {
				t.Fatal(err)
			}
			if enabled != (key == "configured") {
				t.Fatalf("pass %d chain %s enabled=%v", i, key, enabled)
			}
		}
	}
}
