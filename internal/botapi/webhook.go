package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const (
	webhookScanInterval = 250 * time.Millisecond
	webhookLeaseTTL     = 30 * time.Second
	webhookIdleDelay    = time.Hour
	webhookBotWorkers   = 16
	webhookHTTPWorkers  = 64
	webhookDueBatch     = 64
)

type webhookDispatcher struct {
	control GatewayWebhookControl
	gateway GatewayService
	client  *http.Client
	logger  *zap.Logger
	botSem  chan struct{}
	httpSem chan struct{}
}

func newWebhookHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// A redirect could leak X-Telegram-Bot-Api-Secret-Token to another host.
			return http.ErrUseLastResponse
		},
	}
}

func runWebhookDispatcher(ctx context.Context, control GatewayWebhookControl, gateway GatewayService, client *http.Client, logger *zap.Logger) {
	if control == nil || gateway == nil {
		return
	}
	if client == nil {
		client = newWebhookHTTPClient()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	d := &webhookDispatcher{
		control: control, gateway: gateway, client: client, logger: logger,
		botSem: make(chan struct{}, webhookBotWorkers), httpSem: make(chan struct{}, webhookHTTPWorkers),
	}
	d.scan(ctx)
	ticker := time.NewTicker(webhookScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.scan(ctx)
		}
	}
}

func (d *webhookDispatcher) scan(ctx context.Context) {
	configs, err := d.control.ListDueBotAPIWebhooks(ctx, webhookDueBatch)
	if err != nil {
		d.logger.Warn("list due bot api webhooks", zap.Error(err))
		return
	}
	for _, config := range configs {
		select {
		case d.botSem <- struct{}{}:
			go func(config domain.BotAPIWebhook) {
				defer func() { <-d.botSem }()
				d.deliver(ctx, config)
			}(config)
		default:
			return
		}
	}
}

func (d *webhookDispatcher) deliver(parent context.Context, candidate domain.BotAPIWebhook) {
	ctx, cancel := context.WithTimeout(parent, webhookLeaseTTL)
	defer cancel()
	owner := randomBotAPIOwner()
	acquired, err := d.control.AcquireBotAPIWebhookLease(ctx, candidate.BotUserID, owner, webhookLeaseTTL)
	if err != nil {
		d.logger.Warn("acquire bot api webhook lease", zap.Int64("bot_user_id", candidate.BotUserID), zap.Error(err))
		return
	}
	if !acquired {
		return
	}
	released := false
	defer func() {
		if released {
			return
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer releaseCancel()
		_ = d.control.ReleaseBotAPIWebhookLease(releaseCtx, candidate.BotUserID, owner)
	}()

	// Re-read after taking the lease so a stale due-list row can never deliver to
	// a URL that has since been deleted or replaced.
	config, found, err := d.control.BotAPIWebhook(ctx, candidate.BotUserID)
	if err != nil || !found {
		return
	}
	events, err := d.gateway.BotAPIUpdates(ctx, config.BotUserID, 0)
	if err != nil {
		d.fail(ctx, config, owner, fmt.Errorf("load updates: %w", err))
		released = true
		return
	}
	if len(events) == 0 {
		err = d.control.RecordBotAPIWebhookSuccess(ctx, config.BotUserID, owner, time.Now().Add(webhookIdleDelay))
		if err != nil {
			d.logger.Warn("idle bot api webhook", zap.Int64("bot_user_id", config.BotUserID), zap.Error(err))
		}
		released = err == nil
		return
	}

	limit := config.MaxConnections
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	if limit > len(events) {
		limit = len(events)
	}
	type delivery struct {
		index    int
		updateID int64
		err      error
	}
	results := make(chan delivery, limit)
	for i := 0; i < limit; i++ {
		item, _, ok := apiUpdate(events[i])
		if !ok {
			results <- delivery{index: i, updateID: int64(events[i].Pts), err: errors.New("update projection failed")}
			continue
		}
		payload, err := json.Marshal(item)
		if err != nil {
			results <- delivery{index: i, updateID: int64(events[i].Pts), err: err}
			continue
		}
		go func(index int, updateID int64, payload []byte) {
			select {
			case d.httpSem <- struct{}{}:
				defer func() { <-d.httpSem }()
			case <-ctx.Done():
				results <- delivery{index: index, updateID: updateID, err: ctx.Err()}
				return
			}
			results <- delivery{index: index, updateID: updateID, err: d.post(ctx, config, payload)}
		}(i, int64(events[i].Pts), payload)
	}
	deliveries := make([]delivery, limit)
	for i := 0; i < limit; i++ {
		result := <-results
		deliveries[result.index] = result
	}
	confirmedID := int64(0)
	var firstErr error
	for _, result := range deliveries {
		if result.err != nil {
			firstErr = result.err
			break
		}
		confirmedID = result.updateID
	}
	if confirmedID > 0 {
		if err := d.control.ConfirmBotAPIWebhookDelivery(ctx, config.BotUserID, confirmedID); err != nil {
			firstErr = fmt.Errorf("confirm update %d: %w", confirmedID, err)
		}
	}
	if firstErr != nil {
		d.fail(ctx, config, owner, firstErr)
		released = true
		return
	}
	nextAttempt := time.Now()
	if limit == len(events) && len(events) < 100 {
		nextAttempt = nextAttempt.Add(webhookIdleDelay)
	}
	if err := d.control.RecordBotAPIWebhookSuccess(ctx, config.BotUserID, owner, nextAttempt); err != nil {
		d.logger.Warn("complete bot api webhook", zap.Int64("bot_user_id", config.BotUserID), zap.Error(err))
		return
	}
	released = true
}

func (d *webhookDispatcher) post(ctx context.Context, config domain.BotAPIWebhook, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if config.SecretToken != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", config.SecretToken)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (d *webhookDispatcher) fail(ctx context.Context, config domain.BotAPIWebhook, owner string, cause error) {
	exponent := config.FailureCount
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 8 {
		exponent = 8
	}
	delay := time.Second * time.Duration(1<<exponent)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	// Small deterministic jitter prevents synchronized retries without a global RNG lock.
	delay += time.Duration(config.BotUserID&255) * time.Millisecond
	message := cause.Error()
	if err := d.control.RecordBotAPIWebhookFailure(ctx, config.BotUserID, owner, time.Now().Add(delay), message); err != nil {
		d.logger.Warn("record bot api webhook failure", zap.Int64("bot_user_id", config.BotUserID), zap.Error(err))
		return
	}
	d.logger.Warn("bot api webhook delivery failed", zap.Int64("bot_user_id", config.BotUserID), zap.Duration("retry_in", delay), zap.String("reason", message))
}
