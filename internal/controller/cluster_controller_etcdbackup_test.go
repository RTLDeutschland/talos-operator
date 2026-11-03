package controller

import (
	"context"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/rand"
)

// TestMinioClientFailureBehavior verifies that the Minio client behaves as we expect it to.
func TestMinioClientFailureBehavior(t *testing.T) {
	// expect no error from invalid S3 endpoint creation
	s3Client, err := minio.New("http://invalid.example", &minio.Options{
		Creds:  credentials.NewStaticV4("invalid", "invalid", ""),
		Region: "us-east-1",
		Secure: false,
	})
	require.NoError(t, err, "expected no error when creating S3 client")

	// stat should fail
	testKey := ".connection-test-" + rand.String(16)
	ctx := context.TODO()
	_, err = s3Client.StatObject(ctx, "test-bucket-foo", testKey, minio.StatObjectOptions{})
	require.Error(t, err, "expected an error when statting object with invalid S3 client")
	require.True(
		t,
		minio.IsNetworkOrHostDown(err, false),
		"expected network or host down error for invalid S3 client",
	)
}
