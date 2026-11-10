package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/test/cli/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageLocalAndValidation(t *testing.T) {
	h := harness.NewT(t)
	// S3 settings must have no effect when the feature is disabled.
	normal := h.NewNode().Init()
	local := h.NewNode()
	local.Runner.Env["IFSHARE_S3_ENDPOINT"] = "http://127.0.0.1:1"
	local.Runner.Env["IFSHARE_S3_BUCKET"] = "unused"
	local.Init("--storage=local")
	assert.Equal(t, normal.ReadConfig().Datastore.Spec, local.ReadConfig().Datastore.Spec)
	assert.DirExists(t, filepath.Join(local.Dir, "blocks"))
	local.StartDaemon("--storage=local", "--offline")
	cid := local.IPFSAddStr("local storage remains available", "--cid-version=0")
	assert.Equal(t, "local storage remains available", local.IPFS("cat", cid).Stdout.String())
	local.StopDaemon()
	before := local.ReadFile("config")
	result := local.RunIPFS("daemon", "--storage=s3")
	require.NotEqual(t, 0, result.ExitCode())
	assert.Contains(t, result.Stderr.String(), "storage mode differs")
	assert.Equal(t, before, local.ReadFile("config"))
	for _, args := range [][]string{
		{"--storage=invalid"}, {"--storage=s3", "--profile=pebbleds"},
	} {
		node := h.NewNode()
		result := node.RunIPFS(append([]string{"init"}, args...)...)
		require.NotEqual(t, 0, result.ExitCode())
		assert.NoFileExists(t, filepath.Join(node.Dir, "config"))
	}
	missing := h.NewNode()
	missing.Runner.Env["IFSHARE_S3_BUCKET"] = ""
	result = missing.RunIPFS("init", "--storage=s3")
	require.NotEqual(t, 0, result.ExitCode())
	assert.NoFileExists(t, filepath.Join(missing.Dir, "config"))
}

