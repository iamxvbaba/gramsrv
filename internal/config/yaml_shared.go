package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type configRole string

const (
	roleEdge   configRole = "edge"
	roleCore   configRole = "core"
	roleEgress configRole = "egress"
	roleFile   configRole = "file"
	roleSFU    configRole = "sfu"
	roleAdmin  configRole = "admin"
	roleTON    configRole = "ton"
)

type CommonYAML struct {
	Instance InstanceYAML `yaml:"instance"`
	Debug    DebugYAML    `yaml:"debug"`
	Public   PublicYAML   `yaml:"public"`
	Postgres PostgresYAML `yaml:"postgres"`
	Redis    RedisYAML    `yaml:"redis"`
}

type InstanceYAML struct {
	ID string `yaml:"id"`
}

type DebugYAML struct {
	Addr *string `yaml:"addr"`
}

type PublicYAML struct {
	DC                 *int          `yaml:"dc"`
	DefaultCountryCode string        `yaml:"default_country_code"`
	StrictDCCheck      *bool         `yaml:"strict_dc_check"`
	Advertise          AdvertiseYAML `yaml:"advertise"`
	BaseURL            string        `yaml:"base_url"`
	AppScheme          string        `yaml:"app_scheme"`
	AppLinkBase        *string       `yaml:"app_link_base"`
	WebBaseURL         string        `yaml:"web_base_url"`
	AppName            string        `yaml:"app_name"`
}

type AdvertiseYAML struct {
	IP   string `yaml:"ip"`
	Port *int   `yaml:"port"`
}

type BrandingYAML struct {
	ProductName     string `yaml:"product_name"`
	ProductUsername string `yaml:"product_username"`
	DesktopAppName  string `yaml:"desktop_app_name"`
	AndroidAppName  string `yaml:"android_app_name"`
	IOSAppName      string `yaml:"ios_app_name"`
	MacOSAppName    string `yaml:"macos_app_name"`
	WebAAppName     string `yaml:"web_a_app_name"`
	WebKAppName     string `yaml:"web_k_app_name"`
	PremiumName     string `yaml:"premium_name"`
	StarsName       string `yaml:"stars_name"`
}

type PostgresYAML struct {
	DSN                     string `yaml:"dsn"`
	MaxConns                *int   `yaml:"max_conns"`
	MinConns                *int   `yaml:"min_conns"`
	CounterRecoveryMaxConns *int   `yaml:"counter_recovery_max_conns"`
}

type RedisYAML struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       *int   `yaml:"db"`
}

type GRPCServerYAML struct {
	Addr  string        `yaml:"addr"`
	URL   string        `yaml:"url"`
	Token string        `yaml:"token"`
	TLS   ServerTLSYAML `yaml:"tls"`
}

type GRPCClientYAML struct {
	Resolver string        `yaml:"resolver"`
	Targets  []string      `yaml:"targets"`
	Token    string        `yaml:"token"`
	Timeout  string        `yaml:"timeout"`
	TLS      ClientTLSYAML `yaml:"tls"`
}

type ServerTLSYAML struct {
	CertFile     string `yaml:"cert_file"`
	KeyFile      string `yaml:"key_file"`
	ClientCAFile string `yaml:"client_ca_file"`
}

type ClientTLSYAML struct {
	CAFile     string `yaml:"ca_file"`
	ServerName string `yaml:"server_name"`
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
}

type envBuilder struct {
	values map[string]string
}

func newEnvBuilder() *envBuilder {
	return &envBuilder{values: map[string]string{}}
}

func requireYAMLVersion(version int) error {
	if version != 1 {
		return fmt.Errorf("version must be 1")
	}
	return nil
}

func (b *envBuilder) source() envSource {
	return newEnvSource(b.values, false)
}

func (b *envBuilder) setString(key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.values[key] = strings.TrimSpace(value)
}

func (b *envBuilder) setOptionalString(key string, value *string) {
	if value == nil {
		return
	}
	b.values[key] = strings.TrimSpace(*value)
}

func (b *envBuilder) setBool(key string, value *bool) {
	if value == nil {
		return
	}
	b.values[key] = fmt.Sprintf("%t", *value)
}

func (b *envBuilder) setInt(key string, value *int) {
	if value == nil {
		return
	}
	b.values[key] = fmt.Sprintf("%d", *value)
}

func (b *envBuilder) setInt64(key string, value *int64) {
	if value == nil {
		return
	}
	b.values[key] = fmt.Sprintf("%d", *value)
}

