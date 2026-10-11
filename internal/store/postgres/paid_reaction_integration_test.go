package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"telesrv/deploy"
	"telesrv/internal/domain"
)

func postgresPaidReactionRandomID(date int, low uint32) int64 {
	return int64(uint64(uint32(date))<<32 | uint64(low))
}

func TestPaidReactionAtomicLedgerMigrationCreditsLegacyAggregatesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	downSQL, err := deploy.Migrations.ReadFile("migrations/0180_paid_reaction_atomic_ledger.down.sql")
	if err != nil {
		t.Fatalf("read 0178 down: %v", err)
	}
	upSQL, err := deploy.Migrations.ReadFile("migrations/0180_paid_reaction_atomic_ledger.up.sql")
	if err != nil {
		t.Fatalf("read 0178 up: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration test: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("apply 0178 down fixture: %v", err)
	}
	const (
		channelID = int64(9_183_001)
		reactor1  = int64(9_183_101)
		reactor2  = int64(9_183_102)
	)
	if _, err := tx.Exec(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,7)`, channelID); err != nil {
		t.Fatalf("seed legacy channel balance: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_message_paid_reactions(channel_id,message_id,reactor_user_id,stars,anonymous,reaction_date)
VALUES ($1,1,$2,10,false,1700000001),
       ($1,2,$2,20,false,1700000002),
       ($1,1,$3,5,true,1700000003)`, channelID, reactor1, reactor2); err != nil {
		t.Fatalf("seed legacy paid reactions: %v", err)
	}
	if _, err := tx.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("apply 0178 up: %v", err)
	}
	var cutoverAt, rejectThrough int
	if err := tx.QueryRow(ctx, `SELECT cutover_at,reject_random_id_through
FROM channel_paid_reaction_cutover WHERE singleton`).Scan(&cutoverAt, &rejectThrough); err != nil {
		t.Fatalf("read legacy cutover fence: %v", err)
	}
	if cutoverAt <= 0 || rejectThrough != cutoverAt+domain.PaidReactionRandomIDMaxFutureSeconds {
		t.Fatalf("legacy cutover=%d reject-through=%d", cutoverAt, rejectThrough)
	}
	var balance int64
	if err := tx.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&balance); err != nil || balance != 42 {
		t.Fatalf("legacy credited balance=%d err=%v, want 42", balance, err)
	}
	rows, err := tx.Query(ctx, `
SELECT actor_user_id,amount FROM channel_stars_transactions
WHERE channel_id=$1 AND reason='reaction' ORDER BY actor_user_id`, channelID)
	if err != nil {
		t.Fatalf("list legacy reaction credits: %v", err)
	}
	defer rows.Close()
	type credit struct{ actor, amount int64 }
	var credits []credit
	for rows.Next() {
		var item credit
		if err := rows.Scan(&item.actor, &item.amount); err != nil {
			t.Fatalf("scan legacy credit: %v", err)
		}
		credits = append(credits, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list legacy reaction credits: %v", err)
	}
	if len(credits) != 2 || credits[0] != (credit{reactor1, 30}) || credits[1] != (credit{reactor2, 5}) {
		t.Fatalf("legacy credits=%+v", credits)
	}
	var auditRows, commandRows int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_legacy_credits WHERE channel_id=$1`, channelID).Scan(&auditRows); err != nil || auditRows != 2 {
		t.Fatalf("legacy audit rows=%d err=%v", auditRows, err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE channel_id=$1`, channelID).Scan(&commandRows); err != nil || commandRows != 0 {
		t.Fatalf("legacy command receipts=%d err=%v, want none", commandRows, err)
	}
}

