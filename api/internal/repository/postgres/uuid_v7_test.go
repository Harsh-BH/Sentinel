package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// The partition-pruning optimisation rests entirely on being able to recover a
// job's creation time from its ID. If this extraction is wrong, reads silently
// fall back to scanning every partition — or, worse, the window misses the row.
func TestUUIDv7Time_MatchesGenerationTime(t *testing.T) {
	before := time.Now().UTC()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("NewV7: %v", err)
	}
	after := time.Now().UTC()

	got, ok := uuidV7Time(id)
	if !ok {
		t.Fatal("expected a v7 UUID to yield a timestamp")
	}

	// UUIDv7 stores milliseconds, so allow for truncation at both ends.
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("extracted %s outside generation window [%s, %s]", got, before, after)
	}
}

func TestUUIDv7Time_RejectsOtherVersions(t *testing.T) {
	// uuid.New() is a v4 — random, no embedded timestamp to recover.
	if _, ok := uuidV7Time(uuid.New()); ok {
		t.Error("a v4 UUID must not yield a timestamp")
	}
	if _, ok := uuidV7Time(uuid.Nil); ok {
		t.Error("the nil UUID must not yield a timestamp")
	}
}

// The window must be wide enough to absorb clock skew between replicas and the
// microsecond truncation Postgres applies, but narrow enough to prune.
func TestPartitionSlack_IsWiderThanPlausibleSkew(t *testing.T) {
	if partitionSlack < time.Hour {
		t.Errorf("partitionSlack %s is too tight to absorb clock skew", partitionSlack)
	}
	// A quarter is ~90 days; anything approaching that stops pruning usefully.
	if partitionSlack > 7*24*time.Hour {
		t.Errorf("partitionSlack %s is wide enough to defeat pruning", partitionSlack)
	}
}
