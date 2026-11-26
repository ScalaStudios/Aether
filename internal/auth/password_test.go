package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordHashing(t *testing.T) {
	ctx := context.Background()
	h := HashPassword("correct horse battery staple")
	if ok, err := VerifyPassword(ctx, h, "correct horse battery staple"); !ok || err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if ok, _ := VerifyPassword(ctx, h, "wrong"); ok {
		t.Fatal("invalid password accepted")
	}
	if h == HashPassword("correct horse battery staple") {
		t.Fatal("salt not applied")
	}
}

func TestArgonConcurrencyIsBounded(t *testing.T) {
	var inFlight, peak atomic.Int32
	real := idKey
	idKey = func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		return real(password, salt, time, memory, threads, keyLen)
	}
	defer func() { idKey = real }()

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			VerifyPassword(context.Background(), dummyHash, "guess")
		}()
		go func() {
			defer wg.Done()
			HashPassword("new password")
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > argonSlotsN {
		t.Fatalf("%d Argon2 computations ran at once, want at most %d", got, argonSlotsN)
	}
}

func TestArgonBusyFailsFast(t *testing.T) {
	for range cap(argonSlots) {
		argonSlots <- struct{}{}
	}
	defer func() {
		for range cap(argonSlots) {
			<-argonSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := VerifyPassword(ctx, dummyHash, "guess"); !errors.Is(err, ErrPasswordBusy) {
		t.Fatalf("got %v, want ErrPasswordBusy", err)
	}
	if err := BurnPasswordCheck(ctx, "guess"); !errors.Is(err, ErrPasswordBusy) {
		t.Fatalf("burn got %v, want ErrPasswordBusy", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("waited past the request deadline")
	}
}
