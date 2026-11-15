package kv

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Valkey is the production KV.
type Valkey struct {
	Client valkey.Client
}

// Dial connects to a valkey:// or redis:// URL.
func Dial(url string) (*Valkey, error) {
	opt, err := valkey.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse VALKEY_URL: %w", err)
	}
	client, err := valkey.NewClient(opt)
	if err != nil {
		return nil, fmt.Errorf("connect valkey: %w", err)
	}
	return &Valkey{Client: client}, nil
}

// Get implements KV.
func (v *Valkey) Get(ctx context.Context, key string) (string, bool, error) {
	s, err := v.Client.Do(ctx, v.Client.B().Get().Key(key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", false, nil
	}
	return s, err == nil, err
}

// Set implements KV.
func (v *Valkey) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	cmd := v.Client.B().Set().Key(key).Value(value)
	if ttl > 0 {
		return v.Client.Do(ctx, cmd.Px(ttl).Build()).Error()
	}
	return v.Client.Do(ctx, cmd.Build()).Error()
}

// Del implements KV.
func (v *Valkey) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return v.Client.Do(ctx, v.Client.B().Del().Key(keys...).Build()).Error()
}

// Incr implements KV: INCR, then PEXPIRE NX so the window starts at the first hit.
func (v *Valkey) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return v.Client.Do(ctx, v.Client.B().Incr().Key(key).Build()).AsInt64()
	}
	results := v.Client.DoMulti(ctx,
		v.Client.B().Incr().Key(key).Build(),
		v.Client.B().Pexpire().Key(key).Milliseconds(ttl.Milliseconds()).Nx().Build())
	n, err := results[0].AsInt64()
	if err != nil {
		return 0, err
	}
	return n, results[1].Error()
}

// Publish implements KV.
func (v *Valkey) Publish(ctx context.Context, channel, message string) error {
	return v.Client.Do(ctx, v.Client.B().Publish().Channel(channel).Message(message).Build()).Error()
}

// Ping implements KV.
func (v *Valkey) Ping(ctx context.Context) error {
	return v.Client.Do(ctx, v.Client.B().Ping().Build()).Error()
}

// Close implements KV.
func (v *Valkey) Close() { v.Client.Close() }

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func formatInt(n int64) string { return strconv.FormatInt(n, 10) }
