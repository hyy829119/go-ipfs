package s3ds

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ds "github.com/ipfs/go-datastore"
	q "github.com/ipfs/go-datastore/query"
	dstest "github.com/ipfs/go-datastore/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOwner = "QmYwAPJzv5CZsnAzt8auVTLVtYvUm5TDrsVJJiKmLmrAcE"

// s3Server exercises the SDK's HTTP requests as well as datastore semantics.
// Real signing, persistence and daemon integration are covered by TestS3Storage.
type s3Server struct {
	mu       sync.Mutex
	objects  map[string][]byte
	lists    atomic.Int64
	heads    atomic.Int64
	deny     atomic.Bool
	failPage atomic.Bool
	putHook  func()
}

func (s *s3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.deny.Load() || (s.failPage.Load() && r.URL.Query().Get("continuation-token") != "") {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code></Error>")
		return
	}
	if s.putHook != nil && r.Method == http.MethodPut {
		s.putHook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
	if r.URL.Query().Get("list-type") == "2" {
		s.lists.Add(1)
		type item struct {
			Key  string
			Size int
		}
		result := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			Contents              []item   `xml:"Contents"`
			IsTruncated           bool
			NextContinuationToken string `xml:",omitempty"`
		}{}
		keys := slices.Sorted(maps.Keys(s.objects))
		prefix, after := r.URL.Query().Get("prefix"), r.URL.Query().Get("continuation-token")
		for _, k := range keys {
			if !strings.HasPrefix(k, prefix) || k <= after {
				continue
			}
			if len(result.Contents) == 1000 {
				result.IsTruncated = true
				result.NextContinuationToken = result.Contents[999].Key
				break
			}
			result.Contents = append(result.Contents, item{Key: k, Size: len(s.objects[k])})
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(result)
		return
	}
	value, exists := s.objects[key]
	switch r.Method {
	case http.MethodPut:
		if exists && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.objects[key] = data
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		if r.Method == http.MethodHead {
			s.heads.Add(1)
		}
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(value)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(value)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func testConfig(endpoint string) map[string]any {
	return map[string]any{"endpoint": endpoint, "bucket": "test-bucket", "region": "us-east-1", "rootDirectory": "nodes/test", "owner": testOwner}
}

func newTestStore(t *testing.T) (*datastore, *s3Server) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	backend := &s3Server{objects: make(map[string][]byte)}
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)
	c, err := parseConfig(testConfig(server.URL))
	require.NoError(t, err)
	store, err := c.Create("")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store.(*datastore), backend
}

func TestDatastoreContract(t *testing.T) {
	store, _ := newTestStore(t)
	dstest.SubtestAll(t, store)
}

func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"endpoint", "ftp://example.com"}, {"endpoint", "https://user:secret@example.com"},
		{"endpoint", "https://example.com?secret=1"}, {"bucket", "../bad"}, {"bucket", ""},
		{"rootDirectory", ".."}, {"rootDirectory", "/node"}, {"rootDirectory", "a/../b"},
		{"rootDirectory", ""}, {"owner", "not-a-peer"}, {"region", ""},
		{"concurrency", 0}, {"concurrency", 129}, {"concurrency", 1.5},
		{"requestTimeout", "0s"}, {"requestTimeout", "invalid"}, {"usageCacheTTL", "-1s"},
		{"credentialsFile", "relative.json"}, {"credentialsFile", 12}, {"accessKey", "inline-secret"},
		{"secretKey", "inline-secret"}, {"sessionToken", "inline-secret"},
	} {
		t.Run(fmt.Sprint(tc.key, "=", tc.value), func(t *testing.T) {
			params := testConfig("https://s3.example.com")
			params[tc.key] = tc.value
			_, err := parseConfig(params)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "inline-secret")
		})
	}
	params := testConfig("https://s3.example.com/")
	params["credentialsFile"] = filepath.Join(t.TempDir(), "credentials.json")
	params["usageCacheTTL"] = "0s"
	c, err := parseConfig(params)
	require.NoError(t, err)
	assert.Equal(t, "https://s3.example.com", c.DiskSpec()["endpoint"])
	assert.NotContains(t, c.DiskSpec(), "credentialsFile")
	assert.NotContains(t, c.DiskSpec(), "concurrency")
}

func TestCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	c := &datastoreConfig{}
	_, err := c.credentials()
	require.ErrorContains(t, err, "AWS_ACCESS_KEY_ID")
	c.credentialsFile = filepath.Join(t.TempDir(), "credentials.json")
	require.NoError(t, os.WriteFile(c.credentialsFile, []byte(`{"accessKey":"key","secretKey":"secret"}`), 0o600))
	creds, err := c.credentials()
	require.NoError(t, err)
	assert.Equal(t, "key", creds.AccessKey)
	assert.Equal(t, "secret", creds.SecretKey)
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(c.credentialsFile, 0o644))
		_, err = c.credentials()
		require.ErrorContains(t, err, "chmod 600")
		require.NoError(t, os.Chmod(c.credentialsFile, 0o600))
	}
	require.NoError(t, os.WriteFile(c.credentialsFile, []byte(`invalid-secret-data`), 0o600))
	_, err = c.credentials()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid-secret-data")
}

func TestPrefixOwnershipAndAccessErrors(t *testing.T) {
	store, backend := newTestStore(t)
	require.NoError(t, store.claim(t.Context()), "same node can reopen its prefix")
	backend.mu.Lock()
	backend.objects["nodes/test/.ifshare-owner"] = []byte("another-node")
	backend.mu.Unlock()
	require.ErrorContains(t, store.claim(t.Context()), "belongs to another node")
	backend.deny.Store(true)
	_, err := store.Has(t.Context(), ds.NewKey("missing"))
	require.Error(t, err, "403 must not be reported as a missing block")
	assert.NotErrorIs(t, err, ds.ErrNotFound)
	require.Error(t, store.Put(t.Context(), ds.NewKey("data"), []byte("x")))
	_, err = store.DiskUsage(t.Context())
	require.Error(t, err, "list failure must not be reported as an empty repo")
}

func TestStreamingPagesAndUsageCache(t *testing.T) {
	store, backend := newTestStore(t)
	backend.mu.Lock()
	for i := range 1005 {
		backend.objects[fmt.Sprintf("nodes/test/blocks/%04d", i)] = []byte("abc")
	}
	backend.objects["nodes/another/blocks/foreign"] = []byte("not part of this repo")
	backend.mu.Unlock()
	results, err := store.Query(t.Context(), q.Query{KeysOnly: true, Orders: []q.Order{q.OrderByKey{}}, Limit: 2})
	require.NoError(t, err)
	entries, err := results.Rest()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "/0000", entries[0].Key)
	assert.EqualValues(t, 1, backend.lists.Load(), "a small ordered query must not load every page")
	usage, err := store.DiskUsage(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 3015, usage)
	calls := backend.lists.Load()
	_, err = store.DiskUsage(t.Context())
	require.NoError(t, err)
	assert.Equal(t, calls, backend.lists.Load(), "UI polling should use the cached total")
	require.NoError(t, store.Delete(t.Context(), ds.NewKey("0000")))
	usage, err = store.DiskUsage(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 3012, usage)
	backend.failPage.Store(true)
	results, err = store.Query(t.Context(), q.Query{KeysOnly: true})
	require.NoError(t, err)
	_, err = results.Rest()
	require.Error(t, err, "failure on a later page must reach the caller")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results, err = store.Query(ctx, q.Query{KeysOnly: true})
	require.NoError(t, err)
	_, err = results.Rest()
	require.ErrorIs(t, err, context.Canceled)
}

func TestBatchConcurrencyAndRelease(t *testing.T) {
	store, backend := newTestStore(t)
	store.config.concurrency = 4
	started, release := make(chan struct{}, 32), make(chan struct{})
	var active, peak atomic.Int64
	backend.putHook = func() {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	b, err := store.Batch(t.Context())
	require.NoError(t, err)
	for i := range 32 {
		value := []byte("original")
		require.NoError(t, b.Put(t.Context(), ds.NewKey(fmt.Sprint(i)), value))
		value[0] = 'X'
	}
	done := make(chan error, 1)
	go func() { done <- b.Commit(t.Context()) }()
	for range 4 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("batch did not issue concurrent writes")
		}
	}
	once.Do(func() { close(release) })
	require.NoError(t, <-done)
	assert.EqualValues(t, 4, peak.Load())
	assert.Empty(t, b.(*batch).ops, "committed buffers must be released")
	assert.Zero(t, backend.heads.Load(), "PUT must not perform a HEAD first")
	value, err := store.Get(t.Context(), ds.NewKey("0"))
	require.NoError(t, err)
	assert.Equal(t, "original", string(value))
	backend.deny.Store(true)
	require.NoError(t, b.Put(t.Context(), ds.NewKey("fail"), []byte("data")))
	require.Error(t, b.Commit(t.Context()))
	assert.Empty(t, b.(*batch).ops)
	require.NoError(t, b.Put(t.Context(), ds.NewKey("cancel"), []byte("data")))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, b.Commit(ctx), context.Canceled)
	assert.Empty(t, b.(*batch).ops)
}
