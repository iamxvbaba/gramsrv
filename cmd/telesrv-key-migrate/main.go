// Command telesrv-key-migrate converts an RSA private key to traditional
// PKCS#1 PEM without changing its key material.
package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	input := flag.String("in", "data/server_rsa.pem", "input RSA private key PEM")
	output := flag.String("out", "", "output PKCS#1 PEM; defaults to the input path")
	backup := flag.String("backup", "", "backup path when replacing the input")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(errors.New("positional arguments are not accepted"))
	}
	if err := migrate(*input, *output, *backup); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "telesrv-key-migrate:", err)
	os.Exit(1)
}

func migrate(input, output, backup string) error {
	if input == "" {
		return errors.New("-in is required")
	}
	if output == "" {
		output = input
	}
	inputPath, err := filepath.Abs(input)
	if err != nil {
		return fmt.Errorf("resolve input: %w", err)
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve output: %w", err)
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return errors.New("input does not contain a PEM block")
	}
	var key *rsa.PrivateKey
	if parsed, parseErr := x509.ParsePKCS1PrivateKey(block.Bytes); parseErr == nil {
		key = parsed
	} else if parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes); parseErr == nil {
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return errors.New("input PKCS#8 key is not RSA")
		}
	} else {
		return errors.New("input is not a supported RSA PKCS#1 or PKCS#8 private key")
	}
	if err := key.Validate(); err != nil {
		return fmt.Errorf("validate RSA key: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(outputPath), ".server-rsa-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict temporary output: %w", err)
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if inputPath == outputPath && backup == "" {
		backup = outputPath + ".before-pkcs1"
	}
	if inputPath == outputPath {
		if _, err := os.Stat(backup); err == nil {
			return fmt.Errorf("backup already exists: %s", backup)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check backup: %w", err)
		}
		if err := os.Rename(inputPath, backup); err != nil {
			return fmt.Errorf("create backup: %w", err)
		}
	}
	if err := os.Rename(tmpPath, outputPath); err != nil {
		if inputPath == outputPath {
			_ = os.Rename(backup, inputPath)
		}
		return fmt.Errorf("install output: %w", err)
	}
	fmt.Printf("PKCS#1 RSA key written to %s\n", outputPath)
	if inputPath == outputPath {
		fmt.Printf("original key backed up to %s\n", backup)
	}
	return nil
}