func (b *envBuilder) setDuration(key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if _, err := time.ParseDuration(value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	b.values[key] = value
	return nil
}

func (b *envBuilder) setList(key string, values []string) {
	if len(values) == 0 {
		return
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	if len(out) > 0 {
		b.values[key] = strings.Join(out, ",")
	}
}

func applyCommonYAML(b *envBuilder, y CommonYAML) error {
	b.setString("TELESRV_INSTANCE_ID", y.Instance.ID)
	b.setOptionalString("TELESRV_DEBUG_ADDR", y.Debug.Addr)
	b.setInt("TELESRV_DC", y.Public.DC)
	b.setString("TELESRV_DEFAULT_COUNTRY_CODE", y.Public.DefaultCountryCode)
	b.setBool("TELESRV_STRICT_DC_CHECK", y.Public.StrictDCCheck)
	b.setString("TELESRV_ADVERTISE_IP", y.Public.Advertise.IP)
	b.setInt("TELESRV_ADVERTISE_PORT", y.Public.Advertise.Port)
	b.setString("TELESRV_PUBLIC_BASE_URL", y.Public.BaseURL)
	b.setString("TELESRV_PUBLIC_APP_SCHEME", y.Public.AppScheme)
	b.setOptionalString("TELESRV_PUBLIC_APP_LINK_BASE", y.Public.AppLinkBase)
	b.setString("TELESRV_PUBLIC_WEB_BASE_URL", y.Public.WebBaseURL)
	b.setString("TELESRV_PUBLIC_APP_NAME", y.Public.AppName)

	b.setString("TELESRV_POSTGRES_DSN", y.Postgres.DSN)
	b.setInt("TELESRV_POSTGRES_MAX_CONNS", y.Postgres.MaxConns)
	b.setInt("TELESRV_POSTGRES_MIN_CONNS", y.Postgres.MinConns)
	b.setInt("TELESRV_POSTGRES_COUNTER_RECOVERY_MAX_CONNS", y.Postgres.CounterRecoveryMaxConns)
	b.setString("TELESRV_REDIS_ADDR", y.Redis.Addr)
	b.setString("TELESRV_REDIS_PASSWORD", y.Redis.Password)
	b.setInt("TELESRV_REDIS_DB", y.Redis.DB)
	return nil
}

// IdentityYAML locates the operator-editable server identity (identity.json
// plus icon). Core and Admin must point at the same directory: Admin writes it
// and every Core instance watches it.
type IdentityYAML struct {
	Dir string `yaml:"dir"`
}

func applyIdentityYAML(b *envBuilder, y IdentityYAML) {
	b.setString("TELESRV_IDENTITY_DIR", y.Dir)
}

func applyBrandingYAML(b *envBuilder, y BrandingYAML) {
	b.setString("TELESRV_BRAND_PRODUCT_NAME", y.ProductName)
	b.setString("TELESRV_BRAND_PRODUCT_USERNAME", y.ProductUsername)
	b.setString("TELESRV_BRAND_DESKTOP_APP_NAME", y.DesktopAppName)
	b.setString("TELESRV_BRAND_ANDROID_APP_NAME", y.AndroidAppName)
	b.setString("TELESRV_BRAND_IOS_APP_NAME", y.IOSAppName)
	b.setString("TELESRV_BRAND_MACOS_APP_NAME", y.MacOSAppName)
	b.setString("TELESRV_BRAND_WEB_A_APP_NAME", y.WebAAppName)
	b.setString("TELESRV_BRAND_WEB_K_APP_NAME", y.WebKAppName)
	b.setString("TELESRV_BRAND_PREMIUM_NAME", y.PremiumName)
	b.setString("TELESRV_BRAND_STARS_NAME", y.StarsName)
}

func applyCoreExecServerYAML(b *envBuilder, y GRPCServerYAML) {
	b.setString("TELESRV_CORE_EXEC_GRPC_ADDR", y.Addr)
	b.setString("TELESRV_CORE_EXEC_TOKEN", y.Token)
	b.setString("TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_CORE_EXEC_GRPC_TLS_KEY_FILE", y.TLS.KeyFile)
	b.setString("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CA_FILE", y.TLS.ClientCAFile)
}

func applyCoreExecClientYAML(b *envBuilder, y GRPCClientYAML) error {
	b.setString("TELESRV_CORE_EXEC_GRPC_RESOLVER", y.Resolver)
	b.setList("TELESRV_CORE_EXEC_GRPC_TARGETS", y.Targets)
	b.setString("TELESRV_CORE_EXEC_TOKEN", y.Token)
	if err := b.setDuration("TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT", y.Timeout); err != nil {
		return err
	}
	b.setString("TELESRV_CORE_EXEC_GRPC_TLS_CA_FILE", y.TLS.CAFile)
	b.setString("TELESRV_CORE_EXEC_GRPC_TLS_SERVER_NAME", y.TLS.ServerName)
	b.setString("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_KEY_FILE", y.TLS.KeyFile)
	return nil
}

func applyFileServerYAML(b *envBuilder, y GRPCServerYAML) {
	b.setString("TELESRV_FILE_GRPC_ADDR", y.Addr)
	b.setString("TELESRV_FILE_TOKEN", y.Token)
	b.setString("TELESRV_FILE_GRPC_TLS_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_FILE_GRPC_TLS_KEY_FILE", y.TLS.KeyFile)
	b.setString("TELESRV_FILE_GRPC_TLS_CLIENT_CA_FILE", y.TLS.ClientCAFile)
}

func applyFileClientYAML(b *envBuilder, y GRPCClientYAML) error {
	b.setString("TELESRV_FILE_GRPC_RESOLVER", y.Resolver)
	b.setList("TELESRV_FILE_GRPC_TARGETS", y.Targets)
	b.setString("TELESRV_FILE_TOKEN", y.Token)
	if err := b.setDuration("TELESRV_FILE_GRPC_REQUEST_TIMEOUT", y.Timeout); err != nil {
		return err
	}
	b.setString("TELESRV_FILE_GRPC_TLS_CA_FILE", y.TLS.CAFile)
	b.setString("TELESRV_FILE_GRPC_TLS_SERVER_NAME", y.TLS.ServerName)
	b.setString("TELESRV_FILE_GRPC_TLS_CLIENT_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_FILE_GRPC_TLS_CLIENT_KEY_FILE", y.TLS.KeyFile)
	return nil
}

func applyEgressDeliveryServerYAML(b *envBuilder, y GRPCServerYAML) {
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_ADDR", y.Addr)
	b.setString("TELESRV_EGRESS_DELIVERY_TOKEN", y.Token)
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_TLS_KEY_FILE", y.TLS.KeyFile)
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CA_FILE", y.TLS.ClientCAFile)
}

func applyEgressDeliveryClientYAML(b *envBuilder, y GRPCClientYAML) error {
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_RESOLVER", y.Resolver)
	b.setList("TELESRV_EGRESS_DELIVERY_GRPC_TARGETS", y.Targets)
	b.setString("TELESRV_EGRESS_DELIVERY_TOKEN", y.Token)
	if err := b.setDuration("TELESRV_EGRESS_DELIVERY_GRPC_REQUEST_TIMEOUT", y.Timeout); err != nil {
		return err
	}
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CA_FILE", y.TLS.CAFile)
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_TLS_SERVER_NAME", y.TLS.ServerName)
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_KEY_FILE", y.TLS.KeyFile)
	return nil
}

func applySFUControlServerYAML(b *envBuilder, y GRPCServerYAML) {
	b.setString("TELESRV_SFU_CONTROL_GRPC_ADDR", y.Addr)
	b.setString("TELESRV_SFU_CONTROL_GRPC_URL", y.URL)
	b.setString("TELESRV_SFU_CONTROL_TOKEN", y.Token)
	b.setString("TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_SFU_CONTROL_GRPC_TLS_KEY_FILE", y.TLS.KeyFile)
	b.setString("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CA_FILE", y.TLS.ClientCAFile)
}

func applySFUControlClientYAML(b *envBuilder, y GRPCClientYAML) error {
	b.setString("TELESRV_SFU_CONTROL_TOKEN", y.Token)
	if err := b.setDuration("TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT", y.Timeout); err != nil {
		return err
	}
	b.setString("TELESRV_SFU_CONTROL_GRPC_TLS_CA_FILE", y.TLS.CAFile)
	b.setString("TELESRV_SFU_CONTROL_GRPC_TLS_SERVER_NAME", y.TLS.ServerName)
	b.setString("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CERT_FILE", y.TLS.CertFile)
	b.setString("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_KEY_FILE", y.TLS.KeyFile)
	return nil
}

func loadRoleYAML[T any](role configRole, apply func(*envBuilder, T) error) (Config, error) {
	path, err := roleConfigPath(role)
	if err != nil {
		return Config{}, err
	}
	var y T
	if err := readStrictYAML(path, &y); err != nil {
		return Config{}, fmt.Errorf("config file %q: %w", path, err)
	}
	b := newEnvBuilder()
	if err := apply(b, y); err != nil {
		return Config{}, err
	}
	cfg, err := loadFromEnv(b.source())
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func roleConfigPath(role configRole) (string, error) {
	if path, ok, err := configPathFromArgs(os.Args[1:]); err != nil || ok {
		return path, err
	}
	if path, ok := os.LookupEnv("TELESRV_CONFIG"); ok {
		path = strings.TrimSpace(path)
		if path == "" {
			return "", fmt.Errorf("TELESRV_CONFIG must not be empty for YAML role config")
		}
		return path, nil
	}
	return filepath.Join("configs", string(role)+".yaml"), nil
}

func configPathFromArgs(args []string) (string, bool, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config" || arg == "-config":
			if i+1 >= len(args) {
				return "", true, fmt.Errorf("%s requires a path", arg)
			}
			path := strings.TrimSpace(args[i+1])
			if path == "" {
				return "", true, fmt.Errorf("%s requires a non-empty path", arg)
			}
			return path, true, nil
		case strings.HasPrefix(arg, "--config="):
			path := strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
			if path == "" {
				return "", true, fmt.Errorf("--config requires a non-empty path")
			}
			return path, true, nil
		case strings.HasPrefix(arg, "-config="):
			path := strings.TrimSpace(strings.TrimPrefix(arg, "-config="))
			if path == "" {
				return "", true, fmt.Errorf("-config requires a non-empty path")
			}
			return path, true, nil
		}
	}
	return "", false, nil
}

func readStrictYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	expanded := os.Expand(string(data), func(key string) string {
		return os.Getenv(key)
	})
	dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("expected a single YAML document")
		}
		return err
	}
	return nil
}
