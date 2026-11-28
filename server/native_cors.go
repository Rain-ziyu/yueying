package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/minio/minio-go/v7/pkg/cors"
)

// Extend the existing web client's rules so multipart uploads and range reads
// retain the same allowed methods, headers and cache duration on each client.
func extendClientOrigins(config *cors.Config, webOrigin string, origins []string) (bool, error) {
	found, changed := false, false
	if config != nil {
		for i := range config.CORSRules {
			rule := &config.CORSRules[i]
			if !slices.Contains(rule.AllowedOrigin, webOrigin) {
				continue
			}
			found = true
			for _, value := range origins {
				origin := strings.TrimSpace(value)
				if origin != "" && !slices.Contains(rule.AllowedOrigin, origin) {
					rule.AllowedOrigin = append(rule.AllowedOrigin, origin)
					changed = true
				}
			}
		}
	}
	if !found {
		return false, fmt.Errorf("no existing bucket CORS rule for %s", webOrigin)
	}
	return changed, nil
}

func syncClientOrigins(ctx context.Context, webOrigin string) error {
	stores, err := storesFromEnv()
	if err != nil {
		return err
	}
	store, ok := stores[os.Getenv("DEFAULT_STORE")].(s3)
	if !ok {
		return fmt.Errorf("the default store must support the S3 CORS API")
	}
	config, err := store.c.GetBucketCors(ctx, store.bucket)
	if err != nil {
		return err
	}
	changed, err := extendClientOrigins(config, webOrigin, strings.Split(os.Getenv("NATIVE_CLIENT_ORIGINS"), ","))
	if err != nil || !changed {
		return err
	}
	return store.c.SetBucketCors(ctx, store.bucket, config)
}
