package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonWait    = 3 * time.Second
	argonSlotsN  = 2
)

var (
	ErrPasswordBusy = errors.New("password hashing is busy")
	argonSlots      = make(chan struct{}, argonSlotsN)
	idKey           = argon2.IDKey
)

func acquireArgon(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, argonWait)
	defer cancel()
	select {
	case argonSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrPasswordBusy
	}
}

func boundedIDKey(ctx context.Context, password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) ([]byte, error) {
	if err := acquireArgon(ctx); err != nil {
		return nil, err
	}
	defer func() { <-argonSlots }()
	return idKey(password, salt, time, memory, threads, keyLen), nil
}

func HashPassword(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	argonSlots <- struct{}{}
	key := idKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-argonSlots
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func VerifyPassword(ctx context.Context, encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported password hash")
	}
	var version int
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got, err := boundedIDKey(ctx, []byte(password), salt, time, memory, threads, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var dummyHash = HashPassword("aether-timing-equalizer")

func BurnPasswordCheck(ctx context.Context, password string) error {
	_, err := VerifyPassword(ctx, dummyHash, password)
	return err
}
