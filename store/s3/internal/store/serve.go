package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"

	"github.com/sideways-works/pipelinegen-plugins/store/s3/internal/s3"
)

// Version is the plugin's version, set by the release build.
var Version = "dev"

// Settings are the plugin's config block: where the bucket is, and the prefix of the records in it.
type Settings struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
	// PathStyle puts the bucket in the path instead of the host, as SeaweedFS and MinIO usually want.
	PathStyle bool `json:"path_style"`
}

// fromEnv takes where the store is from the variables a CI system sets at run time, as piperun's
// services do, over the config's; a prefix from there comes before the config's own, which then
// divides the bucket's grant between repositories.
func fromEnv(set *Settings) {
	for v, field := range map[string]*string{
		plugin.EnvStoreEndpoint: &set.Endpoint, plugin.EnvStoreRegion: &set.Region, plugin.EnvStoreBucket: &set.Bucket,
	} {
		if got := os.Getenv(v); got != "" {
			*field = got
		}
	}

	if p := os.Getenv(plugin.EnvStorePrefix); p != "" {
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}

		set.Prefix = p + strings.TrimPrefix(set.Prefix, "/")
		set.PathStyle = true
	}
}

// Serve answers pipelinegen's calls.
func Serve(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return plugin.Serve(args, stdin, stdout, stderr, map[string]plugin.Handler{
		plugin.CallDescribe:   func([]byte) (any, error) { return describe(), nil },
		plugin.CallStoreList:  list,
		plugin.CallStoreGet:   get,
		plugin.CallStoreRead:  read,
		plugin.CallStoreWrite: write,
	})
}

func describe() plugin.Description {
	return plugin.Description{Protocol: plugin.Protocol, Kind: plugin.KindStore, Name: "s3-store", Version: Version}
}

// open reads the request's settings and the credentials from the environment.
func open(req plugin.Request) (*Store, error) {
	data, err := json.Marshal(req.Config)
	if err != nil {
		return nil, fmt.Errorf("%w: config: %w", ErrStore, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var set Settings
	if err := dec.Decode(&set); err != nil {
		return nil, fmt.Errorf("%w: config: %w", ErrStore, err)
	}

	fromEnv(&set)

	endpoint, err := url.Parse(set.Endpoint)
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" {
		return nil, fmt.Errorf("%w: config: endpoint must be an http or https URL, such as https://s3.eu-central-1.amazonaws.com", ErrStore)
	}

	if set.Bucket == "" || set.Region == "" {
		return nil, fmt.Errorf("%w: config: bucket and region are required", ErrStore)
	}

	if set.Prefix != "" && !strings.HasSuffix(set.Prefix, "/") {
		set.Prefix += "/"
	}

	creds := s3.Credentials{
		AccessKeyID:     os.Getenv(plugin.EnvStoreAccessKeyID),
		SecretAccessKey: os.Getenv(plugin.EnvStoreSecretAccessKey),
		SessionToken:    os.Getenv(plugin.EnvStoreSessionToken),
	}

	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("%w: no key: %s and %s", ErrStore, plugin.EnvStoreAccessKeyID, plugin.EnvStoreSecretAccessKey)
	}

	return &Store{
		Client: &s3.Client{Endpoint: endpoint, Region: set.Region, Bucket: set.Bucket, PathStyle: set.PathStyle, Creds: creds},
		Prefix: set.Prefix,
		Heads:  LoadHeads(req.Cache),
	}, nil
}

// ctxOf is a call's context: a plugin call is a process, which pipelinegen ends when the call's
// time is up.
func ctxOf() context.Context { return context.Background() }

func decodeReq(in []byte, req any) error {
	if err := json.Unmarshal(in, req); err != nil {
		return fmt.Errorf("%w: request: %w", ErrStore, err)
	}

	return nil
}

func list(in []byte) (any, error) {
	var req plugin.StoreListRequest
	if err := decodeReq(in, &req); err != nil {
		return nil, err
	}

	s, err := open(req.Request)
	if err != nil {
		return nil, err
	}

	recs, err := s.List(ctxOf(), req.Prefixes)
	if err != nil {
		return nil, err
	}

	ans := plugin.StoreListAnswer{Records: make([]plugin.StoreRecord, 0, len(recs))}
	for _, r := range recs {
		ans.Records = append(ans.Records, plugin.StoreRecord{Name: r.Name, Version: r.Version, Time: r.Time})
	}

	return ans, s.Heads.Save()
}

func get(in []byte) (any, error) {
	var req plugin.StoreGetRequest
	if err := decodeReq(in, &req); err != nil {
		return nil, err
	}

	s, err := open(req.Request)
	if err != nil {
		return nil, err
	}

	r, err := s.First(ctxOf(), req.Names)
	if err != nil || r == nil {
		return plugin.StoreGetAnswer{}, err
	}

	return plugin.StoreGetAnswer{Record: &plugin.StoreRecord{Name: r.Name, Version: r.Version, Time: r.Time}}, nil
}

func read(in []byte) (any, error) {
	var req plugin.StoreReadRequest
	if err := decodeReq(in, &req); err != nil {
		return nil, err
	}

	s, err := open(req.Request)
	if err != nil {
		return nil, err
	}

	files, err := s.Read(ctxOf(), req.Versions, req.Paths)
	if err != nil {
		return nil, err
	}

	return plugin.StoreReadAnswer{Files: files}, nil
}

func write(in []byte) (any, error) {
	var req plugin.StoreWriteRequest
	if err := decodeReq(in, &req); err != nil {
		return nil, err
	}

	s, err := open(req.Request)
	if err != nil {
		return nil, err
	}

	ups := make([]Update, 0, len(req.Updates))
	for _, u := range req.Updates {
		ups = append(ups, Update{Name: u.Name, Expect: u.Expect, Any: u.Any, Delete: u.Delete, Files: u.Files})
	}

	versions, err := s.Write(ctxOf(), ups, req.Time)
	ans := plugin.StoreWriteAnswer{Versions: versions}

	if stale, ok := errors.AsType[*StaleError](err); ok {
		ans.Stale = []string{stale.Name}

		return ans, s.Heads.Save()
	}

	if err != nil {
		return nil, err
	}

	return ans, s.Heads.Save()
}
