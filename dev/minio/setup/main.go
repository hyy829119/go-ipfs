// Command setup creates the development bucket using the local MinIO credentials.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func setup() error {
	data, err := os.ReadFile(os.Getenv("IFSHARE_S3_CREDENTIALS_FILE"))
	if err != nil {
		return err
	}
	var keys struct {
		AccessKey string `json:"accessKey"`
		SecretKey string `json:"secretKey"`
	}
	if err := json.Unmarshal(data, &keys); err != nil {
		return errors.New("invalid credentials file")
	}
	client := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(os.Getenv("IFSHARE_S3_ENDPOINT")), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(keys.AccessKey, keys.SecretKey, "")})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(os.Getenv("IFSHARE_S3_BUCKET"))})
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyOwnedByYou" {
		return nil
	}
	return err
}

func main() {
	if err := setup(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("MinIO bucket is ready.")
}
