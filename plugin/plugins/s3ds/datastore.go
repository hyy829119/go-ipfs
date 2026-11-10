package s3ds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	ds "github.com/ipfs/go-datastore"
	q "github.com/ipfs/go-datastore/query"
	"golang.org/x/sync/errgroup"
)

type datastore struct {
	client    *s3.Client
	config    *datastoreConfig
	transport *http.Transport
	usageMu   sync.Mutex
	usage     uint64
	usageAt   time.Time
}

var _ ds.Batching = (*datastore)(nil)
var _ ds.PersistentDatastore = (*datastore)(nil)

func apiCode(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return slices.Contains(codes, apiErr.ErrorCode())
}

// A separate control object prevents two different repositories from sharing
// a prefix: their independent pin sets make a shared GC sweep unsafe.
func (d *datastore) claim(ctx context.Context) error {
	key := d.config.prefix + "/.ifshare-owner"
	_, err := d.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(d.config.bucket), Key: &key, Body: strings.NewReader(d.config.owner), IfNoneMatch: aws.String("*")})
	if err == nil {
		return nil
	}
	if !apiCode(err, "PreconditionFailed", "ConditionalRequestConflict") {
		return fmt.Errorf("s3ds: claim repository prefix: %w", err)
	}
	result, err := d.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(d.config.bucket), Key: &key})
	if err != nil {
		return fmt.Errorf("s3ds: read repository owner: %w", err)
	}
	defer result.Body.Close()
	owner, err := io.ReadAll(io.LimitReader(result.Body, 1024))
	if err != nil {
		return fmt.Errorf("s3ds: read repository owner: %w", err)
	}
	if string(owner) != d.config.owner {
		return errors.New("s3ds: object prefix belongs to another node; use a separate rootDirectory for each node")
	}
	return nil
}

func (d *datastore) objectKey(key ds.Key) string { return d.config.prefix + "/blocks" + key.String() }

func (d *datastore) Put(ctx context.Context, key ds.Key, value []byte) error {
	_, err := d.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(d.config.bucket), Key: aws.String(d.objectKey(key)), Body: bytes.NewReader(value)})
	if err != nil {
		return fmt.Errorf("s3ds: write block: %w", err)
	}
	return nil
}

func (d *datastore) Get(ctx context.Context, key ds.Key) ([]byte, error) {
	result, err := d.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(d.config.bucket), Key: aws.String(d.objectKey(key))})
	if apiCode(err, "NoSuchKey", "NotFound") {
		return nil, ds.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("s3ds: read block: %w", err)
	}
	defer result.Body.Close()
	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("s3ds: read block body: %w", err)
	}
	return data, nil
}

func (d *datastore) GetSize(ctx context.Context, key ds.Key) (int, error) {
	result, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(d.config.bucket), Key: aws.String(d.objectKey(key))})
	if apiCode(err, "NoSuchKey", "NotFound") {
		return -1, ds.ErrNotFound
	}
	if err != nil {
		return -1, fmt.Errorf("s3ds: stat block: %w", err)
	}
	return int(aws.ToInt64(result.ContentLength)), nil
}

func (d *datastore) Has(ctx context.Context, key ds.Key) (bool, error) {
	_, err := d.GetSize(ctx, key)
	if errors.Is(err, ds.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (d *datastore) Delete(ctx context.Context, key ds.Key) error {
	_, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(d.config.bucket), Key: aws.String(d.objectKey(key))})
	if err != nil {
		return fmt.Errorf("s3ds: delete block: %w", err)
	}
	d.usageMu.Lock()
	d.usageAt = time.Time{}
	d.usageMu.Unlock()
	return nil
}

// S3 acknowledges durable writes; Sync adds no network round trip.
func (d *datastore) Sync(ctx context.Context, _ ds.Key) error { return ctx.Err() }
func (d *datastore) Close() error                             { d.transport.CloseIdleConnections(); return nil }

func (d *datastore) Query(ctx context.Context, query q.Query) (q.Results, error) {
	ctx, cancel := context.WithCancel(ctx)
	root := d.config.prefix + "/blocks"
	prefix := root + "/"
	if cleaned := ds.NewKey(query.Prefix).String(); cleaned != "/" {
		prefix = root + cleaned + "/"
	}
	pages := s3.NewListObjectsV2Paginator(d.client, &s3.ListObjectsV2Input{Bucket: aws.String(d.config.bucket), Prefix: &prefix, MaxKeys: aws.Int32(1000)})
	var page *s3.ListObjectsV2Output
	index, done := 0, false
	results := q.ResultsFromIterator(query, q.Iterator{
		Next: func() (q.Result, bool) {
			if done {
				return q.Result{}, false
			}
			for page == nil || index >= len(page.Contents) {
				if !pages.HasMorePages() {
					done = true
					cancel()
					return q.Result{}, false
				}
				var err error
				page, err = pages.NextPage(ctx)
				if err != nil {
					done = true
					cancel()
					return q.Result{Error: fmt.Errorf("s3ds: list blocks: %w", err)}, true
				}
				index = 0
			}
			object := page.Contents[index]
			index++
			entry := q.Entry{Key: strings.TrimPrefix(aws.ToString(object.Key), root), Size: int(aws.ToInt64(object.Size))}
			if !query.KeysOnly {
				value, err := d.Get(ctx, ds.NewKey(entry.Key))
				if err != nil {
					done = true
					cancel()
					return q.Result{Error: err}, true
				}
				entry.Value = value
			}
			return q.Result{Entry: entry}, true
		},
		Close: func() error { cancel(); return nil },
	})
	apply := query
	// S3 already streams keys in ascending order. Avoid buffering all keys for
	// the mount datastore's merge query and blockstore enumeration.
	if len(apply.Orders) == 1 {
		switch apply.Orders[0].(type) {
		case q.OrderByKey, *q.OrderByKey:
			apply.Orders = nil
		}
	}
	return q.NaiveQueryApply(apply, results), nil
}

func (d *datastore) DiskUsage(ctx context.Context) (uint64, error) {
	d.usageMu.Lock()
	defer d.usageMu.Unlock()
	if !d.usageAt.IsZero() && time.Since(d.usageAt) < d.config.usageTTL {
		return d.usage, nil
	}
	results, err := d.Query(ctx, q.Query{KeysOnly: true, ReturnsSizes: true})
	if err != nil {
		return 0, err
	}
	defer results.Close()
	var size uint64
	for result := range results.Next() {
		if result.Error != nil {
			return 0, result.Error
		}
		size += uint64(result.Size)
	}
	d.usage, d.usageAt = size, time.Now()
	return size, nil
}

type operation struct {
	value  []byte
	remove bool
}
type batch struct {
	store *datastore
	ops   map[ds.Key]operation
}

func (d *datastore) Batch(context.Context) (ds.Batch, error) {
	return &batch{store: d, ops: make(map[ds.Key]operation)}, nil
}
func (b *batch) Put(_ context.Context, key ds.Key, value []byte) error {
	b.ops[key] = operation{value: bytes.Clone(value)}
	return nil
}
func (b *batch) Delete(_ context.Context, key ds.Key) error {
	b.ops[key] = operation{remove: true}
	return nil
}
func (b *batch) Commit(ctx context.Context) error {
	defer clear(b.ops)
	parent := ctx
	if err := parent.Err(); err != nil {
		return err
	}
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(b.store.config.concurrency)
	for key, op := range b.ops {
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error {
			if op.remove {
				return b.store.Delete(ctx, key)
			}
			return b.store.Put(ctx, key, op.value)
		})
	}
	err := group.Wait()
	if err != nil {
		return err
	}
	return parent.Err()
}
