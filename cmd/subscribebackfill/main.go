// Command subscribebackfill joins existing accounts into the auto-subscribe channels, mirroring the
// registration auto-subscribe hook (TELESRV_AUTOSUBSCRIBE_CHANNELS / auth.WithAutoSubscribe) for
// accounts created before the feature existed. scripts/fixsubscribe.sh and scripts/subscribe.sh are
// thin wrappers around this tool.
//
// Channels are given as numeric IDs or @usernames; when none are given the tool falls back to
// TELESRV_AUTOSUBSCRIBE_CHANNELS (process env first, then the .env file), and finally to the top N
// non-deleted channels by participant count (default N=3). Accounts are every active non-bot row in
// the users table; deleted tombstones and system accounts (777000 and friends) are skipped.
//
// Joins go through the same store path the channels RPC uses, so member row, dialog, participant
// count, read watermarks and megagroup service messages are written transactionally and idempotently:
// users who are already members are counted and skipped. Real-time "you joined" update fan-out is not
// emitted; the channel appears on the client's next dialog sync. The server does not need to be
// stopped, but restart it after editing .env so new registrations pick the channels up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// envEntry is one KEY=VALUE pair parsed out of a .env file.
type envEntry map[string]string

// loadEnvFile parses a .env file leniently (comments, blank lines, optional "export " prefix,
// surrounding quotes) and returns the entries; a missing file is not an error.
func loadEnvFile(path string) (envEntry, error) {
	out := envEntry{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			out[key] = value
		}
	}
	return out, nil
}

// targetChannel is one candidate channel for the backfill.
type targetChannel struct {
	ID                int64
	Username          string
	Title             string
	Broadcast         bool
	Megagroup         bool
	JoinRequest       bool
	Monoforum         bool
	ParticipantsCount int
}

func (c targetChannel) label() string {
	name := c.Username
	if name == "" {
		name = fmt.Sprintf("id=%d", c.ID)
	}
	kind := "channel"
	if c.Megagroup {
		kind = "group"
	}
	return fmt.Sprintf("%d %s (%s, %q, %d members)", c.ID, name, kind, c.Title, c.ParticipantsCount)
}