// Opt in with IFSHARE_TEST_S3=1 and the environment produced by
// dev/minio/local.py. Only prefixes created by this test are deleted.
func TestS3Storage(t *testing.T) {
	if os.Getenv("IFSHARE_TEST_S3") != "1" {
		t.Skip("set IFSHARE_TEST_S3=1 to test against MinIO")
	}
	require.NotEmpty(t, os.Getenv("IFSHARE_S3_ENDPOINT"))
	require.NotEmpty(t, os.Getenv("IFSHARE_S3_BUCKET"))
	h := harness.NewT(t)
	node := h.NewNode()
	node.Runner.Env["IFSHARE_S3_PREFIX"] = ""
	node.Init("--storage=s3")
	spec := node.ReadConfig().Datastore.Spec
	blocks := mountAt(t, spec, "/blocks")
	require.Equal(t, "s3ds", blocks["type"])
	assert.Equal(t, "nodes/"+node.PeerID().String(), blocks["rootDirectory"])
	assert.Equal(t, "levelds", mountAt(t, spec, "/")["type"])
	assert.NoDirExists(t, filepath.Join(node.Dir, "blocks"))
	assert.DirExists(t, filepath.Join(node.Dir, "datastore"))
	for _, secretField := range []string{"accessKey", "secretKey", "sessionToken"} {
		assert.NotContains(t, node.IPFS("config", "show").Stdout.String(), `"`+secretField+`"`)
	}
	cleanupS3Prefix(t, node, blocks)

	// Two identities may use the same bucket, but must not share a prefix.
	other := h.NewNode()
	other.Runner.Env["IFSHARE_S3_PREFIX"] = blocks["rootDirectory"].(string)
	result := other.RunIPFS("init", "--storage=s3")
	require.NotEqual(t, 0, result.ExitCode())
	assert.Contains(t, result.Stderr.String(), "belongs to another node")
	assert.NoFileExists(t, filepath.Join(other.Dir, "config"))
	other.Init("--storage=local")
	badCredentials := h.NewNode()
	badCredentials.Runner.Env["IFSHARE_S3_CREDENTIALS_FILE"] = ""
	badCredentials.Runner.Env["AWS_ACCESS_KEY_ID"] = "ifshare-invalid-test-key"
	badCredentials.Runner.Env["AWS_SECRET_ACCESS_KEY"] = "invalid-test-secret"
	result = badCredentials.RunIPFS("init", "--storage=s3")
	require.NotEqual(t, 0, result.ExitCode())
	assert.Contains(t, result.Stderr.String(), "S3 preflight")
	assert.NoFileExists(t, filepath.Join(badCredentials.Dir, "config"))
	assert.NoDirExists(t, filepath.Join(badCredentials.Dir, "blocks"))

	data := make([]byte, 2*1024*1024+123)
	_, err := rand.Read(data)
	require.NoError(t, err)
	node.StartDaemon("--storage=s3", "--offline")
	defer node.StopDaemon()
	cid := node.IPFSAdd(bytes.NewReader(data), "--cid-version=0", "--chunker=size-262144")
	localCID := other.IPFSAdd(bytes.NewReader(data), "--cid-version=0", "--chunker=size-262144")
	assert.Equal(t, localCID, cid, "storage must not change the CID recipe")
	node.IPFS("files", "cp", "/ipfs/"+cid, "/example.bin")
	assert.Contains(t, node.IPFS("files", "ls", "/").Stdout.String(), "example.bin")
	assert.Equal(t, data, node.IPFS("files", "read", "/example.bin").Stdout.Bytes())
	garbage := node.IPFSAddStr("unreferenced S3 test content", "--pin=false", "--cid-version=1")
	node.IPFS("repo", "gc")
	result = node.RunIPFS("block", "stat", "--offline", garbage)
	assert.NotEqual(t, 0, result.ExitCode(), "GC must remove unreferenced objects")
	node.StopDaemon()
	node.StartDaemon("--offline") // persisted mode works without repeating the flag
	assert.Equal(t, data, node.IPFS("cat", cid).Stdout.Bytes())
	assert.Contains(t, node.IPFS("pin", "ls", "-t", "recursive", cid).Stdout.String(), cid)
	assert.Contains(t, node.IPFS("files", "ls", "/").Stdout.String(), "example.bin")
	client := node.GatewayClient()
	response := client.Get("/ipfs/"+cid, client.WithHeader("Range", "bytes=17-1040"))
	assert.Equal(t, 206, response.StatusCode)
	assert.Equal(t, string(data[17:1041]), response.Body)
	var stats struct{ RepoSize uint64 }
	require.NoError(t, json.Unmarshal(node.IPFS("repo", "stat", "--enc=json").Stdout.Bytes(), &stats))
	assert.Greater(t, stats.RepoSize, uint64(len(data)))
	node.StopDaemon()
	before := node.ReadFile("config")
	result = node.RunIPFS("daemon", "--storage=local")
	require.NotEqual(t, 0, result.ExitCode())
	assert.Contains(t, result.Stderr.String(), "storage mode differs")
	assert.Equal(t, before, node.ReadFile("config"))

	// A local node can retrieve newly added S3 content over the unchanged wire.
	for _, n := range []*harness.Node{node, other} {
		n.UpdateConfig(func(c *config.Config) {
			c.Routing.Type = config.NewOptionalString("none")
			c.Provide.Enabled = config.False
		})
		n.StartDaemon()
	}
	defer other.StopDaemon()
	node.Connect(other)
	newCID := node.IPFSAddStr("retrieved from an S3-backed peer", "--cid-version=1")
	assert.Equal(t, "retrieved from an S3-backed peer", other.IPFS("cat", newCID).Stdout.String())
}

