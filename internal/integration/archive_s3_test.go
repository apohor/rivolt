//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/archive/s3"
)

// startMinIO runs MinIO and returns its endpoint. MinIO no longer publishes
// images to Docker Hub (minio/minio pulls are refused), so this is Chainguard's
// maintained build, pinned by digest; it runs as non-root, hence /tmp.
func startMinIO(ctx context.Context, t *testing.T) string {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "cgr.dev/chainguard/minio@sha256:709099319add48dfb44aa596a7070d3ddd8dd04d7ae881765719aec29df9e510",
			Cmd:          []string{"server", "/tmp/minio-data", "--address=:9000"},
			ExposedPorts: []string{"9000/tcp"},
			Env:          map[string]string{"MINIO_ROOT_USER": "rivolt-test", "MINIO_ROOT_PASSWORD": "rivolt-test-secret"},
			WaitingFor:   wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// TestArchiveS3Client: every verb the archive uses against a real MinIO:
// create, put (signed payload), get, list across continuation pages, delete
// (including a missing key), not-found, and a bad secret refused.
func TestArchiveS3Client(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := &s3.Client{Endpoint: startMinIO(ctx, t), Region: "us-east-1", Bucket: "rivolt-test-archive",
		AccessKey: "rivolt-test", SecretKey: "rivolt-test-secret", ListPageSize: 10}
	// /minio/health/ready can answer before the object layer is up; MinIO
	// then refuses requests with XMinioServerNotInitialized for a moment.
	for i := 0; ; i++ {
		err := c.CreateBucket(ctx)
		if err == nil {
			break
		}
		if i == 30 || !strings.Contains(err.Error(), "XMinioServerNotInitialized") {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	key := func(i int) string { return fmt.Sprintf("prod/v1/u=user-1/v=veh-%02d/2026-05.csv.gz.enc", i) }
	for i := 0; i < 25; i++ {
		body := bytes.Repeat([]byte{byte(i)}, 1000+i)
		if err := c.Put(ctx, key(i), body, "application/octet-stream"); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	// Another prefix the listing must not return.
	if err := c.Put(ctx, "prod/v1/u=user-2/v=veh-00/2026-05.csv.gz.enc", []byte("other"), ""); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Get(ctx, key(7))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, bytes.Repeat([]byte{7}, 1007)) {
		t.Errorf("get returned %d bytes", len(got))
	}
	var listed []s3.Object
	if err := c.List(ctx, "prod/v1/u=user-1/", func(o s3.Object) error { listed = append(listed, o); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 25 || listed[0].Key != key(0) || listed[24].Size != 1024 {
		t.Fatalf("listed %d objects across pages of 10: %+v", len(listed), listed)
	}
	for i := 0; i < 25; i++ {
		if err := c.Delete(ctx, key(i)); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if err := c.Delete(ctx, key(0)); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
	if _, err := c.Get(ctx, key(3)); !errors.Is(err, s3.ErrNotFound) {
		t.Errorf("get after delete: %v, want ErrNotFound", err)
	}
	n := 0
	if err := c.List(ctx, "prod/v1/u=user-1/", func(s3.Object) error { n++; return nil }); err != nil || n != 0 {
		t.Errorf("after deleting all: %d left, %v", n, err)
	}
	bad := *c
	bad.SecretKey = "wrong"
	if err := bad.Put(ctx, "prod/v1/x", []byte("x"), ""); err == nil {
		t.Error("a request signed with the wrong secret was accepted")
	}
}