func TestPaidReactionAtomicLedgerDownRefusesPostUpgradeActivityPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var transactionID int64
	if err := pool.QueryRow(ctx, `INSERT INTO channel_stars_transactions
    (channel_id,actor_user_id,amount,reason,peer_type,peer_id,date)
VALUES(9183991,9183992,1,'reaction','user',9183992,1700000000)
RETURNING id`).Scan(&transactionID); err != nil {
		t.Fatalf("seed post-upgrade paid reaction credit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM channel_stars_transactions WHERE id=$1`, transactionID)
	})
	downSQL, err := deploy.Migrations.ReadFile("migrations/0180_paid_reaction_atomic_ledger.down.sql")
	if err != nil {
		t.Fatalf("read 0178 down: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, downErr := tx.Exec(ctx, string(downSQL))
	_ = tx.Rollback(ctx)
	if downErr == nil || !strings.Contains(downErr.Error(), "cannot downgrade paid reaction atomic ledger after post-upgrade activity") {
		t.Fatalf("0178 down error=%v, want post-upgrade activity guard", downErr)
	}
	var commandsTable string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(to_regclass('public.channel_paid_reaction_commands')::text,'')`).Scan(&commandsTable); err != nil {
		t.Fatalf("check paid reaction command table: %v", err)
	}
	if commandsTable == "" {
		t.Fatal("rejected 0178 down discarded the paid reaction command table")
	}
}

func TestChannelPaidReactionPostgresOwnerSendAsAndOrderedCrossSettlement(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 931, Phone: "+1779" + suffix + "41", FirstName: "CrossOwner"})
	if err != nil {
		t.Fatalf("create cross owner: %v", err)
	}
	admin, err := users.Create(ctx, domain.User{AccessHash: 932, Phone: "+1779" + suffix + "42", FirstName: "CrossAdmin"})
	if err != nil {
		t.Fatalf("create cross admin: %v", err)
	}
	userIDs := []int64{owner.ID, admin.ID}
	channels := NewChannelStore(pool)
	channelIDs := make([]int64, 0, 3)
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, `DELETE FROM channel_paid_reaction_commands WHERE channel_id=ANY($1::bigint[])`, channelIDs)
			_, _ = pool.Exec(ctx, `DELETE FROM channel_message_paid_reactions WHERE channel_id=ANY($1::bigint[])`, channelIDs)
			_, _ = pool.Exec(ctx, `DELETE FROM channel_stars_transactions WHERE channel_id=ANY($1::bigint[])`, channelIDs)
			_, _ = pool.Exec(ctx, `DELETE FROM channel_stars_balances WHERE channel_id=ANY($1::bigint[])`, channelIDs)
			_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id=ANY($1::bigint[])`, channelIDs)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM stars_transactions WHERE user_id=ANY($1::bigint[])`, userIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM stars_balances WHERE user_id=ANY($1::bigint[])`, userIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, userIDs)
	})
	type paidChannel struct {
		channel domain.Channel
		msgID   int
	}
	createPaid := func(creator int64, members []int64, title string, date int) paidChannel {
		t.Helper()
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
			CreatorUserID: creator, Title: title, Broadcast: true, MemberUserIDs: members, Date: date,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
		if _, err := channels.SetAvailableReactions(ctx, creator, created.Channel.ID, domain.ChannelReactionPolicy{
			Type: domain.ChannelReactionPolicyAll, PaidEnabled: true,
		}); err != nil {
			t.Fatalf("enable %s paid reactions: %v", title, err)
		}
		sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: creator, ChannelID: created.Channel.ID, RandomID: int64(date), Message: title, Date: date + 1,
		})
		if err != nil {
			t.Fatalf("send %s post: %v", title, err)
		}
		return paidChannel{channel: created.Channel, msgID: sent.Message.ID}
	}
	a := createPaid(owner.ID, nil, "Cross A "+suffix, 1700010000)
	b := createPaid(owner.ID, []int64{admin.ID}, "Cross B "+suffix, 1700010010)
	c := createPaid(admin.ID, nil, "Cross C "+suffix, 1700010020)
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID: owner.ID, ChannelID: b.channel.ID, MemberID: admin.ID,
		AdminRights: domain.ChannelAdminRights{PostMessages: true}, Date: 1700010030,
	}); err != nil {
		t.Fatalf("promote post-messages admin: %v", err)
	}
	peerA := domain.Peer{Type: domain.PeerTypeChannel, ID: a.channel.ID}
	peerB := domain.Peer{Type: domain.PeerTypeChannel, ID: b.channel.ID}
	reqs := []domain.SendChannelPaidReactionRequest{
		{UserID: owner.ID, ChannelID: a.channel.ID, MessageID: a.msgID, Stars: 1,
			RandomID: postgresPaidReactionRandomID(1700010040, 91001), Date: 1700010040,
			Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peerB}, DisplayPeer: peerB},
		{UserID: owner.ID, ChannelID: b.channel.ID, MessageID: b.msgID, Stars: 1,
			RandomID: postgresPaidReactionRandomID(1700010041, 91002), Date: 1700010041,
			Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peerA}, DisplayPeer: peerA},
	}
	settleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	errs := make(chan error, len(reqs))
	var wg sync.WaitGroup
	for _, req := range reqs {
		req := req
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := channels.AddChannelMessagePaidReaction(settleCtx, req)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ordered cross settlement: %v", err)
		}
	}
	var balance, grants, debits int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, owner.ID).Scan(&balance); err != nil || balance != domain.DefaultStarsStartingGrant-2 {
		t.Fatalf("cross payer balance=%d err=%v", balance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE user_id=$1 AND reason='grant'`, owner.ID).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("concurrent lazy grants=%d err=%v, want one", grants, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE user_id=$1 AND reason='reaction' AND amount=-1`, owner.ID).Scan(&debits); err != nil || debits != 2 {
		t.Fatalf("cross payer debits=%d err=%v", debits, err)
	}
	adminPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: b.channel.ID}
	adminReq := domain.SendChannelPaidReactionRequest{
		UserID: admin.ID, ChannelID: c.channel.ID, MessageID: c.msgID, Stars: 1,
		RandomID: postgresPaidReactionRandomID(1700010050, 91003), Date: 1700010050,
		Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &adminPeer}, DisplayPeer: adminPeer,
	}
	if _, err := channels.AddChannelMessagePaidReaction(ctx, adminReq); !errors.Is(err, domain.ErrPaidReactionSendAsPeerInvalid) {
		t.Fatalf("post-messages admin send-as err=%v", err)
	}
	var adminCommandRows, adminBalanceRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE payer_user_id=$1 AND random_id=$2`, admin.ID, adminReq.RandomID).Scan(&adminCommandRows); err != nil || adminCommandRows != 0 {
		t.Fatalf("rejected admin command rows=%d err=%v", adminCommandRows, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_balances WHERE user_id=$1`, admin.ID).Scan(&adminBalanceRows); err != nil || adminBalanceRows != 0 {
		t.Fatalf("rejected admin balance rows=%d err=%v", adminBalanceRows, err)
	}
}

