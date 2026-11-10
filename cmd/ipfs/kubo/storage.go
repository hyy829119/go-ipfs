package kubo

import (
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/repo/fsrepo"
)

const storageOptionName = "storage"

func validateStorageMode(mode string) error {
	if mode != "" && mode != "local" && mode != "s3" {
		return errors.New("storage must be local or s3")
	}
	return nil
}

func usesS3(spec map[string]any) bool {
	if spec["type"] == "s3ds" {
		return true
	}
	if child, ok := spec["child"].(map[string]any); ok && usesS3(child) {
		return true
	}
	if mounts, ok := spec["mounts"].([]any); ok {
		for _, item := range mounts {
			if mount, ok := item.(map[string]any); ok && usesS3(mount) {
				return true
			}
		}
	}
	return false
}

func checkStorageMode(conf *config.Config, mode string) error {
	if err := validateStorageMode(mode); err != nil {
		return err
	}
	if mode != "" && (mode == "s3") != usesS3(conf.Datastore.Spec) {
		return errors.New("storage mode differs from this repository; create a separate repository and migrate data before switching")
	}
	return nil
}

func configureStorage(conf *config.Config, mode, repoRoot string) error {
	if err := validateStorageMode(mode); err != nil {
		return err
	}
	if mode == "" {
		return nil
	}
	if mode == "local" {
		return checkStorageMode(conf, mode)
	}
	if !reflect.DeepEqual(conf.Datastore.Spec, config.DefaultDatastoreConfig().Spec) {
		return errors.New("--storage=s3 cannot be combined with a custom datastore config or datastore profile")
	}
	prefix := os.Getenv("IFSHARE_S3_PREFIX")
	if prefix == "" {
		prefix = "nodes/" + conf.Identity.PeerID
	}
	region := os.Getenv("IFSHARE_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	blockSpec := map[string]any{
		"type": "s3ds", "endpoint": os.Getenv("IFSHARE_S3_ENDPOINT"),
		"bucket": os.Getenv("IFSHARE_S3_BUCKET"), "region": region,
		"rootDirectory": prefix, "owner": conf.Identity.PeerID,
		"credentialsFile": os.Getenv("IFSHARE_S3_CREDENTIALS_FILE"),
		"concurrency":     16, "requestTimeout": "30s", "usageCacheTTL": "30s",
	}
	// Check credentials, bucket access and ownership before creating config or
	// datastore_spec, so a failed connection can be corrected and init retried.
	dsc, err := fsrepo.AnyDatastoreConfig(blockSpec)
	if err != nil {
		return fmt.Errorf("S3 configuration: %w", err)
	}
	ds, err := dsc.Create(repoRoot)
	if err != nil {
		return fmt.Errorf("S3 preflight: %w", err)
	}
	if err := ds.Close(); err != nil {
		return fmt.Errorf("S3 preflight close: %w", err)
	}
	blockSpec["mountpoint"] = "/blocks"
	conf.Datastore.Spec = map[string]any{"type": "mount", "mounts": []any{
		blockSpec,
		map[string]any{"mountpoint": "/", "type": "levelds", "path": "datastore", "compression": "none"},
	}}
	return nil
}
