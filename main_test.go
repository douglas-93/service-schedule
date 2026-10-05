package main

import (
	"errors"
	"strings"
	"testing"
)

func TestRetryStartRetriesUntilSuccess(t *testing.T) {
	attempts := 0
	err := retryStart("example", 2, 0, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary failure")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("retryStart() returned an error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("retryStart() made %d attempts, want 3", attempts)
	}
}

func TestRetryStartReturnsErrorAfterRetriesExhausted(t *testing.T) {
	attempts := 0
	err := retryStart("example", 2, 0, func() error {
		attempts++
		return errors.New("service unavailable")
	})

	if err == nil {
		t.Fatal("retryStart() returned nil, want an error")
	}
	if attempts != 3 {
		t.Fatalf("retryStart() made %d attempts, want 3", attempts)
	}
	if !strings.Contains(err.Error(), "após 3 tentativa(s)") {
		t.Fatalf("error %q does not report total attempts", err)
	}
}

func TestRetryStartZeroRetriesMakesOneAttempt(t *testing.T) {
	attempts := 0
	err := retryStart("example", 0, 0, func() error {
		attempts++
		return errors.New("service unavailable")
	})

	if err == nil {
		t.Fatal("retryStart() returned nil, want an error")
	}
	if attempts != 1 {
		t.Fatalf("retryStart() made %d attempts, want 1", attempts)
	}
}