// TestChannelPaidReactionPostgres 回归迁移 0010：广播频道付费 reaction 对真实 PG 的累计 +
// 聚合（总星数 / viewer 自身 / top reactors 降序 / 同 reactor 多次累加）。
func TestChannelPaidReactionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 521, Phone: "+1778" + suffix + "41", FirstName: "PaidRxOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 522, Phone: "+1778" + suffix + "42", FirstName: "PaidRxMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	poor, err := users.Create(ctx, domain.User{AccessHash: 523, Phone: "+1778" + suffix + "43", FirstName: "PaidRxPoor"})
	if err != nil {
		t.Fatalf("create poor member: %v", err)
	}
	userIDs := []int64{owner.ID, member.ID, poor.ID}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channel_paid_reaction_commands WHERE channel_id = $1", channelID)
			_, _ = pool.Exec(ctx, "DELETE FROM channel_message_paid_reactions WHERE channel_id = $1", channelID)
			_, _ = pool.Exec(ctx, "DELETE FROM channel_stars_transactions WHERE channel_id = $1", channelID)
			_, _ = pool.Exec(ctx, "DELETE FROM channel_stars_balances WHERE channel_id = $1", channelID)
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM stars_transactions WHERE user_id = ANY($1::bigint[])", userIDs)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_balances WHERE user_id = ANY($1::bigint[])", userIDs)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", userIDs)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Paid Reaction " + suffix,
		Broadcast:     true,
		MemberUserIDs: []int64{member.ID, poor.ID},
		Date:          1700000400,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.SetAvailableReactions(ctx, owner.ID, channelID, domain.ChannelReactionPolicy{
		Type: domain.ChannelReactionPolicyAll, PaidEnabled: true,
	}); err != nil {
		t.Fatalf("enable paid reactions: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9401,
		Message:   "paid reaction target",
		Date:      1700000401,
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	msgID := sent.Message.ID
	channelBeforePaid, err := channels.GetChannelByID(ctx, channelID)
	if err != nil {
		t.Fatalf("load channel PTS before paid reaction: %v", err)
	}
	ptsBeforePaid := channelBeforePaid.Pts
	var originalCutover, originalRejectThrough int
	if err := pool.QueryRow(ctx, `SELECT cutover_at,reject_random_id_through
FROM channel_paid_reaction_cutover WHERE singleton`).Scan(&originalCutover, &originalRejectThrough); err != nil {
		t.Fatalf("load fresh paid reaction cutover: %v", err)
	}
	if originalRejectThrough != 0 {
		t.Fatalf("fresh database paid reaction cutover rejects through %d, want 0", originalRejectThrough)
	}
	restoredCutover := false
	t.Cleanup(func() {
		if !restoredCutover {
			_, _ = pool.Exec(ctx, `UPDATE channel_paid_reaction_cutover
SET cutover_at=$1,reject_random_id_through=$2 WHERE singleton`, originalCutover, originalRejectThrough)
		}
	})
	const cutoverFenceDate = 1700000402
	if _, err := pool.Exec(ctx, `UPDATE channel_paid_reaction_cutover
SET cutover_at=$1,reject_random_id_through=$2 WHERE singleton`,
		cutoverFenceDate-domain.PaidReactionRandomIDMaxFutureSeconds, cutoverFenceDate); err != nil {
		t.Fatalf("enable paid reaction cutover fixture: %v", err)
	}
	fencedRandomID := postgresPaidReactionRandomID(cutoverFenceDate, 81000)
	if _, err := channels.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: msgID, Stars: 100,
		RandomID: fencedRandomID, Date: cutoverFenceDate,
	}); !errors.Is(err, domain.ErrPaidReactionCutoverAmbiguous) {
		t.Fatalf("legacy cutover random id err=%v", err)
	}
	var fencedCommands, fencedAggregates int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM channel_paid_reaction_commands
