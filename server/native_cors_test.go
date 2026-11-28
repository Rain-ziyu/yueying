package main

import (
	"reflect"
	"testing"

	"github.com/minio/minio-go/v7/pkg/cors"
)

func TestNativeOriginsPreserveBucketRules(t *testing.T) {
	web := "https://rain-ziyu.github.io"
	other := cors.Rule{ID: "other", AllowedOrigin: []string{"https://other.example"}, AllowedMethod: []string{"HEAD"}}
	config := cors.NewConfig([]cors.Rule{other, {
		ID: "uploads", AllowedOrigin: []string{web}, AllowedMethod: []string{"PUT"},
		AllowedHeader: []string{"Content-Type"}, ExposeHeader: []string{"ETag"}, MaxAgeSeconds: 600,
	}})
	expected := config.CORSRules[1]
	expected.AllowedOrigin = []string{web, "https://localhost"}
	origins := []string{"https://localhost", "https://localhost", ""}
	changed, err := extendClientOrigins(config, web, origins)
	if err != nil || !changed || !reflect.DeepEqual(config.CORSRules, []cors.Rule{other, expected}) {
		t.Fatalf("unexpected merge: %+v, %v", config, err)
	}
	if changed, err = extendClientOrigins(config, web, origins); changed || err != nil {
		t.Fatal("repeat merge must leave the configuration unchanged")
	}
	if changed, err = extendClientOrigins(config, "https://missing.example", origins); err == nil || changed {
		t.Fatal("missing source rule must prevent updating the bucket")
	}
}
