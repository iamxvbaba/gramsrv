// Package td implements MTProto encoding and decoding.
package td

//go:generate go run github.com/iamxvbaba/td/cmd/gotdgen --doc "https://core.telegram.org/" --clean --server --handlers --mapping --slices --package tg --target tg --layer-target tlprofile --layer-package tlprofile --schema-manifest _schema/layers/manifest.json --layer-policy _schema/layers/policy.json --layer-plan-audit _schema/layers/audit/execution-plans.json
//go:generate go run github.com/iamxvbaba/td/cmd/gotdgen --doc "https://core.telegram.org/" --clean --package e2e --target tg/e2e --schema _schema/encrypted.tl

//go:generate go run github.com/iamxvbaba/td/cmd/gotdgen --clean --client=false --package tgtrace --target tgtrace --schema _schema/trace.tl

//go:generate go run github.com/iamxvbaba/td/cmd/gotdgen --clean --package mt --target mt --client=false --schema _schema/mt.tl
