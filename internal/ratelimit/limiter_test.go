package ratelimit_test

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/ratelimit"
)

func TestBurstCapacity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config ratelimit.Config
		want   int
	}{
		{name: "default", config: ratelimit.Config{RPM: 3}, want: 3},
		{name: "below RPM", config: ratelimit.Config{RPM: 60, Burst: new(2)}, want: 2},
		{name: "above RPM", config: ratelimit.Config{RPM: 1, Burst: new(4)}, want: 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := configured(t, tt.config)

				for i := range tt.want {
					if err := limiter.TryAcquire("key"); err != nil {
						t.Fatalf("acquisition %d: %v", i, err)
					}
				}
				if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
					t.Fatalf("exhausted acquisition = %v, want ErrLimitReached", err)
				}
			})
		})
	}
}

func TestRefillBoundaryAndIdleCeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := configured(t, ratelimit.Config{RPM: 60, Burst: new(1)})
		if err := limiter.TryAcquire("key"); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Second - time.Millisecond)
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("before refill = %v, want ErrLimitReached", err)
		}
		time.Sleep(time.Millisecond)
		if err := limiter.TryAcquire("key"); err != nil {
			t.Fatalf("at refill = %v, want success", err)
		}
		time.Sleep(time.Hour)
		if err := limiter.TryAcquire("key"); err != nil {
			t.Fatalf("after idle = %v, want success", err)
		}
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("above idle ceiling = %v, want ErrLimitReached", err)
		}
	})
}

func TestPositiveUpdatePreservesFractionalRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := configured(t, ratelimit.Config{RPM: 60, Burst: new(1)})
		if err := limiter.TryAcquire("key"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(750 * time.Millisecond)

		if err := limiter.Configure("key", ratelimit.Config{RPM: 120, Burst: new(4)}); err != nil {
			t.Fatal(err)
		}
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("after burst increase = %v, want ErrLimitReached", err)
		}
		time.Sleep(124 * time.Millisecond)
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("before new-rate refill = %v, want ErrLimitReached", err)
		}
		time.Sleep(time.Millisecond)
		if err := limiter.TryAcquire("key"); err != nil {
			t.Fatalf("0.75 old-rate + 0.25 new-rate permit = %v, want success", err)
		}
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("second permit = %v, want ErrLimitReached", err)
		}
	})
}

func TestBurstShrinkThenGrowDiscardsExcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := configured(t, ratelimit.Config{RPM: 60, Burst: new(10)})
		for range 3 {
			if err := limiter.TryAcquire("key"); err != nil {
				t.Fatal(err)
			}
		}

		for _, burst := range []int{2, 20} {
			if err := limiter.Configure("key", ratelimit.Config{RPM: 60, Burst: &burst}); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if err := limiter.TryAcquire("key"); err != nil {
				t.Fatal(err)
			}
		}
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("third permit after shrink/regrow = %v, want ErrLimitReached", err)
		}
	})
}

func TestUpdateBurstDefaultAndOverride(t *testing.T) {
	for _, tt := range []struct {
		name  string
		prior *int
		burst *int
		want  int
	}{
		{name: "default follows RPM", want: 6},
		{name: "explicit remains", prior: new(2), burst: new(2), want: 2},
		{name: "omitting explicit restores default", prior: new(2), want: 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := configured(t, ratelimit.Config{RPM: 3, Burst: tt.prior})

				if err := limiter.Configure("key", ratelimit.Config{RPM: 6, Burst: tt.burst}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Minute)
				for i := range tt.want {
					if err := limiter.TryAcquire("key"); err != nil {
						t.Fatalf("acquisition %d: %v", i, err)
					}
				}
				if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
					t.Fatalf("above updated burst = %v, want ErrLimitReached", err)
				}
			})
		})
	}
}

