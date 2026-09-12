package retry_test

import (
	"testing"
	"time"

	"github.com/deyna256/clan/internal/retry"
)

func TestParseRetryAfter(t *testing.T) {
	received := time.Date(1994, 11, 6, 8, 49, 35, 0, time.UTC)
	for _, tt := range []struct {
		name  string
		value string
		want  retry.Cooldown
	}{
		{name: "zero seconds", value: "0", want: retry.Cooldown{Kind: retry.RetryAt, Until: received}},
		{name: "seconds", value: "2", want: retry.Cooldown{Kind: retry.RetryAt, Until: received.Add(2 * time.Second)}},
		{name: "leading zeros and HTTP whitespace", value: " \t0002\t ", want: retry.Cooldown{Kind: retry.RetryAt, Until: received.Add(2 * time.Second)}},
		{name: "HTTP date", value: "Sun, 06 Nov 1994 08:49:37 GMT", want: retry.Cooldown{Kind: retry.RetryAt, Until: received.Add(2 * time.Second)}},
		{name: "RFC850 date", value: "Sunday, 06-Nov-94 08:49:37 GMT", want: retry.Cooldown{Kind: retry.RetryAt, Until: received.Add(2 * time.Second)}},
		{name: "asctime date", value: "Sun Nov  6 08:49:37 1994", want: retry.Cooldown{Kind: retry.RetryAt, Until: received.Add(2 * time.Second)}},
		{name: "past date", value: "Sun, 06 Nov 1994 08:49:34 GMT", want: retry.Cooldown{Kind: retry.RetryAt, Until: received.Add(-time.Second)}},
		{name: "duration boundary", value: "9223372036", want: retry.Cooldown{Kind: retry.RetryAt, Until: received.Add(9223372036 * time.Second)}},
		{name: "duration overflow", value: "9223372037", want: retry.Cooldown{Kind: retry.RetryBlocked}},
		{name: "largest uint64", value: "18446744073709551615", want: retry.Cooldown{Kind: retry.RetryBlocked}},
		{name: "uint64 overflow", value: "18446744073709551616", want: retry.Cooldown{Kind: retry.RetryBlocked}},
		{name: "many digits", value: "999999999999999999999999999999999999", want: retry.Cooldown{Kind: retry.RetryBlocked}},
		{name: "missing"},
		{name: "whitespace only", value: " \t"},
		{name: "negative", value: "-1"},
		{name: "plus sign", value: "+1"},
		{name: "fraction", value: "1.5"},
		{name: "exponent", value: "1e3"},
		{name: "non-ASCII digits", value: "１２"},
		{name: "newline is not optional whitespace", value: "2\n"},
		{name: "CRLF is not optional whitespace", value: "\r\n2"},
		{name: "Unicode space", value: "\u00a02"},
		{name: "overflow with invalid suffix", value: "999999999999999999999999999999999999x"},
		{name: "invalid date", value: "Sun, 99 Nov 1994 08:49:37 GMT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := retry.ParseRetryAfter(tt.value, received)

			if err != nil || got.Kind != tt.want.Kind || !got.Until.Equal(tt.want.Until) {
				t.Fatalf("ParseRetryAfter() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestParseRetryAfterRejectsZeroReceiptTime(t *testing.T) {
	for _, value := range []string{"", "1", "malformed"} {
		got, err := retry.ParseRetryAfter(value, time.Time{})

		if err == nil || got != (retry.Cooldown{}) {
			t.Fatalf("ParseRetryAfter(%q) = (%+v, %v), want zero cooldown and error", value, got, err)
		}
	}
}

func TestParseRetryAfterPreservesZeroDate(t *testing.T) {
	for _, tt := range []struct {
		name     string
		value    string
		received time.Time
	}{
		{name: "seconds reach zero", value: "1", received: time.Time{}.Add(-time.Second)},
		{name: "future zero date", value: "Mon, 01 Jan 0001 00:00:00 GMT", received: time.Time{}.Add(-time.Second)},
		{name: "past zero date", value: "Mon, 01 Jan 0001 00:00:00 GMT", received: time.Time{}.Add(time.Second)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := retry.ParseRetryAfter(tt.value, tt.received)

			if err != nil || got.Kind != retry.RetryAt || !got.Until.IsZero() {
				t.Fatalf("ParseRetryAfter() = (%+v, %v), want RetryAt with zero date", got, err)
			}
		})
	}
}
