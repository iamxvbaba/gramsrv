package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/config"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	srv, err := newServer(cfg, newReadStore(pool))
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("telesrv-admin listening on %s", cfg.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

type uiConfig struct {
	Addr          string
	PostgresDSN   string
	AdminAPIURL   string
	AdminAPIToken string
	Password      string
	Token         string
	SessionKey    []byte
}

// loadConfig 通过 internal/config.Load() 加载 .env 配置文件与环境变量，
// 并转换为 telesrv-admin 需要的 uiConfig。环境变量优先级高于 .env 文件。
func loadConfig() (uiConfig, error) {
	appCfg, err := config.Load()
	if err != nil {
		return uiConfig{}, fmt.Errorf("load config: %w", err)
	}

	adminAPIAddr := appCfg.AdminAPIAddr
	if strings.TrimSpace(adminAPIAddr) == "" {
		adminAPIAddr = "127.0.0.1:2399"
	}

	if appCfg.AdminUIPassword == "" && appCfg.AdminUIToken == "" {
		return uiConfig{}, fmt.Errorf("TELESRV_ADMIN_UI_PASSWORD or TELESRV_ADMIN_UI_TOKEN is required")
	}
	if strings.TrimSpace(appCfg.AdminAPIToken) == "" {
		return uiConfig{}, fmt.Errorf("TELESRV_ADMIN_API_TOKEN is required for admin write actions")
	}
	if appCfg.AdminSessionKey == "" {
		return uiConfig{}, fmt.Errorf("TELESRV_ADMIN_SESSION_KEY is required")
	}
	sum := sha256.Sum256([]byte(appCfg.AdminSessionKey))

	return uiConfig{
		Addr:          appCfg.AdminUIAddr,
		PostgresDSN:   appCfg.PostgresDSN,
		AdminAPIURL:   adminAPIURL(adminAPIAddr),
		AdminAPIToken: appCfg.AdminAPIToken,
		Password:      appCfg.AdminUIPassword,
		Token:         appCfg.AdminUIToken,
		SessionKey:    sum[:],
	}, nil
}

func adminAPIURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "127.0.0.1:2399"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + addr
}

func newCommandID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + time.Now().UTC().Format("20060102T150405.000000000") + "-" + hex.EncodeToString(b[:])
}
