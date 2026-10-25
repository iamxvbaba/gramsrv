package postgres

import (
	"context"
	"math"
	"testing"
	"time"

	"telesrv/deploy"
	"telesrv/internal/domain"
)

func TestRatingScaleMigrationMatchesDomainPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := ratingTestUser(t, pool, time.Now().UnixNano(), time.Now().UTC())
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO account_rating(user_id,computed_at,updated_at,stars_component,manual_component,pending_stars,pending_date)
VALUES($1,now(),now(),123,17,5,now())`, userID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := deploy.Migrations.ReadFile("migrations/0261_rating_level_scale.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	scores := []int64{-1, 0, math.MaxInt64}
	for level := 1; level <= domain.MaxAccountRatingLevel; level++ {
		threshold := domain.AccountRatingLevelThreshold(level)
		scores = append(scores, threshold-1, threshold, threshold+1)
	}
	for _, score := range scores {
		_, err = tx.Exec(ctx, `UPDATE account_rating SET stars=$2,level=0,current_level_stars=0,next_level_stars=25 WHERE user_id=$1`, userID, score)
		if err != nil {
			t.Fatal(err)
		}
		var before string
		err = tx.QueryRow(ctx, `SELECT (to_jsonb(r)-'level'-'current_level_stars'-'next_level_stars')::text FROM account_rating r WHERE user_id=$1`, userID).Scan(&before)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
		var level int
		var current int64
		var next *int64
		var after string
		err = tx.QueryRow(ctx, `SELECT level,current_level_stars,next_level_stars,(to_jsonb(r)-'level'-'current_level_stars'-'next_level_stars')::text FROM account_rating r WHERE user_id=$1`, userID).Scan(&level, &current, &next, &after)
		if err != nil {
			t.Fatal(err)
		}
		wantLevel, wantCurrent, wantNext, hasNext := domain.AccountRatingLevelForStars(score)
		if level != wantLevel || current != wantCurrent || (next != nil) != hasNext || (hasNext && *next != wantNext) || before != after {
			t.Fatalf("score %d: migration level/current/next=%d/%d/%v, domain=%d/%d/%d/%v, unrelated state unchanged=%v", score, level, current, next, wantLevel, wantCurrent, wantNext, hasNext, before == after)
		}
	}
}
