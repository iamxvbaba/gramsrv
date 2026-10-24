package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/config"
	"telesrv/internal/domain"
	"telesrv/internal/hoststats"
)

const (
	defaultAdminAPIAddr   = "127.0.0.1:2599"
	hostStatsPollInterval = 5 * time.Second
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

	hs := hoststats.NewPoller(cfg.DiskStatsPath)
	go hs.Run(ctx, hostStatsPollInterval)

	srv, err := newServer(cfg, newReadStore(pool), hs)
	if err != nil {
		return err
	}
	go srv.sweepExpiredInvites(ctx)
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
	TOTPSecret    string
	// DiskStatsPath points the dashboard host-disk sampler at the local path
	// that matters for the selected blob backend: permanent localfs storage or
	// the S3 upload spool.
	DiskStatsPath string
	// Permissions is the right set a panel session is issued with, from
	// TELESRV_ADMIN_UI_PERMISSIONS. The shipped default is the single wildcard
	// entry, so introducing the permission model never locks an operator out of a
	// panel that worked before.
	Permissions []string
	// IdentityDir mirrors config.IdentityDir -- must point at the same
	// directory the telesrv server reads, so an identity edit here is visible
	// to the running server immediately (see internal/identity). The name the
	// server boots with is read once at startup, so a rename here shows up on
	// the next telesrv restart.
	IdentityDir string
	// WelcomeMessagePhoneDefault/WelcomeMessageEmailDefault mirror
	// config.WelcomeMessage{Phone,Email}Template -- the env-var-resolved text
	// telesrv falls back to whenever the identity panel override is unset.
	// Surfaced as "effective default" on the identity screen so the panel can
	// show what would be sent and offer a one-click Reset, assuming both
	// binaries share the same .env.
	WelcomeMessagePhoneDefault string
	WelcomeMessageEmailDefault string
	// LoginCodeMessageDefault mirrors config.LoginCodeMessageTemplate -- the
	// delivery-message text the telesrv process falls back to whenever the
	// identity panel override is unset. Same "effective default" contract as
	// the two fields above.
	LoginCodeMessageDefault string
	// RepoRoot is the working directory the .env editor (see
	// internal/procctl) operates on: it reads .env.example for the field
	// vocabulary and rewrites .env in place. The panel resolves it the same
	// way config.Load does -- the process working directory.
	RepoRoot string
	// RedisAddr/RedisPassword/RedisDB mirror the telesrv-side ephemeral store
	// settings, used by the Services status page to ping Redis the same way
	// the server is configured (see handleServerStatusAPI in
	// serversettings.go).
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	// ServerPort is telesrv's MTProto TCP listen port (config.ListenAddr),
	// dialed from 127.0.0.1 by the Services status page to report whether the
	// core server is up.
	ServerPort int
}

// loadConfig 通过 internal/config.Load() 加载 .env 配置文件与环境变量，
// 并转换为 telesrv-admin 需要的 uiConfig。环境变量优先级高于 .env 文件。
func loadConfig() (uiConfig, error) {
	appCfg, err := config.Load()
	if err != nil {
		return uiConfig{}, fmt.Errorf("load config: %w", err)
	}
	// 管理面板也能按手机号建号，必须与 telesrv 主进程使用同一套格式策略，
	// 否则面板写入的号码主进程会当成非法号。
	domain.ConfigurePhoneFormatCheck(appCfg.CheckNumberFormat)

	adminAPIAddr := appCfg.AdminAPIAddr
	if strings.TrimSpace(adminAPIAddr) == "" {
		adminAPIAddr = defaultAdminAPIAddr
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
	totpSecret := strings.TrimSpace(os.Getenv("TELESRV_ADMIN_UI_TOTP_SECRET"))
	if totpSecret != "" {
		if _, err := newTOTPVerifier(totpSecret); err != nil {
			return uiConfig{}, fmt.Errorf("TELESRV_ADMIN_UI_TOTP_SECRET: %w", err)
		}
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		return uiConfig{}, fmt.Errorf("resolve working directory: %w", err)
	}
	serverPort, err := serverListenPort(appCfg.ListenAddr)
	if err != nil {
		return uiConfig{}, err
	}

	return uiConfig{
		Addr:                       appCfg.AdminUIAddr,
		PostgresDSN:                appCfg.PostgresDSN,
		AdminAPIURL:                adminAPIURL(adminAPIAddr),
		AdminAPIToken:              appCfg.AdminAPIToken,
		Password:                   appCfg.AdminUIPassword,
		Token:                      appCfg.AdminUIToken,
		SessionKey:                 sum[:],
		TOTPSecret:                 totpSecret,
		DiskStatsPath:              dashboardDiskPath(appCfg),
		Permissions:                appCfg.AdminUIPermissions,
		IdentityDir:                appCfg.IdentityDir,
		WelcomeMessagePhoneDefault: appCfg.WelcomeMessagePhoneTemplate,
		WelcomeMessageEmailDefault: appCfg.WelcomeMessageEmailTemplate,
		LoginCodeMessageDefault:    appCfg.LoginCodeMessageTemplate,
		RepoRoot:                   repoRoot,
		RedisAddr:                  appCfg.RedisAddr,
		RedisPassword:              appCfg.RedisPassword,
		RedisDB:                    appCfg.RedisDB,
		ServerPort:                 serverPort,
	}, nil
}

// serverListenPort extracts the TCP port from telesrv's MTProto listen
// address (config.ListenAddr), defaulting like config does. Only the port is
// used: the status check always dials 127.0.0.1 so it probes this machine's
// own telesrv process regardless of what interface it was bound to.
func serverListenPort(addr string) (int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "0.0.0.0:2398"
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("parse TELESRV_LISTEN %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse TELESRV_LISTEN port %q: %w", portStr, err)
	}
	return port, nil
}

func dashboardDiskPath(cfg config.Config) string {
	if strings.EqualFold(strings.TrimSpace(cfg.BlobBackendKind), "s3") && strings.TrimSpace(cfg.BlobStagingDir) != "" {
		return cfg.BlobStagingDir
	}
	return cfg.BlobDir
}

func adminAPIURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = defaultAdminAPIAddr
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
