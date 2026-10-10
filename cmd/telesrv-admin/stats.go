package main

import (
	"context"
	"fmt"
	"net/http"
)

// DailyCountPoint is one day of a single-number time series (registrations,
// active users, ...). Date is the "2006-01-02" label the chart draws on the
// x axis.
type DailyCountPoint struct {
	Date  string `json:"date"`
	Value int64  `json:"value"`
}

// DailyStarPoint is one day of the stars economy: both the number of gifts
// sent and the total stars spent on them.
type DailyStarPoint struct {
	Date  string `json:"date"`
	Stars int64  `json:"stars"`
	Gifts int64  `json:"gifts"`
}

// StatsResponse is what the /api/stats panel page renders. All the chart
// arrays are complete daily series covering the leading `Days` days, with
// zero-filled days where nothing happened, so the client never has to realign
// sparse rows onto a calendar.
type StatsResponse struct {
	StarsGivenToday  int64             `json:"stars_given_today"`
	GiftsSentToday   int64             `json:"gifts_sent_today"`
	StarsGivenTotal  int64             `json:"stars_given_total"`
	GiftsSentTotal   int64             `json:"gifts_sent_total"`
	StarBalanceTotal int64             `json:"star_balance_total"`
	UsersTotal       int64             `json:"users_total"`
	OnlineNow        int64             `json:"online_now"`
	Days             int               `json:"days"`
	UsersChart       []DailyCountPoint `json:"users_chart"`
	ActiveChart      []DailyCountPoint `json:"active_chart"`
	StarsChart       []DailyStarPoint  `json:"stars_chart"`
}

const (
	statsDefaultDays = 14
	statsMaxDays     = 90
)

// AdminStats aggregates the ecosystem numbers the statistics page shows. Every
// time range is evaluated against the database clock (date_trunc('day',
// now())) so the panel and the server cannot disagree about where today ends.
func (s *readStore) AdminStats(ctx context.Context, days int) (StatsResponse, error) {
	var out StatsResponse
	if days <= 0 {
		days = statsDefaultDays
	}
	if days > statsMaxDays {
		days = statsMaxDays
	}
	out.Days = days

	// Summary scalars. "Stars given" is the money spent on gifts, derived from
	// the immutable catalog-revision price a gift was bought at rather than the
	// living catalog row, so resellers/price edits never rewrite history.
	if err := s.pool.QueryRow(ctx, `
SELECT
  (SELECT COUNT(*) FROM peer_star_gifts p
     JOIN star_gift_catalog_revisions r ON r.id = p.catalog_revision_id
    WHERE p.gift_date >= extract(epoch FROM date_trunc('day', now()))::bigint
      AND p.gift_date <  extract(epoch FROM date_trunc('day', now()) + interval '1 day')::bigint),
  (SELECT COALESCE(SUM(r.stars),0) FROM peer_star_gifts p
     JOIN star_gift_catalog_revisions r ON r.id = p.catalog_revision_id
    WHERE p.gift_date >= extract(epoch FROM date_trunc('day', now()))::bigint
      AND p.gift_date <  extract(epoch FROM date_trunc('day', now()) + interval '1 day')::bigint),
  (SELECT COUNT(*) FROM peer_star_gifts),
  (SELECT COALESCE(SUM(r.stars),0) FROM peer_star_gifts p
     JOIN star_gift_catalog_revisions r ON r.id = p.catalog_revision_id),
  (SELECT COALESCE(SUM(balance),0) FROM stars_balances)
`).Scan(&out.GiftsSentToday, &out.StarsGivenToday, &out.GiftsSentTotal, &out.StarsGivenTotal, &out.StarBalanceTotal); err != nil {
		return out, fmt.Errorf("stats summary: %w", err)
	}

	var err error
	if out.UsersTotal, err = s.CountAccounts(ctx); err != nil {
		return out, err
	}
	if out.OnlineNow, err = s.CountOnlineAccounts(ctx); err != nil {
		return out, err
	}

	seriesStart := "date_trunc('day', now()) - (($1::int - 1) * interval '1 day')"

	if out.UsersChart, err = dailyCounts(ctx, s, days, `
	SELECT d.d::text, COALESCE(u.c, 0)
	  FROM (SELECT (date_trunc('day', now()) - (($1::int - 1) * interval '1 day'))::date + gs AS d
	          FROM generate_series(0, $1::int - 1) gs) d
	  LEFT JOIN (SELECT date(created_at) AS day, count(*) AS c
	               FROM users
	              WHERE NOT is_bot AND deleted_at IS NULL
	                AND created_at >= `+seriesStart+`
	              GROUP BY date(created_at)) u ON u.day = d.d
	 ORDER BY d.d`); err != nil {
		return out, err
	}

	if out.ActiveChart, err = dailyCounts(ctx, s, days, `
	SELECT d.d::text, COALESCE(u.c, 0)
	  FROM (SELECT (date_trunc('day', now()) - (($1::int - 1) * interval '1 day'))::date + gs AS d
	          FROM generate_series(0, $1::int - 1) gs) d
	  LEFT JOIN (SELECT date(active_at) AS day, count(DISTINCT user_id) AS c
	               FROM authorizations
	              WHERE active_at >= `+seriesStart+`
	              GROUP BY date(active_at)) u ON u.day = d.d
	 ORDER BY d.d`); err != nil {
		return out, err
	}

	if out.StarsChart, err = dailyStars(ctx, s, days, `
	SELECT d.d::text, COALESCE(u.gifts,0), COALESCE(u.stars,0)
	  FROM (SELECT (date_trunc('day', now()) - (($1::int - 1) * interval '1 day'))::date + gs AS d
	          FROM generate_series(0, $1::int - 1) gs) d
	  LEFT JOIN (SELECT date(to_timestamp(p.gift_date)) AS day,
	                    count(*) AS gifts,
	                    COALESCE(SUM(r.stars),0) AS stars
	               FROM peer_star_gifts p
	               LEFT JOIN star_gift_catalog_revisions r ON r.id = p.catalog_revision_id
	              WHERE p.gift_date >= extract(epoch FROM (`+seriesStart+`))::bigint
	              GROUP BY date(to_timestamp(p.gift_date))) u ON u.day = d.d
	 ORDER BY d.d`); err != nil {
		return out, err
	}

	return out, nil
}

func dailyCounts(ctx context.Context, s *readStore, days int, query string) ([]DailyCountPoint, error) {
	rows, err := s.pool.Query(ctx, query, days)
	if err != nil {
		return nil, fmt.Errorf("stats daily counts: %w", err)
	}
	defer rows.Close()
	out := make([]DailyCountPoint, 0, days)
	for rows.Next() {
		var p DailyCountPoint
		if err := rows.Scan(&p.Date, &p.Value); err != nil {
			return nil, fmt.Errorf("stats daily counts scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats daily counts iterate: %w", err)
	}
	return out, nil
}

func dailyStars(ctx context.Context, s *readStore, days int, query string) ([]DailyStarPoint, error) {
	rows, err := s.pool.Query(ctx, query, days)
	if err != nil {
		return nil, fmt.Errorf("stats daily stars: %w", err)
	}
	defer rows.Close()
	out := make([]DailyStarPoint, 0, days)
	for rows.Next() {
		var p DailyStarPoint
		if err := rows.Scan(&p.Date, &p.Gifts, &p.Stars); err != nil {
			return nil, fmt.Errorf("stats daily stars scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats daily stars iterate: %w", err)
	}
	return out, nil
}

func (s *server) handleStatsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	days, _ := parseInt(r.URL.Query().Get("days"))
	stats, err := s.read.AdminStats(r.Context(), days)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