func TestS3DaemonInit(t *testing.T) {
	if os.Getenv("IFSHARE_TEST_S3") != "1" {
		t.Skip("set IFSHARE_TEST_S3=1 to test against MinIO")
	}
	h := harness.NewT(t)
	// The template supplies a new identity and random ports, so daemon --init
	// can be exercised without ever binding a real node's default ports.
	template := h.NewNode().Init()
	configPath := filepath.Join(h.Dir, "init-config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(template.ReadFile("config")), 0o600))
	node := h.NewNode()
	node.Runner.Env["IFSHARE_S3_PREFIX"] = ""
	node.StartDaemon("--init", "--storage=s3", "--init-config="+configPath, "--offline")
	defer node.StopDaemon()
	blocks := mountAt(t, node.ReadConfig().Datastore.Spec, "/blocks")
	cleanupS3Prefix(t, node, blocks)
	assert.Equal(t, "s3ds", blocks["type"])
	cid := node.IPFSAddStr("daemon initialized S3", "--cid-version=1")
	assert.Equal(t, "daemon initialized S3", node.IPFS("cat", cid).Stdout.String())
}

func cleanupS3Prefix(t testing.TB, node *harness.Node, spec map[string]any) {
	t.Helper()
	keys := struct {
		AccessKey    string `json:"accessKey"`
		SecretKey    string `json:"secretKey"`
		SessionToken string `json:"sessionToken"`
	}{AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"), SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("AWS_SESSION_TOKEN")}
	if path := os.Getenv("IFSHARE_S3_CREDENTIALS_FILE"); path != "" {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &keys))
	}
	client := s3.New(s3.Options{Region: spec["region"].(string), BaseEndpoint: aws.String(spec["endpoint"].(string)), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(keys.AccessKey, keys.SecretKey, keys.SessionToken)})
	prefix := spec["rootDirectory"].(string) + "/"
	require.True(t, strings.HasPrefix(prefix, "nodes/"))
	t.Cleanup(func() {
		node.StopDaemon()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(spec["bucket"].(string)), Prefix: &prefix})
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if !assert.NoError(t, err) {
				return
			}
			for _, object := range page.Contents {
				_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(spec["bucket"].(string)), Key: object.Key})
				assert.NoError(t, err, fmt.Sprint("clean up test prefix ", prefix))
			}
		}
	})
}

// BenchmarkS3Import compares sequential and bounded parallel commits against
// real S3. Use -benchtime=1x with IFSHARE_TEST_S3=1 for a local MinIO smoke run.
// Timings include importing and pinning, but exclude setup and verification.
func BenchmarkS3Import(b *testing.B) {
	if os.Getenv("IFSHARE_TEST_S3") != "1" {
		b.Skip("set IFSHARE_TEST_S3=1 to benchmark against MinIO")
	}
	const size = 64 * 1024 * 1024
	file, err := os.Create(filepath.Join(b.TempDir(), "random.bin"))
	require.NoError(b, err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.CopyN(io.MultiWriter(file, hash), rand.Reader, size)
	require.NoError(b, err)
	checksum := hash.Sum(nil)
	var firstCID string
	for _, concurrency := range []int{1, 16} {
		b.Run(fmt.Sprintf("concurrency-%d", concurrency), func(b *testing.B) {
			h := harness.New()
			b.Cleanup(h.Cleanup)
			node := h.NewNode()
			node.Runner.Env["IFSHARE_S3_PREFIX"] = ""
			node.Init("--storage=s3")
			var blocks map[string]any
			node.UpdateConfig(func(c *config.Config) {
				for _, raw := range c.Datastore.Spec["mounts"].([]any) {
					mount := raw.(map[string]any)
					if mount["mountpoint"] == "/blocks" {
						mount["concurrency"] = concurrency
						blocks = mount
					}
				}
			})
			require.NotNil(b, blocks)
			cleanupS3Prefix(b, node, blocks)
			node.StartDaemon("--offline")
			b.SetBytes(size)
			b.ResetTimer()
			var cid string
			for range b.N {
				cid = node.IPFSAdd(io.NewSectionReader(file, 0, size), "--cid-version=0", "--chunker=size-262144")
			}
			b.StopTimer()
			if firstCID == "" {
				firstCID = cid
			} else {
				require.Equal(b, firstCID, cid)
			}
			downloaded := sha256.Sum256(node.IPFS("cat", cid).Stdout.Bytes())
			require.Equal(b, checksum, downloaded[:])
			node.StopDaemon()
		})
	}
}
