package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestImportStopsWhenCancelledFromAnotherReplica(t *testing.T) {
	addr := os.Getenv("AETHER_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("AETHER_TEST_VALKEY_ADDR is not set")
	}
	kv := redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("AETHER_TEST_VALKEY_PASSWORD")})
	t.Cleanup(func() { kv.Close() })
	s := &Server{kv: kv}
	id := "test-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { kv.Del(context.Background(), importCancelKey(id)) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.watchImportCancel(ctx, cancel, id, 20*time.Millisecond)

	time.Sleep(200 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("the import stopped before anyone cancelled it")
	}
	if err := s.requestImportCancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the import kept running after another replica cancelled it")
	}
}
