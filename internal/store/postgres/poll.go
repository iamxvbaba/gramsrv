package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// This file implements the authoritative poll state (the polls / poll_votes tables, migration 0089):
//   - PollStore: create a poll on send, list getPollVotes;
//   - the private/channel message store's vote, close and read-path enrichment share this file's SQL helpers;
//   - validation and the viewer gate reuse domain.ValidatePollVote / ResolvePollResults, consistent with the memory implementation.
//
// options/correct_options/votes.options JSONB uniformly store a base64(option bytes) array
// (i.e. the natural form of json.Marshal([][]byte)).

// PollStore is the PostgreSQL implementation of store.PollStore.
type PollStore struct {
	db sqlcgen.DBTX
}

// NewPollStore creates a PollStore on top of a pgx pool (or transaction).
func NewPollStore(db sqlcgen.DBTX) *PollStore {
	return &PollStore{db: db}
}

func (s *PollStore) CreatePoll(ctx context.Context, def domain.PollDefinition) error {
	if def.ID == 0 || len(def.Options) == 0 {
		return domain.ErrPollInvalid
	}
	options, err := json.Marshal(def.Options)
	if err != nil {
		return fmt.Errorf("marshal poll options: %w", err)
	}
	correct, err := json.Marshal(def.CorrectOptions)
	if err != nil {
		return fmt.Errorf("marshal poll correct options: %w", err)
	}
	solutionEntities, err := encodeMessageEntities(def.SolutionEntities)
	if err != nil {
		return fmt.Errorf("marshal poll solution entities: %w", err)
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO polls (
  poll_id, creator_user_id, multiple_choice, quiz, public_voters, revoting_disabled, hide_results,
  closed, close_period, close_date, options, correct_options, solution, solution_entities
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		def.ID, def.CreatorUserID, def.MultipleChoice, def.Quiz, def.PublicVoters, def.RevotingDisabled, def.HideResultsUntilClose,
		def.Closed, int32(def.ClosePeriod), int32(def.CloseDate), options, correct, def.Solution, solutionEntities,
	); err != nil {
		return fmt.Errorf("insert poll: %w", err)
	}
	return nil
}

func (s *PollStore) GetPollDefinition(ctx context.Context, pollID int64) (domain.PollDefinition, bool, error) {
	defs, err := loadPollDefinitions(ctx, s.db, []int64{pollID}, false)
	if err != nil {
		return domain.PollDefinition{}, false, err
	}
	def, ok := defs[pollID]
	return def, ok, nil
}

func (s *PollStore) ListPollVotes(ctx context.Context, req domain.PollVotesListRequest) (domain.PollVotesList, error) {
	if req.PollID == 0 || req.Limit <= 0 {
		return domain.PollVotesList{}, domain.ErrPollInvalid
	}
	defs, err := loadPollDefinitions(ctx, s.db, []int64{req.PollID}, false)
	if err != nil {
		return domain.PollVotesList{}, err
	}
	if _, ok := defs[req.PollID]; !ok {
		return domain.PollVotesList{}, domain.ErrPollNotFound
	}
	optionFilter := ""
	args := []any{req.PollID}
	if len(req.Option) > 0 {
		args = append(args, base64.StdEncoding.EncodeToString(req.Option))
		optionFilter = fmt.Sprintf("AND options ? $%d", len(args))
	}
	var count int
	if err := s.db.QueryRow(ctx, fmt.Sprintf(`
SELECT COUNT(*)::int FROM poll_votes WHERE poll_id = $1 %s`, optionFilter), args...).Scan(&count); err != nil {
		return domain.PollVotesList{}, fmt.Errorf("count poll votes: %w", err)
	}
	offsetFilter := ""
	if req.OffsetDate > 0 || req.OffsetUserID > 0 {
		args = append(args, int32(req.OffsetDate), req.OffsetUserID)
		offsetFilter = fmt.Sprintf("AND (vote_date, user_id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, int32(req.Limit+1))
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
SELECT user_id, options::text, vote_date
FROM poll_votes
WHERE poll_id = $1 %s %s
ORDER BY vote_date DESC, user_id DESC
LIMIT $%d`, optionFilter, offsetFilter, len(args)), args...)
	if err != nil {
		return domain.PollVotesList{}, fmt.Errorf("list poll votes: %w", err)
	}
	defer rows.Close()
	out := domain.PollVotesList{Count: count}
	for rows.Next() {
		var userID int64
		var optionsJSON string
		var date int32
		if err := rows.Scan(&userID, &optionsJSON, &date); err != nil {
			return domain.PollVotesList{}, fmt.Errorf("scan poll vote: %w", err)
		}
		options, err := decodePollOptions(optionsJSON)
		if err != nil {
			return domain.PollVotesList{}, err
		}
		out.Votes = append(out.Votes, domain.PollVote{PollID: req.PollID, UserID: userID, Options: options, Date: int(date)})
	}
	if err := rows.Err(); err != nil {
		return domain.PollVotesList{}, fmt.Errorf("iterate poll votes: %w", err)
	}
	if len(out.Votes) > req.Limit {
		out.Votes = out.Votes[:req.Limit]
		out.HasMore = true
	}
	return out, nil
}

// loadPollDefinitions loads authoritative definitions in bulk; locks rows when forUpdate (used by the vote/close transactions).
func loadPollDefinitions(ctx context.Context, db sqlcgen.DBTX, pollIDs []int64, forUpdate bool) (map[int64]domain.PollDefinition, error) {
	if len(pollIDs) == 0 {
		return map[int64]domain.PollDefinition{}, nil
	}
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	rows, err := db.Query(ctx, `
SELECT poll_id, creator_user_id, multiple_choice, quiz, public_voters, revoting_disabled, hide_results,
       closed, close_period, close_date, options::text, correct_options::text, solution, solution_entities::text
FROM polls
WHERE poll_id = ANY($1)`+suffix, pollIDs)
	if err != nil {
		return nil, fmt.Errorf("load poll definitions: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]domain.PollDefinition, len(pollIDs))
	for rows.Next() {
		var def domain.PollDefinition
		var closePeriod, closeDate int32
		var optionsJSON, correctJSON, solutionEntitiesJSON string
		if err := rows.Scan(&def.ID, &def.CreatorUserID, &def.MultipleChoice, &def.Quiz, &def.PublicVoters, &def.RevotingDisabled, &def.HideResultsUntilClose,
			&def.Closed, &closePeriod, &closeDate, &optionsJSON, &correctJSON, &def.Solution, &solutionEntitiesJSON); err != nil {
			return nil, fmt.Errorf("scan poll definition: %w", err)
		}
		def.ClosePeriod = int(closePeriod)
		def.CloseDate = int(closeDate)
		if def.Options, err = decodePollOptions(optionsJSON); err != nil {
			return nil, err
		}
		if def.CorrectOptions, err = decodePollOptions(correctJSON); err != nil {
			return nil, err
		}
		if def.SolutionEntities, err = decodeMessageEntities(solutionEntitiesJSON); err != nil {
			return nil, fmt.Errorf("decode poll solution entities: %w", err)
		}
		out[def.ID] = def
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate poll definitions: %w", err)
	}
	return out, nil
}

// applyPollVote performs one vote inside the caller's transaction (validation + upsert/vote-retraction delete).
// The def passed in must already be locked with FOR UPDATE to avoid concurrent double voting in a quiz.
func applyPollVote(ctx context.Context, db sqlcgen.DBTX, def domain.PollDefinition, userID int64, options [][]byte, date int) error {
	var existingJSON *string
	err := db.QueryRow(ctx, `
SELECT options::text FROM poll_votes WHERE poll_id = $1 AND user_id = $2 FOR UPDATE`, def.ID, userID).Scan(&existingJSON)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load existing poll vote: %w", err)
	}
	var existing [][]byte
	if existingJSON != nil {
		if existing, err = decodePollOptions(*existingJSON); err != nil {
			return err
		}
	}
	if err := domain.ValidatePollVote(def, existing, options, date); err != nil {
		return err
	}
	if len(options) == 0 {
		if _, err := db.Exec(ctx, `DELETE FROM poll_votes WHERE poll_id = $1 AND user_id = $2`, def.ID, userID); err != nil {
			return fmt.Errorf("delete poll vote: %w", err)
		}
		return nil
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal poll vote options: %w", err)
	}
	if _, err := db.Exec(ctx, `
INSERT INTO poll_votes (poll_id, user_id, options, vote_date)
VALUES ($1, $2, $3, $4)
ON CONFLICT (poll_id, user_id)
DO UPDATE SET options = EXCLUDED.options, vote_date = EXCLUDED.vote_date`, def.ID, userID, encoded, int32(date)); err != nil {
		return fmt.Errorf("upsert poll vote: %w", err)
	}
	return nil
}

// closePollAsCreator closes a poll (idempotent); returns ErrPollNotCreator for non-creators.
func closePollAsCreator(ctx context.Context, db sqlcgen.DBTX, def domain.PollDefinition, byUserID int64) error {
	if def.CreatorUserID != byUserID {
		return domain.ErrPollNotCreator
	}
	if _, err := db.Exec(ctx, `UPDATE polls SET closed = TRUE WHERE poll_id = $1`, def.ID); err != nil {
		return fmt.Errorf("close poll: %w", err)
	}
	return nil
}

// pollViewerAggregates computes the viewer's raw aggregates in bulk (counts/total/recent/viewer options).
func pollViewerAggregates(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, pollIDs []int64) (map[int64]domain.PollAggregates, error) {
	out := make(map[int64]domain.PollAggregates, len(pollIDs))
	if len(pollIDs) == 0 {
		return out, nil
	}
	ensure := func(pollID int64) domain.PollAggregates {
		agg, ok := out[pollID]
		if !ok {
			agg = domain.PollAggregates{Counts: make(map[string]int)}
		}
		return agg
	}
	counts, err := db.Query(ctx, `
SELECT poll_id, opt, COUNT(*)::int
FROM poll_votes, LATERAL jsonb_array_elements_text(options) AS opt
WHERE poll_id = ANY($1)
GROUP BY poll_id, opt`, pollIDs)
	if err != nil {
		return nil, fmt.Errorf("aggregate poll counts: %w", err)
	}
	for counts.Next() {
		var pollID int64
		var encoded string
		var count int32
		if err := counts.Scan(&pollID, &encoded, &count); err != nil {
			counts.Close()
			return nil, fmt.Errorf("scan poll count: %w", err)
		}
		option, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			counts.Close()
			return nil, fmt.Errorf("decode poll count option: %w", err)
		}
		agg := ensure(pollID)
		agg.Counts[string(option)] = int(count)
		out[pollID] = agg
	}
	if err := counts.Err(); err != nil {
		counts.Close()
		return nil, fmt.Errorf("iterate poll counts: %w", err)
	}
	counts.Close()

	totals, err := db.Query(ctx, `
SELECT poll_id, COUNT(*)::int FROM poll_votes WHERE poll_id = ANY($1) GROUP BY poll_id`, pollIDs)
	if err != nil {
		return nil, fmt.Errorf("aggregate poll totals: %w", err)
	}
	for totals.Next() {
		var pollID int64
		var total int32
		if err := totals.Scan(&pollID, &total); err != nil {
			totals.Close()
			return nil, fmt.Errorf("scan poll total: %w", err)
		}
		agg := ensure(pollID)
		agg.TotalVoters = int(total)
		out[pollID] = agg
	}
	if err := totals.Err(); err != nil {
		totals.Close()
		return nil, fmt.Errorf("iterate poll totals: %w", err)
	}
	totals.Close()

	recent, err := db.Query(ctx, `
SELECT poll_id, user_id
FROM (
    SELECT poll_id, user_id,
           row_number() OVER (PARTITION BY poll_id ORDER BY vote_date DESC, user_id DESC) AS rn
    FROM poll_votes
    WHERE poll_id = ANY($1)
) ranked
WHERE rn <= $2
ORDER BY poll_id, rn`, pollIDs, int32(domain.MaxPollRecentVoters))
	if err != nil {
		return nil, fmt.Errorf("aggregate poll recent voters: %w", err)
	}
	for recent.Next() {
		var pollID, userID int64
		if err := recent.Scan(&pollID, &userID); err != nil {
			recent.Close()
			return nil, fmt.Errorf("scan poll recent voter: %w", err)
		}
		agg := ensure(pollID)
		agg.RecentVoters = append(agg.RecentVoters, userID)
		out[pollID] = agg
	}
	if err := recent.Err(); err != nil {
		recent.Close()
		return nil, fmt.Errorf("iterate poll recent voters: %w", err)
	}
	recent.Close()

	if viewerUserID != 0 {
		viewer, err := db.Query(ctx, `
SELECT poll_id, options::text FROM poll_votes WHERE poll_id = ANY($1) AND user_id = $2`, pollIDs, viewerUserID)
		if err != nil {
			return nil, fmt.Errorf("load viewer poll votes: %w", err)
		}
		for viewer.Next() {
			var pollID int64
			var optionsJSON string
			if err := viewer.Scan(&pollID, &optionsJSON); err != nil {
				viewer.Close()
				return nil, fmt.Errorf("scan viewer poll vote: %w", err)
			}
			options, err := decodePollOptions(optionsJSON)
			if err != nil {
				viewer.Close()
				return nil, err
			}
			agg := ensure(pollID)
			agg.ViewerOptions = options
			out[pollID] = agg
		}
		if err := viewer.Err(); err != nil {
			viewer.Close()
			return nil, fmt.Errorf("iterate viewer poll votes: %w", err)
		}
		viewer.Close()
	}
	return out, nil
}

// pollMediaRef marks a poll media to enrich together with its viewer (box owner for private chats, request viewer for channels).
type pollMediaRef struct {
	media  *domain.MessageMedia
	viewer int64
}

// enrichPollMediaRefs is the postgres read paths' unified poll enrichment:
// viewer options are loaded grouped by (viewer), while counts/total/recent/definition are queried once.
func enrichPollMediaRefs(ctx context.Context, db sqlcgen.DBTX, refs []pollMediaRef) error {
	if len(refs) == 0 {
		return nil
	}
	now := int(time.Now().Unix())
	pollIDSet := make(map[int64]struct{}, len(refs))
	viewers := make(map[int64][]pollMediaRef)
	for _, ref := range refs {
		if ref.media == nil || ref.media.Kind != domain.MessageMediaKindPoll || ref.media.Poll == nil || ref.media.Poll.ID == 0 {
			continue
		}
		pollIDSet[ref.media.Poll.ID] = struct{}{}
		viewers[ref.viewer] = append(viewers[ref.viewer], ref)
	}
	if len(pollIDSet) == 0 {
		return nil
	}
	pollIDs := make([]int64, 0, len(pollIDSet))
	for id := range pollIDSet {
		pollIDs = append(pollIDs, id)
	}
	defs, err := loadPollDefinitions(ctx, db, pollIDs, false)
	if err != nil {
		return err
	}
	for viewer, group := range viewers {
		aggs, err := pollViewerAggregates(ctx, db, viewer, pollIDs)
		if err != nil {
			return err
		}
		for _, ref := range group {
			def, ok := defs[ref.media.Poll.ID]
			if !ok {
				continue
			}
			agg, ok := aggs[ref.media.Poll.ID]
			if !ok {
				agg = domain.PollAggregates{Counts: map[string]int{}}
			}
			results := domain.ResolvePollResults(def, agg, viewer, now)
			domain.ApplyPollState(ref.media.Poll, def, results, now)
		}
	}
	return nil
}

func decodePollOptions(raw string) ([][]byte, error) {
	if raw == "" || raw == "[]" || raw == "null" {
		return nil, nil
	}
	var out [][]byte
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode poll options: %w", err)
	}
	return out, nil
}