// loadCandidateChannels reads every non-deleted channel with the columns the tool reasons about.
func loadCandidateChannels(ctx context.Context, db *pgxpool.Pool) ([]targetChannel, error) {
	rows, err := db.Query(ctx, `
SELECT id, COALESCE(username, ''), title, broadcast, megagroup, join_request, monoforum, participants_count
FROM channels
WHERE NOT deleted
ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	out := []targetChannel{}
	for rows.Next() {
		var c targetChannel
		if err := rows.Scan(&c.ID, &c.Username, &c.Title, &c.Broadcast, &c.Megagroup, &c.JoinRequest, &c.Monoforum, &c.ParticipantsCount); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// resolveChannels maps the requested specs (numeric IDs or @usernames) onto candidate channels.
// Unknown specs are reported as errors; the caller decides whether to continue.
func resolveChannels(candidates []targetChannel, specs []string) ([]targetChannel, []string) {
	byID := map[int64]targetChannel{}
	byName := map[string]targetChannel{}
	for _, c := range candidates {
		byID[c.ID] = c
		if c.Username != "" {
			byName[strings.ToLower(c.Username)] = c
		}
	}
	resolved := []targetChannel{}
	missing := []string{}
	seen := map[int64]bool{}
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(spec, "@"), 10, 64); err == nil {
			if c, ok := byID[id]; ok && !seen[c.ID] {
				seen[c.ID] = true
				resolved = append(resolved, c)
			} else if !ok {
				missing = append(missing, spec)
			}
			continue
		}
		name := strings.TrimPrefix(spec, "@")
		if c, ok := byName[strings.ToLower(name)]; ok {
			if !seen[c.ID] {
				seen[c.ID] = true
				resolved = append(resolved, c)
			}
			continue
		}
		missing = append(missing, spec)
	}
	return resolved, missing
}

// pickTopChannels selects the top n channels by participant count, largest first. Broadcast channels
// and megagroups both qualify; join-request channels are kept so the caller can warn about them.
func pickTopChannels(candidates []targetChannel, n int) []targetChannel {
	if n <= 0 {
		return nil
	}
	sorted := append([]targetChannel(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ParticipantsCount > sorted[j].ParticipantsCount
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// listUserIDs enumerates the accounts to subscribe: every active non-bot row, minus system accounts.
func listUserIDs(ctx context.Context, db *pgxpool.Pool) ([]int64, error) {
	rows, err := db.Query(ctx, `SELECT id FROM users WHERE deleted_at IS NULL AND NOT is_bot ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user id: %w", err)
		}
		if domain.IsSystemUserID(id) {
			continue
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func parseIDList(spec string) ([]int64, error) {
	out := []int64{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q: not a positive id", part)
		}
		out = append(out, id)
	}
	return out, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "subscribebackfill:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultEnvPath := os.Getenv("TELESRV_ENV_FILE")
	if defaultEnvPath == "" {
		defaultEnvPath = "./.env"
	}
	envPath := flag.String("env", defaultEnvPath, "path to the deployment .env used for DSN/channel defaults")
	dsnFlag := flag.String("dsn", "", "PostgreSQL DSN (default: TELESRV_POSTGRES_DSN env, then the .env file)")
	channelsFlag := flag.String("channels", "", "comma-separated channel IDs or @usernames (default: TELESRV_AUTOSUBSCRIBE_CHANNELS, then top N)")
	top := flag.Int("top", 3, "when no channels resolve, join the top N channels by participant count")
	usersFlag := flag.String("users", "", "restrict to these comma-separated user IDs (default: all active non-bot accounts)")
	list := flag.Bool("list", false, "list candidate channels and account count, then exit")
	resolve := flag.Bool("resolve", false, "resolve the channel selection to comma-separated numeric IDs, then exit (for scripts)")
	dryRun := flag.Bool("dry-run", false, "report what would be joined without writing")
	flag.Parse()

	fileEnv, err := loadEnvFile(*envPath)
	if err != nil {
		return err
	}
	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("TELESRV_POSTGRES_DSN")
	}
	if dsn == "" {
		dsn = fileEnv["TELESRV_POSTGRES_DSN"]
	}
	if dsn == "" {
		return fmt.Errorf("no PostgreSQL DSN: pass -dsn, set TELESRV_POSTGRES_DSN, or add it to %s", *envPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer pool.Close()

	candidates, err := loadCandidateChannels(ctx, pool)
	if err != nil {
		return err
	}
	// Channel specs come from -channels and/or positional arguments (scripts pass them positionally).
	specs := splitSpecs(*channelsFlag)
	specs = append(specs, flag.Args()...)
	if len(specs) == 0 {
		specs = splitSpecs(os.Getenv("TELESRV_AUTOSUBSCRIBE_CHANNELS"))
	}
	if len(specs) == 0 {
		specs = splitSpecs(fileEnv["TELESRV_AUTOSUBSCRIBE_CHANNELS"])
	}
	resolved, missing := resolveChannels(candidates, specs)
	// --resolve output must stay a clean, parseable id list on stdout for scripts, so any
	// informational selection report goes to stderr in that mode.
	announce := os.Stdout
	if *resolve {
		announce = os.Stderr
	}
	if len(resolved) == 0 && !*list {
		resolved = pickTopChannels(candidates, *top)
		if len(resolved) == 0 {
			return fmt.Errorf("no channels to subscribe: create a channel first, or pass -channels id1,id2")
		}
		fmt.Fprintf(announce, "no explicit channels given; using the top %d channel(s) by member count:\n", *top)
		for _, c := range resolved {
			fmt.Fprintf(announce, "  %s\n", c.label())
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(announce, "warning: unknown channel spec(s), skipped: %s\n", strings.Join(missing, ", "))
	}
	if *resolve {
		ids := make([]string, 0, len(resolved))
		for _, c := range resolved {
			ids = append(ids, strconv.FormatInt(c.ID, 10))
		}
		fmt.Println(strings.Join(ids, ","))
		return nil
	}
	if *list {
		fmt.Printf("channels (%d):\n", len(candidates))
		for _, c := range candidates {
			marker := ""
			if c.JoinRequest {
				marker += " [join-request]"
			}
			if c.Monoforum {
				marker += " [monoforum]"
			}
			fmt.Printf("  %s%s\n", c.label(), marker)
		}
		return nil
	}

	// Sanity-check the selection before touching accounts: a join-request or monoforum channel cannot
	// take forced members, so it would fail (or merely pend) for every user below.
	usable := []targetChannel{}
	for _, c := range resolved {
		switch {
		case c.Monoforum:
			fmt.Printf("skipping channel %s: monoforum channels do not support joins\n", c.label())
		case c.JoinRequest:
			fmt.Printf("skipping channel %s: channel requires join approval; auto-subscribe cannot force membership\n", c.label())
		default:
			usable = append(usable, c)
		}
	}
	if len(usable) == 0 {
		return fmt.Errorf("no subscribable channels left after skipping join-request/monoforum channels")
	}

	var userIDs []int64
	if *usersFlag != "" {
		userIDs, err = parseIDList(*usersFlag)
		if err != nil {
			return fmt.Errorf("-users: %w", err)
		}
		if len(userIDs) == 0 {
			return fmt.Errorf("-users: no user ids given")
		}
	} else {
		userIDs, err = listUserIDs(ctx, pool)
		if err != nil {
			return err
		}
	}
	if *dryRun {
		fmt.Printf("dry run: would join %d account(s) into %d channel(s):\n", len(userIDs), len(usable))
		for _, c := range usable {
			fmt.Printf("  %s\n", c.label())
		}
		return nil
	}

	channels := postgres.NewChannelStore(pool)
	date := int(time.Now().Unix())
	type channelStats struct {
		joined  int
		existed int
		pending int
		banned  int
		failed  int
	}
	stats := map[int64]*channelStats{}
	for _, c := range usable {
		stats[c.ID] = &channelStats{}
	}
	for _, userID := range userIDs {
		for _, c := range usable {
			if _, err := channels.JoinChannel(ctx, c.ID, userID, date); err != nil {
				s := stats[c.ID]
				switch {
				case errors.Is(err, domain.ErrUserAlreadyParticipant):
					s.existed++
				case errors.Is(err, domain.ErrInviteRequestSent):
					s.pending++
				case errors.Is(err, domain.ErrChannelUserBanned):
					s.banned++
				default:
					s.failed++
					if s.failed <= 3 {
						fmt.Printf("warning: join user %d -> channel %d failed: %v\n", userID, c.ID, err)
					}
				}
			} else {
				stats[c.ID].joined++
			}
		}
	}

	fmt.Printf("subscribed %d account(s) into %d channel(s):\n", len(userIDs), len(usable))
	for _, c := range usable {
		s := stats[c.ID]
		fmt.Printf("  %s -> joined=%d already=%d pending=%d banned=%d failed=%d\n", c.label(), s.joined, s.existed, s.pending, s.banned, s.failed)
	}
	return nil
}

// splitSpecs splits a comma-separated spec list into trimmed, non-empty entries.
func splitSpecs(spec string) []string {
	out := []string{}
	for _, part := range strings.Split(spec, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