func TestInitiallyDisabledModes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		rpm     int
		wantErr error
	}{
		{name: "zero", rpm: 0, wantErr: ratelimit.ErrLimitReached},
		{name: "unlimited", rpm: ratelimit.Unlimited},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := configured(t, ratelimit.Config{RPM: tt.rpm})

				err := limiter.TryAcquire("key")
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("initial acquisition = %v, want %v", err, tt.wantErr)
				}
				if err := limiter.Configure("key", ratelimit.Config{RPM: 1}); err != nil {
					t.Fatal(err)
				}
				if err := limiter.TryAcquire("key"); err != nil {
					t.Fatalf("first positive bucket = %v, want full bucket", err)
				}
			})
		})
	}
}

func TestDisabledModesResetBucket(t *testing.T) {
	for _, tt := range []struct {
		name    string
		rpm     int
		wantErr error
	}{
		{name: "zero", rpm: 0, wantErr: ratelimit.ErrLimitReached},
		{name: "unlimited", rpm: ratelimit.Unlimited},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := configured(t, ratelimit.Config{RPM: 60, Burst: new(2)})
				if err := limiter.TryAcquire("key"); err != nil {
					t.Fatal(err)
				}

				if err := limiter.Configure("key", ratelimit.Config{RPM: tt.rpm, Burst: new(1)}); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					err := limiter.TryAcquire("key")
					if !errors.Is(err, tt.wantErr) {
						t.Fatalf("disabled acquisition = %v, want %v", err, tt.wantErr)
					}
				}
				if err := limiter.Configure("key", ratelimit.Config{RPM: 60, Burst: new(2)}); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := limiter.TryAcquire("key"); err != nil {
						t.Fatalf("reset bucket = %v, want success", err)
					}
				}
				if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
					t.Fatalf("after reset burst = %v, want ErrLimitReached", err)
				}
			})
		})
	}
}

func TestInvalidConfigurationLeavesStateUnchanged(t *testing.T) {
	for _, tt := range []struct {
		name   string
		key    accesskey.ID
		config ratelimit.Config
	}{
		{name: "blank ID", key: " \t", config: ratelimit.Config{RPM: 60}},
		{name: "negative RPM", key: "key", config: ratelimit.Config{RPM: -2}},
		{name: "zero burst", key: "key", config: ratelimit.Config{RPM: 60, Burst: new(0)}},
		{name: "negative burst", key: "key", config: ratelimit.Config{RPM: 60, Burst: new(-1)}},
		{name: "zero RPM invalid burst", key: "key", config: ratelimit.Config{RPM: 0, Burst: new(0)}},
		{name: "unlimited invalid burst", key: "key", config: ratelimit.Config{RPM: -1, Burst: new(-1)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := configured(t, ratelimit.Config{RPM: 60, Burst: new(1)})
				if err := limiter.TryAcquire("key"); err != nil {
					t.Fatal(err)
				}

				if err := limiter.Configure(tt.key, tt.config); err == nil || errors.Is(err, ratelimit.ErrLimitReached) {
					t.Fatalf("invalid configuration = %v, want validation error", err)
				}
				if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
					t.Fatalf("balance after invalid update = %v, want ErrLimitReached", err)
				}
				time.Sleep(time.Second)
				if err := limiter.TryAcquire("key"); err != nil {
					t.Fatalf("refill after invalid update = %v, want success", err)
				}
			})
		})
	}
}

func TestNumericBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		max := int64(1 << 53)
		if strconv.IntSize == 32 {
			max = 1<<31 - 1
		}
		limiter := ratelimit.New()

		if err := limiter.Configure("key", ratelimit.Config{RPM: int(max), Burst: new(int(max))}); err != nil {
			t.Fatalf("maximum value = %v, want success", err)
		}
		if strconv.IntSize == 64 {
			for _, config := range []ratelimit.Config{
				{RPM: int(max + 1)},
				{RPM: 0, Burst: new(int(max + 1))},
				{RPM: ratelimit.Unlimited, Burst: new(int(max + 1))},
			} {
				limiter := configured(t, ratelimit.Config{RPM: 1})
				if err := limiter.Configure("key", config); err == nil {
					t.Fatal("value above 2^53 accepted")
				}
				if err := limiter.TryAcquire("key"); err != nil {
					t.Fatalf("after invalid update = %v, want success", err)
				}
				if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
					t.Fatalf("after original burst = %v, want ErrLimitReached", err)
				}
			}
		}
	})
}

func TestKeyAndInstanceIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := configured(t, ratelimit.Config{RPM: 1})
		if err := limiter.Configure(" key ", ratelimit.Config{RPM: 1}); err != nil {
			t.Fatal(err)
		}
		restarted := configured(t, ratelimit.Config{RPM: 1})

		for _, key := range []accesskey.ID{"key", " key "} {
			if err := limiter.TryAcquire(key); err != nil {
				t.Fatalf("key %q = %v, want success", key, err)
			}
		}
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("original instance = %v, want ErrLimitReached", err)
		}
		if err := restarted.TryAcquire("key"); err != nil {
			t.Fatalf("new instance = %v, want full bucket", err)
		}
	})
}

func TestConfigureDoesNotRetainBurstPointer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		burst := 1
		limiter := configured(t, ratelimit.Config{RPM: 60, Burst: &burst})

		burst = 10
		if err := limiter.TryAcquire("key"); err != nil {
			t.Fatal(err)
		}
		if err := limiter.TryAcquire("key"); !errors.Is(err, ratelimit.ErrLimitReached) {
			t.Fatalf("after caller mutation = %v, want ErrLimitReached", err)
		}
	})
}

func TestRemoveAndUnknownKeys(t *testing.T) {
	limiter := configured(t, ratelimit.Config{RPM: 0})

	for _, key := range []accesskey.ID{"key", "key", "unknown", "", "\t"} {
		limiter.Remove(key)
		if err := limiter.TryAcquire(key); !errors.Is(err, ratelimit.ErrNotConfigured) {
			t.Fatalf("removed or unknown key %q = %v, want ErrNotConfigured", key, err)
		}
	}
	if err := limiter.Configure("key", ratelimit.Config{RPM: 1}); err != nil {
		t.Fatal(err)
	}
	if err := limiter.TryAcquire("key"); err != nil {
		t.Fatalf("reconfigured key = %v, want fresh bucket", err)
	}
}

func TestConcurrentAcquisitionsAndCompletedConfiguration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := configured(t, ratelimit.Config{RPM: 60, Burst: new(7)})
		results := make([]error, 100)
		var workers sync.WaitGroup

		for i := range results {
			workers.Go(func() {
				if err := limiter.Configure("key", ratelimit.Config{RPM: 60, Burst: new(7)}); err != nil {
					results[i] = err
					return
				}
				results[i] = limiter.TryAcquire("key")
			})
		}
		workers.Wait()

		accepted := 0
		for _, err := range results {
			if err == nil {
				accepted++
			} else if !errors.Is(err, ratelimit.ErrLimitReached) {
				t.Fatalf("concurrent acquisition: %v", err)
			}
		}
		if accepted != 7 {
			t.Fatalf("accepted %d concurrent requests, want 7", accepted)
		}

		if err := limiter.Configure("key", ratelimit.Config{RPM: ratelimit.Unlimited}); err != nil {
			t.Fatal(err)
		}
		for i := range results {
			workers.Go(func() { results[i] = limiter.TryAcquire("key") })
		}
		workers.Wait()
		for _, err := range results {
			if err != nil {
				t.Fatalf("after completed unlimited configuration = %v, want success", err)
			}
		}

		if err := limiter.Configure("key", ratelimit.Config{RPM: 0}); err != nil {
			t.Fatal(err)
		}
		for i := range results {
			workers.Go(func() { results[i] = limiter.TryAcquire("key") })
		}
		workers.Wait()
		for _, err := range results {
			if !errors.Is(err, ratelimit.ErrLimitReached) {
				t.Fatalf("after completed zero configuration = %v, want ErrLimitReached", err)
			}
		}
	})
}

func configured(t *testing.T, config ratelimit.Config) *ratelimit.Limiter {
	t.Helper()
	limiter := ratelimit.New()
	if err := limiter.Configure("key", config); err != nil {
		t.Fatal(err)
	}
	return limiter
}
