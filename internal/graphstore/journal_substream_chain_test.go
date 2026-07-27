package graphstore

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// TestChainHashIncludesSubstream_S6 pins the DDL-freeze S6 decision: substream is
// folded into the chain-hash preimage. Two things are proved:
//
//  1. Directly, chainHash over two rows that differ ONLY in substream yields
//     different digests, while an identical substream reproduces the digest — so
//     substream is genuinely part of the preimage and the framing stays injective.
//  2. End to end, out-of-band tampering that changes ONLY substream is caught by
//     Verify with ErrChainBroken.
func TestChainHashIncludesSubstream_S6(t *testing.T) {
	// (1) Direct preimage check.
	prev := [32]byte{1, 2, 3}
	ph := [32]byte{9, 9, 9}
	base := chainHash(prev, "gcj-root", 2, "lumen", "lumen.channel.emit", "", "ir-1", ph)
	withSub := chainHash(prev, "gcj-root", 2, "lumen", "lumen.channel.emit", "chan-a", "ir-1", ph)
	sameSub := chainHash(prev, "gcj-root", 2, "lumen", "lumen.channel.emit", "", "ir-1", ph)
	if base == withSub {
		t.Fatalf("chainHash ignores substream: '' and 'chan-a' produced the same digest")
	}
	if base != sameSub {
		t.Fatalf("chainHash not deterministic for equal inputs: %x vs %x", base, sameSub)
	}

	// (2) End-to-end Verify detection.
	s := newTestStore(t)
	ctx := context.Background()
	const stream = "gcj-root-substream"

	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, stream, testEngine, uint64(i), 0, []JournalEvent{{
			Type:    testType,
			Payload: canonPayload(t, `{"i":`+strconv.Itoa(i)+`}`),
		}}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := s.Verify(ctx, stream); err != nil {
		t.Fatalf("verify clean chain: %v", err)
	}

	// Keep seq 2's payload and chain hash, changing only its substream after
	// deliberately dropping the trigger that protects production writes.
	events, err := s.ReadStream(ctx, stream, 2, 2)
	if err != nil || len(events) != 1 {
		t.Fatalf("read seq 2: %v (n=%d)", err, len(events))
	}
	if events[0].Substream != "" {
		t.Fatalf("precondition: seq 2 substream = %q, want empty", events[0].Substream)
	}
	db := s.ReadDB()
	if _, err := db.ExecContext(ctx, `DROP TRIGGER journal_no_update`); err != nil {
		t.Fatalf("drop update trigger: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE journal SET substream = 'chan-a'
		  WHERE stream_id = ? AND seq = 2`,
		stream,
	); err != nil {
		t.Fatalf("tamper with substream: %v", err)
	}

	err = s.Verify(ctx, stream)
	if !errors.Is(err, ErrChainBroken) {
		t.Fatalf("verify after substream-only change = %v, want ErrChainBroken (substream not chained?)", err)
	}
}
