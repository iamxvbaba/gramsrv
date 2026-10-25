package config

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Copying a shipped Core YAML must not disclose session IPs to a third party.
func TestExampleConfigurationKeepsGeoIPOptIn(t *testing.T) {
	paths := []string{
		"../../configs/examples/core.local.yaml",
		"../../configs/examples/core.ton-mainnet.yaml",
		"../../deploy/docker/config/core.yaml",
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			GeoIP GeoIPYAML `yaml:"geoip"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(doc.GeoIP.Endpoints) != 0 {
			t.Fatalf("%s enables third-party session IP disclosure: %v", path, doc.GeoIP.Endpoints)
		}
	}
}

func TestGeoIPConfigValidationDoesNotEchoCredentials(t *testing.T) {
	const marker = "geoip-test-credential-marker"
	for _, endpoint := range []string{
		"https://geo.example.test/?key=" + marker,
		"https://geo.example.test/%zz/{ip}?key=" + marker,
		"ftp://geo.example.test/{ip}?key=" + marker,
		"https:///{ip}?key=" + marker,
	} {
		err := validateGeoIPConfig(Config{GeoIPEndpoints: []string{"https://api.ipapi.is/?q={ip}", endpoint}})
		if err == nil || strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), "entry 2") {
			t.Fatalf("expected indexed, credential-safe configuration error, got %v", err)
		}
	}
}
