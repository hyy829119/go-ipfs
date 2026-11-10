// Package s3ds connects a Kubo repository's block mount to S3-compatible storage.
// The layout follows go-ds-s3's datastore approach, using AWS SDK v2 and keeping
// mutable Kubo metadata in the repository's local datastore.
package s3ds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ipfs/kubo/plugin"
	"github.com/ipfs/kubo/repo"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Plugins is the statically linked S3 datastore plugin.
var Plugins = []plugin.Plugin{&s3Plugin{}}

type s3Plugin struct{}

var _ plugin.PluginDatastore = (*s3Plugin)(nil)

func (*s3Plugin) Name() string                                { return "ds-s3" }
func (*s3Plugin) Version() string                             { return "0.1.0" }
func (*s3Plugin) Init(*plugin.Environment) error              { return nil }
func (*s3Plugin) DatastoreTypeName() string                   { return "s3ds" }
func (*s3Plugin) DatastoreConfigParser() fsrepo.ConfigFromMap { return parseConfig }

type datastoreConfig struct {
	endpoint, bucket, region, prefix, owner, credentialsFile string
	concurrency                                              int
	timeout, usageTTL                                        time.Duration
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func parseConfig(params map[string]any) (fsrepo.DatastoreConfig, error) {
	// Credentials must never be returned by `config show` or the WebUI editor.
	for _, key := range []string{"accessKey", "secretKey", "sessionToken"} {
		if _, exists := params[key]; exists {
			return nil, fmt.Errorf("s3ds: %s must be supplied through environment variables or credentialsFile", key)
		}
	}
	for _, key := range []string{"endpoint", "bucket", "region", "rootDirectory", "owner", "credentialsFile"} {
		if value, exists := params[key]; exists {
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("s3ds: %s must be a string", key)
			}
		}
	}
	get := func(key string) string { value, _ := params[key].(string); return value }
	c := &datastoreConfig{
		endpoint: strings.TrimRight(get("endpoint"), "/"),
		bucket:   get("bucket"), region: get("region"),
		prefix: get("rootDirectory"), owner: get("owner"),
		credentialsFile: get("credentialsFile"),
		concurrency:     16, timeout: 30 * time.Second, usageTTL: 30 * time.Second,
	}
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("s3ds: endpoint must be an http(s) URL without credentials, query or fragment")
	}
	if !bucketName.MatchString(c.bucket) || strings.Contains(c.bucket, "..") {
		return nil, errors.New("s3ds: bucket must be a valid S3 bucket name")
	}
	if c.region == "" {
		return nil, errors.New("s3ds: region is required")
	}
	if c.prefix == "" || c.prefix == "." || c.prefix == ".." || path.Clean(c.prefix) != c.prefix || strings.HasPrefix(c.prefix, "/") || strings.HasPrefix(c.prefix, "../") || strings.ContainsAny(c.prefix, "\\\r\n\x00") {
		return nil, errors.New("s3ds: rootDirectory must be a non-empty relative object prefix without dot segments")
	}
	if _, err := peer.Decode(c.owner); err != nil {
		return nil, fmt.Errorf("s3ds: owner must be the repository Peer ID: %w", err)
	}
	if c.credentialsFile != "" && !filepath.IsAbs(c.credentialsFile) {
		return nil, errors.New("s3ds: credentialsFile must be an absolute path")
	}
	if raw, ok := params["concurrency"]; ok {
		value, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("s3ds: concurrency: %w", err)
		}
		if err := json.Unmarshal(value, &c.concurrency); err != nil || c.concurrency < 1 || c.concurrency > 128 {
			return nil, errors.New("s3ds: concurrency must be an integer from 1 to 128")
		}
	}
	for key, dest := range map[string]*time.Duration{"requestTimeout": &c.timeout, "usageCacheTTL": &c.usageTTL} {
		if raw, exists := params[key]; exists {
			value, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("s3ds: %s must be a duration string", key)
			}
			*dest, err = time.ParseDuration(value)
			if err != nil || *dest < 0 || (key == "requestTimeout" && *dest == 0) {
				return nil, fmt.Errorf("s3ds: invalid %s", key)
			}
		}
	}
	return c, nil
}

func (c *datastoreConfig) DiskSpec() fsrepo.DiskSpec {
	return fsrepo.DiskSpec{"type": "s3ds", "endpoint": c.endpoint, "region": c.region, "bucket": c.bucket, "rootDirectory": c.prefix, "owner": c.owner}
}

type credentialFile struct {
	AccessKey    string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	SessionToken string `json:"sessionToken,omitempty"`
}

func (c *datastoreConfig) credentials() (credentialFile, error) {
	if c.credentialsFile == "" {
		value := credentialFile{os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")}
		if value.AccessKey == "" || value.SecretKey == "" {
			return value, errors.New("s3ds: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or configure credentialsFile")
		}
		return value, nil
	}
	info, err := os.Stat(c.credentialsFile)
	if err != nil {
		return credentialFile{}, fmt.Errorf("s3ds: credentials file: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return credentialFile{}, errors.New("s3ds: credentialsFile must be a regular file readable only by its owner (chmod 600)")
	}
	if info.Size() > 64*1024 {
		return credentialFile{}, errors.New("s3ds: credentials file exceeds 64 KiB")
	}
	data, err := os.ReadFile(c.credentialsFile)
	if err != nil {
		return credentialFile{}, fmt.Errorf("s3ds: read credentials file: %w", err)
	}
	var value credentialFile
	if err := json.Unmarshal(data, &value); err != nil {
		return value, errors.New("s3ds: invalid credentials file JSON")
	}
	if value.AccessKey == "" || value.SecretKey == "" {
		return value, errors.New("s3ds: credentials file requires accessKey and secretKey")
	}
	return value, nil
}

func (c *datastoreConfig) Create(string) (repo.Datastore, error) {
	creds, err := c.credentials()
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = c.concurrency * 4
	transport.MaxIdleConnsPerHost = c.concurrency * 2
	transport.MaxConnsPerHost = c.concurrency * 2
	client := s3.New(s3.Options{
		Region: c.region, BaseEndpoint: aws.String(c.endpoint), UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider(creds.AccessKey, creds.SecretKey, creds.SessionToken),
		HTTPClient:                 &http.Client{Transport: transport, Timeout: c.timeout},
		Retryer:                    retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 3 }),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	d := &datastore{client: client, config: c, transport: transport}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	if err := d.claim(ctx); err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	return d, nil
}