WHERE payer_user_id=$1 AND random_id=$2`, owner.ID, fencedRandomID).Scan(&fencedCommands); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM channel_message_paid_reactions
WHERE channel_id=$1 AND message_id=$2`, channelID, msgID).Scan(&fencedAggregates); err != nil {
		t.Fatal(err)
	}
	if fencedCommands != 0 || fencedAggregates != 0 {
		t.Fatalf("cutover rejection left command/aggregate=%d/%d", fencedCommands, fencedAggregates)
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_paid_reaction_cutover
SET cutover_at=$1,reject_random_id_through=$2 WHERE singleton`, originalCutover, originalRejectThrough); err != nil {
		t.Fatalf("restore paid reaction cutover fixture: %v", err)
	}
	restoredCutover = true
	var freshBalanceRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_balances WHERE user_id=$1`, owner.ID).Scan(&freshBalanceRows); err != nil || freshBalanceRows != 0 {
		t.Fatalf("fresh owner balance rows=%d err=%v, want none before atomic first-use grant", freshBalanceRows, err)
	}

	// owner 投 100。
	res, err := channels.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: msgID, Stars: 100, RandomID: postgresPaidReactionRandomID(1700000402, 81001), Date: 1700000402,
	})
	if err != nil {
		t.Fatalf("owner paid reaction: %v", err)
	}
	if res.Paid.TotalStars != 100 || res.Paid.MyStars != 100 {
		t.Fatalf("after owner 100 = total %d my %d, want 100/100", res.Paid.TotalStars, res.Paid.MyStars)
	}
	if res.PayerBalance.Balance != domain.DefaultStarsStartingGrant-100 || res.ChannelBalance != 100 {
		t.Fatalf("first atomic balances payer/channel=%d/%d", res.PayerBalance.Balance, res.ChannelBalance)
	}

	// member 投 250 → 总 350，member 视角 my=250，top reactors 降序 member(250)/owner(100)。
	memberReq := domain.SendChannelPaidReactionRequest{
		UserID: member.ID, ChannelID: channelID, MessageID: msgID, Stars: 250, RandomID: postgresPaidReactionRandomID(1700000403, 81002), Date: 1700000403,
	}
	res, err = channels.AddChannelMessagePaidReaction(ctx, memberReq)
	if err != nil {
		t.Fatalf("member paid reaction: %v", err)
	}
	if res.Paid.TotalStars != 350 || res.Paid.MyStars != 250 {
		t.Fatalf("after member 250 = total %d my %d, want 350/250", res.Paid.TotalStars, res.Paid.MyStars)
	}
	if len(res.Paid.TopReactors) != 2 || res.Paid.TopReactors[0].Stars != 250 || !res.Paid.TopReactors[0].My || res.Paid.TopReactors[1].Stars != 100 {
		t.Fatalf("top reactors = %+v, want member(250,My)/owner(100)", res.Paid.TopReactors)
	}

	// owner 再投 50 → 累加到 150，总 400。
	ownerAgain := domain.SendChannelPaidReactionRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: msgID, Stars: 50, RandomID: postgresPaidReactionRandomID(1700000404, 81003), Date: 1700000404,
	}
	res, err = channels.AddChannelMessagePaidReaction(ctx, ownerAgain)
	if err != nil {
		t.Fatalf("owner re-invest: %v", err)
	}
	if res.Paid.TotalStars != 400 || res.Paid.MyStars != 150 {
		t.Fatalf("after owner +50 = total %d my %d, want 400/150 (accumulated)", res.Paid.TotalStars, res.Paid.MyStars)
	}
	ownerAgain.Date++
	replay, err := channels.AddChannelMessagePaidReaction(ctx, ownerAgain)
	if err != nil || !replay.Duplicate || replay.Paid.TotalStars != 400 || replay.PayerBalance.Balance != 850 || replay.ChannelBalance != 400 {
		t.Fatalf("exact replay = %+v err=%v", replay, err)
	}
	conflict := ownerAgain
	conflict.Stars++
	if _, err := channels.AddChannelMessagePaidReaction(ctx, conflict); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed random_id payload err = %v", err)
	}
	var (
		channelBalance int64
		commandCount   int
		payerDebits    int
		channelCredits int
	)
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&channelBalance); err != nil || channelBalance != 400 {
		t.Fatalf("channel paid reaction balance=%d err=%v", channelBalance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE channel_id=$1 AND completed`, channelID).Scan(&commandCount); err != nil || commandCount != 3 {
		t.Fatalf("completed command count=%d err=%v", commandCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE user_id=ANY($1::bigint[]) AND reason='reaction' AND amount<0`, []int64{owner.ID, member.ID}).Scan(&payerDebits); err != nil || payerDebits != 3 {
		t.Fatalf("payer reaction debits=%d err=%v", payerDebits, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_stars_transactions WHERE channel_id=$1 AND reason='reaction' AND amount>0`, channelID).Scan(&channelCredits); err != nil || channelCredits != 3 {
		t.Fatalf("channel reaction credits=%d err=%v", channelCredits, err)
	}

	// 关键回归（读路径修复）：fresh 读回该消息必须携带付费 reaction（不止实时推送）。
	// owner 视角：Paid.TotalStars=400, MyStars=150。
	read, err := channels.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
		UserID: owner.ID, ChannelID: channelID, IDs: []int{msgID},
	})
	if err != nil {
		t.Fatalf("get message reactions: %v", err)
	}
	if len(read.Messages) != 1 || read.Messages[0].Reactions == nil || read.Messages[0].Reactions.Paid == nil {
		t.Fatalf("fresh read messages=%d reactions/paid missing: %+v", len(read.Messages), read.Messages)
	}
	paid := read.Messages[0].Reactions.Paid
	if paid.TotalStars != 400 || paid.MyStars != 150 {
		t.Fatalf("fresh read paid = total %d my %d, want 400/150 (读路径须回显付费 reaction)", paid.TotalStars, paid.MyStars)
	}
	// member 视角读同一条：MyStars=250、TopReactors 含 member 自己带 My。
	readMember, err := channels.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
		UserID: member.ID, ChannelID: channelID, IDs: []int{msgID},
	})
	if err != nil {
		t.Fatalf("get message reactions (member): %v", err)
	}
	mp := readMember.Messages[0].Reactions.Paid
	if mp == nil || mp.TotalStars != 400 || mp.MyStars != 250 {
		t.Fatalf("member fresh read paid = %+v, want total 400 my 250", mp)
	}

	// Concurrent transport retries of one command must converge on exactly one
	// debit, one channel credit and one aggregate increment.
	concurrentReq := domain.SendChannelPaidReactionRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: msgID, Stars: 7, RandomID: postgresPaidReactionRandomID(1700000407, 81006), Date: 1700000407,
	}
	type concurrentResult struct {
		result domain.ChannelMessagePaidReactionResult
		err    error
	}
	const attempts = 8
	results := make(chan concurrentResult, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := channels.AddChannelMessagePaidReaction(ctx, concurrentReq)
			results <- concurrentResult{result: got, err: err}
		}()
	}
	wg.Wait()
	close(results)
	duplicates := 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("concurrent exact replay: %v", got.err)
		}
		if got.result.Duplicate {
			duplicates++
		}
	}
	if duplicates != attempts-1 {
		t.Fatalf("concurrent duplicates=%d, want %d", duplicates, attempts-1)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&channelBalance); err != nil || channelBalance != 407 {
		t.Fatalf("channel balance after concurrent replay=%d err=%v", channelBalance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE channel_id=$1 AND completed`, channelID).Scan(&commandCount); err != nil || commandCount != 4 {
		t.Fatalf("command count after concurrent replay=%d err=%v", commandCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE user_id=$1 AND reason='reaction' AND amount<0`, owner.ID).Scan(&payerDebits); err != nil || payerDebits != 3 {
		t.Fatalf("owner debits after concurrent replay=%d err=%v", payerDebits, err)
	}

	// Invalid target validation happens before any ledger mutation; the command
	// placeholder rolls back with the transaction.
	invalidReq := domain.SendChannelPaidReactionRequest{
		UserID: member.ID, ChannelID: channelID, MessageID: msgID + 999, Stars: 10, RandomID: postgresPaidReactionRandomID(1700000408, 81007), Date: 1700000408,
	}
	if _, err := channels.AddChannelMessagePaidReaction(ctx, invalidReq); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("invalid target err=%v", err)
	}
	var failedCommands int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE payer_user_id=$1 AND random_id=$2`, invalidReq.UserID, invalidReq.RandomID).Scan(&failedCommands); err != nil || failedCommands != 0 {
		t.Fatalf("invalid target command rows=%d err=%v", failedCommands, err)
	}

	// A real but insufficient balance must roll back the placeholder and leave
	// both the aggregate and channel ledger untouched.
	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances(user_id,balance,granted) VALUES($1,5,true)`, poor.ID); err != nil {
		t.Fatalf("seed poor balance: %v", err)
	}
	insufficientReq := domain.SendChannelPaidReactionRequest{
		UserID: poor.ID, ChannelID: channelID, MessageID: msgID, Stars: 10, RandomID: postgresPaidReactionRandomID(1700000409, 81008), Date: 1700000409,
	}
	if _, err := channels.AddChannelMessagePaidReaction(ctx, insufficientReq); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient balance err=%v", err)
	}
	var poorBalance int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, poor.ID).Scan(&poorBalance); err != nil || poorBalance != 5 {
		t.Fatalf("poor balance after rollback=%d err=%v", poorBalance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE payer_user_id=$1 AND random_id=$2`, insufficientReq.UserID, insufficientReq.RandomID).Scan(&failedCommands); err != nil || failedCommands != 0 {
		t.Fatalf("insufficient command rows=%d err=%v", failedCommands, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&channelBalance); err != nil || channelBalance != 407 {
		t.Fatalf("channel balance after failed commands=%d err=%v", channelBalance, err)
	}
	expiredReq := domain.SendChannelPaidReactionRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: msgID, Stars: 1,
		RandomID: postgresPaidReactionRandomID(1700100000-domain.PaidReactionRandomIDMaxAgeSeconds-1, 81009), Date: 1700100000,
	}
	if _, err := channels.AddChannelMessagePaidReaction(ctx, expiredReq); !errors.Is(err, domain.ErrPaidReactionRandomIDExpired) {
		t.Fatalf("expired random id err=%v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE payer_user_id=$1 AND random_id=$2`, expiredReq.UserID, expiredReq.RandomID).Scan(&failedCommands); err != nil || failedCommands != 0 {
		t.Fatalf("expired command rows=%d err=%v", failedCommands, err)
	}
	channelAfterPaid, err := channels.GetChannelByID(ctx, channelID)
	if err != nil || channelAfterPaid.Pts != ptsBeforePaid {
		t.Fatalf("paid reaction consumed PTS: before=%d after=%d err=%v", ptsBeforePaid, channelAfterPaid.Pts, err)
	}

	if _, err := channels.SetAvailableReactions(ctx, owner.ID, channelID, domain.ChannelReactionPolicy{Type: domain.ChannelReactionPolicyAll}); err != nil {
		t.Fatalf("disable paid reactions: %v", err)
	}
	if _, err := channels.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: msgID, Stars: 1, RandomID: postgresPaidReactionRandomID(1700000406, 81005), Date: 1700000406,
	}); !errors.Is(err, domain.ErrReactionInvalid) {
		t.Fatalf("disabled paid reaction err=%v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_paid_reaction_commands WHERE payer_user_id=$1 AND random_id=$2`, owner.ID, postgresPaidReactionRandomID(1700000406, 81005)).Scan(&failedCommands); err != nil || failedCommands != 0 {
		t.Fatalf("disabled command rows=%d err=%v", failedCommands, err)
	}
	// A completed exact command remains replayable after the owner disables
	// new paid reactions; it must not debit or fan out again.
	if replay, err := channels.AddChannelMessagePaidReaction(ctx, ownerAgain); err != nil || !replay.Duplicate ||
		replay.ChannelBalance != 400 || replay.Paid.TotalStars != 400 || replay.PayerBalance.Balance != 843 {
		t.Fatalf("replay after policy disable=%+v err=%v", replay, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_messages SET deleted=true WHERE channel_id=$1 AND id=$2`, channelID, msgID); err != nil {
		t.Fatalf("delete paid target before exact replay: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_members SET status='kicked' WHERE channel_id=$1 AND user_id=$2`, channelID, member.ID); err != nil {
		t.Fatalf("kick paid payer before exact replay: %v", err)
	}
	if replay, err := channels.AddChannelMessagePaidReaction(ctx, memberReq); err != nil || !replay.Duplicate ||
		replay.ChannelBalance != 350 || replay.Paid.TotalStars != 350 || replay.PayerBalance.Balance != 750 {
		t.Fatalf("replay after target delete/payer kick=%+v err=%v", replay, err)
	}
	if replay, found, err := channels.ReplayChannelMessagePaidReaction(ctx, memberReq); err != nil || !found ||
		!replay.Duplicate || replay.Paid.TotalStars != 350 || replay.PayerBalance.Balance != 750 {
		t.Fatalf("preflight replay after target delete/payer kick=%+v found=%v err=%v", replay, found, err)
	}

	// 非法星数被拒。
	if _, err := channels.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: msgID, Stars: 0, RandomID: postgresPaidReactionRandomID(1700000405, 81004), Date: 1700000405,
	}); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("zero-stars err = %v, want ErrChannelInvalid", err)
	}
}
