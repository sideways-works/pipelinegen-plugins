package store

import (
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestWhereTheStoreIsComesFromTheRunOverTheConfig(t *testing.T) {
	t.Setenv(plugin.EnvStoreEndpoint, "http://gateway:7000")
	t.Setenv(plugin.EnvStoreRegion, "eu-central-1")
	t.Setenv(plugin.EnvStoreBucket, "granted")
	t.Setenv(plugin.EnvStorePrefix, "tenant/pipelinegen")

	set := Settings{Endpoint: "https://s3.example", Region: "us-east-1", Bucket: "configured", Prefix: "shop/"}
	fromEnv(&set)

	if set.Endpoint != "http://gateway:7000" || set.Region != "eu-central-1" || set.Bucket != "granted" || set.Prefix != "tenant/pipelinegen/shop/" || !set.PathStyle {
		t.Fatalf("settings %+v", set)
	}
}
