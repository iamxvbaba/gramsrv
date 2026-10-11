package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"telesrv/internal/officialgifts"
)

func main() {
	ctx := context.Background()
	if len(os.Args) < 2 {
		fmt.Println("usage: snapcheck <root>")
		os.Exit(2)
	}
	catalog := officialgifts.New(os.Args[1])
	list, err := catalog.List(ctx)
	if err != nil {
		fmt.Println("LIST ERR:", err)
		os.Exit(1)
	}
	fmt.Printf("LIST OK: %d gifts\n", len(list))
	for _, g := range list {
		bundle, err := catalog.Bundle(ctx, g.ID, g.CanUpgrade())
		if err != nil {
			fmt.Printf("BUNDLE ERR gift=%d: %v\n", g.ID, err)
			os.Exit(1)
		}
		base := bundle.BaseDocument
		models := 0
		patterns := 0
		if bundle.Collectible != nil {
			models = len(bundle.Collectible.Models)
			patterns = len(bundle.Collectible.Patterns)
		}
		fmt.Printf("  gift=%-6d title=%q stars=%-6d base=%-6d bytes anim_file=%s models=%d patterns=%d manifest_sha=%s\n",
			g.ID, g.Title, g.Stars, base.Size, base.FileName, 0, 0, base64.StdEncoding.EncodeToString(bundle.ManifestSHA256)[:12])
		fmt.Printf("         (collectible models=%d patterns=%d backdrops=%d)\n", models, patterns, 0)
	}
	fmt.Println("SNAPSHOT VALID")
}
