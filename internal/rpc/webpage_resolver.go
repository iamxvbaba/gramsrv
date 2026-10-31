package rpc

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Link-preview async backfill (P3): the pending placeholder attached at send time (see
// webpage_url_extract.go) is resolved out-of-band here into a resolved card and swapped
// in place via messages.EditMessage's WebPageResolve mode - only the media is replaced,
// an updateWebPage is generated (associated with the placeholder by webPage id, without
// marking "edited" and without doubling the fan-out). Delivery and difference reuse the
// existing dispatch_outbox + generic message event path.

const (
	webPageResolveConcurrency = 16
	webPageResolveTimeout     = 30 * time.Second
	// webPagePendingLifetime is the window the client waits before re-fetching a pending
	// message. TDesktop and DrKLO both interpret webPagePending.date as an absolute
	// deadline rather than a processing start time; the window must be strictly greater
	// than the resolver's maximum execution time, so a normal slow resolve is not marked
	// as failed by the client prematurely.
	webPagePendingLifetime = 2 * time.Minute
)

type webPageResolveJob struct {
	senderID       int64
	peer           domain.Peer
	msgID          int
	expectedID     int64
	url            string
	invertMedia    bool
	forceLarge     bool
	forceSmall     bool
	timeout        time.Duration
	emptyOnFailure bool
}

type webPageResolveKey struct {
	peerType   domain.PeerType
	scopeID    int64
	msgID      int
	expectedID int64
}

// maybeEnqueueWebPageResolve asynchronously enqueues a resolve when a just-sent message
// (private or channel) carries a pending link-preview placeholder. msgID is the message
// id from the sender's perspective and media is that message's media.
func (r *Router) maybeEnqueueWebPageResolve(senderID int64, peer domain.Peer, msgID int, media *domain.MessageMedia) {
	if msgID == 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
		return
	}
	if media == nil || media.WebPage == nil || media.WebPage.State != domain.MessageWebPageStatePending {
		return
	}
	r.enqueueWebPageResolve(webPageResolveJob{
		senderID:    senderID,
		peer:        peer,
		msgID:       msgID,
		expectedID:  media.WebPage.ID,
		url:         media.WebPage.URL,
		invertMedia: media.InvertMedia,
		forceLarge:  media.WebPage.ForceLargeMedia,
		forceSmall:  media.WebPage.ForceSmallMedia,
	})
}

// enqueueWebPageResolve asynchronously processes one resolve task within the concurrency
// limit; when full the task is dropped (the message stays pending, and the fallback is
// the client reopening the session and reading via getHistory the placeholder that is
// still pending at that point - acceptable for WIP, with cleanup added in P4).
func (r *Router) enqueueWebPageResolve(job webPageResolveJob) {
	if r.webPageResolveSem == nil {
		return
	}
	select {
	case r.webPageResolveSem <- struct{}{}:
	default:
		r.log.Debug("web page resolve dropped: at capacity", zap.Int64("expected_id", job.expectedID))
		return
	}
	go func() {
		defer func() { <-r.webPageResolveSem }()
		timeout := job.timeout
		if timeout <= 0 {
			timeout = webPageResolveTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := r.resolvePendingWebPage(ctx, job); err != nil {
			r.log.Debug("web page resolve failed",
				zap.Int64("sender_id", job.senderID),
				zap.Int("msg_id", job.msgID),
				zap.Error(err))
		}
	}()
}

// resolvePendingWebPage resolves the link and swaps the message's pending placeholder
// into a resolved card in place (synchronous, testable). A transient fetch failure
// returns an error (the message stays pending, without poisoning a popular link); an
// idempotent conflict (message deleted/edited/already resolved) silently succeeds via
// ErrMessageNotModified. Both done and empty states are swapped (empty stops the client
// spinner).
func (r *Router) resolvePendingWebPage(ctx context.Context, job webPageResolveJob) error {
	if r.deps.Files == nil {
		return nil
	}
	resolved, err := r.deps.Files.ResolveWebPage(ctx, job.url)
	if err != nil {
		if !job.emptyOnFailure {
			return err
		}
		resolved = emptyWebPageForResolveJob(job)
		r.log.Info("web page pending finalized as empty",
			zap.Int64("sender_id", job.senderID),
			zap.String("peer_type", string(job.peer.Type)),
			zap.Int64("peer_id", job.peer.ID),
			zap.Int("msg_id", job.msgID),
			zap.Int64("expected_id", job.expectedID),
			zap.Error(err))
	}
	// Preserve the wrapper preferences (force large/small) from the placeholder at send
	// time; they are not in the fetch result.
	if job.expectedID != 0 {
		resolved.ID = job.expectedID
	}
	resolved.ForceLargeMedia = job.forceLarge
	resolved.ForceSmallMedia = job.forceSmall
	media := &domain.MessageMedia{
		Kind:        domain.MessageMediaKindWebPage,
		InvertMedia: job.invertMedia,
		WebPage:     &resolved,
	}
	switch job.peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil
		}
		res, err := r.deps.Channels.EditMessage(ctx, job.senderID, domain.EditChannelMessageRequest{
			UserID:            job.senderID,
			ChannelID:         job.peer.ID,
			ID:                job.msgID,
			Media:             media,
			WebPageResolve:    true,
			ExpectedWebPageID: job.expectedID,
		})
		if errors.Is(err, domain.ErrMessageNotModified) {
			return nil
		}
		if err != nil {
			return err
		}
		// Reuse the channel edit fan-out (projecting updateChannelWebPage) to reach
		// members; difference goes through the generic replay.
		r.enqueueChannelEditMessageFanout(ctx, job.senderID, res)
		return nil
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil
		}
		_, err = r.deps.Messages.EditMessage(ctx, job.senderID, domain.EditMessageRequest{
			OwnerUserID:       job.senderID,
			Peer:              job.peer,
			ID:                job.msgID,
			Media:             media,
			WebPageResolve:    true,
			ExpectedWebPageID: job.expectedID,
		})
		if errors.Is(err, domain.ErrMessageNotModified) {
			return nil
		}
		return err
	default:
		return nil
	}
}

func emptyWebPageForResolveJob(job webPageResolveJob) domain.MessageWebPage {
	url := job.url
	id := job.expectedID
	if normalized, ok := domain.NormalizeWebPageURL(url); ok {
		url = normalized
		if id == 0 {
			id = domain.WebPageURLHash(normalized)
		}
	}
	return domain.MessageWebPage{State: domain.MessageWebPageStateEmpty, ID: id, URL: url}
}

func (r *Router) maybeEnqueueExpiredPrivateWebPageResolves(messages []domain.Message) {
	now := int(r.clock.Now().Unix())
	seen := make(map[webPageResolveKey]struct{})
	for _, msg := range messages {
		job, ok := expiredPrivateWebPageResolveJob(msg, now)
		if !ok {
			continue
		}
		key := webPageResolveKey{peerType: domain.PeerTypeUser, scopeID: msg.OwnerUserID, msgID: msg.ID, expectedID: job.expectedID}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		r.enqueueWebPageResolve(job)
	}
}

func (r *Router) maybeEnqueueExpiredChannelWebPageResolves(userID int64, messages []domain.ChannelMessage) {
	now := int(r.clock.Now().Unix())
	seen := make(map[webPageResolveKey]struct{})
	for _, msg := range messages {
		job, ok := expiredChannelWebPageResolveJob(userID, msg, now)
		if !ok {
			continue
		}
		key := webPageResolveKey{peerType: domain.PeerTypeChannel, scopeID: msg.ChannelID, msgID: msg.ID, expectedID: job.expectedID}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		r.enqueueWebPageResolve(job)
	}
}

func expiredPrivateWebPageResolveJob(msg domain.Message, now int) (webPageResolveJob, bool) {
	wp, ok := expiredPendingWebPage(msg.Media, now)
	if !ok || msg.OwnerUserID == 0 || msg.ID <= 0 || msg.Peer.ID == 0 {
		return webPageResolveJob{}, false
	}
	return webPageResolveJob{
		senderID:       msg.OwnerUserID,
		peer:           msg.Peer,
		msgID:          msg.ID,
		expectedID:     wp.ID,
		url:            wp.URL,
		invertMedia:    msg.Media.InvertMedia,
		forceLarge:     wp.ForceLargeMedia,
		forceSmall:     wp.ForceSmallMedia,
		timeout:        webpageRequestResolveBudget,
		emptyOnFailure: true,
	}, true
}

func expiredChannelWebPageResolveJob(userID int64, msg domain.ChannelMessage, now int) (webPageResolveJob, bool) {
	wp, ok := expiredPendingWebPage(msg.Media, now)
	if !ok || msg.ChannelID == 0 || msg.ID <= 0 {
		return webPageResolveJob{}, false
	}
	senderID := msg.SenderUserID
	if senderID == 0 {
		senderID = userID
	}
	return webPageResolveJob{
		senderID:       senderID,
		peer:           domain.Peer{Type: domain.PeerTypeChannel, ID: msg.ChannelID},
		msgID:          msg.ID,
		expectedID:     wp.ID,
		url:            wp.URL,
		invertMedia:    msg.Media.InvertMedia,
		forceLarge:     wp.ForceLargeMedia,
		forceSmall:     wp.ForceSmallMedia,
		timeout:        webpageRequestResolveBudget,
		emptyOnFailure: true,
	}, true
}

func expiredPendingWebPage(media *domain.MessageMedia, now int) (*domain.MessageWebPage, bool) {
	if media == nil || media.WebPage == nil || media.WebPage.State != domain.MessageWebPageStatePending {
		return nil, false
	}
	if media.WebPage.Date > now {
		return nil, false
	}
	return media.WebPage, true
}
