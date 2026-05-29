package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sgrankin/wave/internal/codec"
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/storage"
	"github.com/sgrankin/wave/internal/storage/sqlite"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

func pid(t *testing.T, addr string) id.ParticipantID {
	t.Helper()
	p, err := id.NewParticipantID(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func waveletName(t *testing.T, wave, wavelet string) id.WaveletName {
	t.Helper()
	w, err := id.NewWaveID("example.com", wave)
	if err != nil {
		t.Fatal(err)
	}
	wl, err := id.NewWaveletID("example.com", wavelet)
	if err != nil {
		t.Fatal(err)
	}
	return id.NewWaveletName(w, wl)
}

func openStore(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

// chainRecord builds the next contiguous record from prev, editing blip "b"
// with contentOp, using the real hash chain.
func chainRecord(prev version.HashedVersion, author id.ParticipantID, contentOp op.DocOp) storage.DeltaRecord {
	ops := []waveop.Operation{
		waveop.WaveletBlipOperation{BlipID: "b", BlipOp: waveop.BlipContentOperation{
			Ctx: waveop.Context{Creator: author, Timestamp: 1000, VersionIncrement: 1}, ContentOp: contentOp}},
	}
	bytes := codec.HashBytes(author, prev.Version(), 1000, ops)
	resulting := version.Apply(prev, bytes, uint64(len(ops)))
	return storage.DeltaRecord{
		Author:           author,
		AppliedAtVersion: prev.Version(),
		ResultingVersion: resulting,
		Timestamp:        1000,
		Ops:              ops,
	}
}

func recordEqual(t *testing.T, got, want storage.DeltaRecord) {
	t.Helper()
	if got.Author != want.Author {
		t.Errorf("author = %v, want %v", got.Author, want.Author)
	}
	if got.AppliedAtVersion != want.AppliedAtVersion {
		t.Errorf("appliedAt = %d, want %d", got.AppliedAtVersion, want.AppliedAtVersion)
	}
	if got.ResultingVersion.Compare(want.ResultingVersion) != 0 {
		t.Errorf("resultingVersion mismatch: got v%d, want v%d", got.ResultingVersion.Version(), want.ResultingVersion.Version())
	}
	if got.Timestamp != want.Timestamp {
		t.Errorf("timestamp = %d, want %d", got.Timestamp, want.Timestamp)
	}
	if len(got.Ops) != len(want.Ops) {
		t.Fatalf("op count = %d, want %d", len(got.Ops), len(want.Ops))
	}
	gc := got.Ops[0].(waveop.WaveletBlipOperation).BlipOp.(waveop.BlipContentOperation).ContentOp
	wc := want.Ops[0].(waveop.WaveletBlipOperation).BlipOp.(waveop.BlipContentOperation).ContentOp
	if !gc.Equal(wc) {
		t.Errorf("op content mismatch: got %v, want %v", gc.Components(), wc.Components())
	}
}

func TestAppendReadBack(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	name := waveletName(t, "w+a", "conv+root")
	zero := version.Zero(name)

	access, err := store.Open(name)
	if err != nil {
		t.Fatalf("open wavelet: %v", err)
	}
	if empty, _ := access.IsEmpty(); !empty {
		t.Error("new wavelet should be empty")
	}

	alice := pid(t, "alice@example.com")
	rec := chainRecord(zero, alice, op.NewDocOp([]op.Component{op.Characters{Text: "hi"}}))
	if err := access.Append([]storage.DeltaRecord{rec}); err != nil {
		t.Fatalf("append: %v", err)
	}

	if empty, _ := access.IsEmpty(); empty {
		t.Error("wavelet should not be empty after append")
	}
	all, err := access.ReadAll()
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("read %d records, want 1", len(all))
	}
	recordEqual(t, all[0], rec)

	end, ok, _ := access.EndVersion()
	if !ok || end.Compare(rec.ResultingVersion) != 0 {
		t.Errorf("end version = %v (ok=%v), want %v", end, ok, rec.ResultingVersion)
	}
	got, ok, _ := access.GetDelta(0)
	if !ok {
		t.Fatal("GetDelta(0) not found")
	}
	recordEqual(t, got, rec)
}

func TestContiguousChain(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	name := waveletName(t, "w+a", "conv+root")
	access, _ := store.Open(name)
	alice := pid(t, "alice@example.com")

	r0 := chainRecord(version.Zero(name), alice, op.NewDocOp([]op.Component{op.Characters{Text: "hi"}}))
	r1 := chainRecord(r0.ResultingVersion, alice, op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "!"}}))
	if err := access.Append([]storage.DeltaRecord{r0, r1}); err != nil {
		t.Fatalf("append batch: %v", err)
	}
	all, _ := access.ReadAll()
	if len(all) != 2 {
		t.Fatalf("read %d records, want 2", len(all))
	}
	recordEqual(t, all[0], r0)
	recordEqual(t, all[1], r1)

	// GetDelta keyed by applied-at version finds the second record.
	got, ok, _ := access.GetDelta(r0.ResultingVersion.Version())
	if !ok {
		t.Fatalf("GetDelta(%d) not found", r0.ResultingVersion.Version())
	}
	recordEqual(t, got, r1)
}

func TestNonContiguousRejected(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	name := waveletName(t, "w+a", "conv+root")
	access, _ := store.Open(name)
	alice := pid(t, "alice@example.com")

	// A record claiming to apply at version 5 when the log is empty (end 0).
	bad := chainRecord(version.NewHashedVersion(5, []byte("h")), alice, op.NewDocOp([]op.Component{op.Characters{Text: "x"}}))
	if err := access.Append([]storage.DeltaRecord{bad}); err == nil {
		t.Error("append of a non-contiguous record should fail")
	}
	if empty, _ := access.IsEmpty(); !empty {
		t.Error("failed append must not have stored anything")
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wave.db")
	name := waveletName(t, "w+a", "conv+root")
	alice := pid(t, "alice@example.com")
	r0 := chainRecord(version.Zero(name), alice, op.NewDocOp([]op.Component{op.Characters{Text: "hi"}}))
	r1 := chainRecord(r0.ResultingVersion, alice, op.NewDocOp([]op.Component{op.Retain{Count: 2}, op.Characters{Text: "!"}}))

	store := openStore(t, path)
	access, _ := store.Open(name)
	if err := access.Append([]storage.DeltaRecord{r0, r1}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen the same file: records must read back bit-identically.
	store2 := openStore(t, path)
	defer store2.Close()
	access2, _ := store2.Open(name)
	all, err := access2.ReadAll()
	if err != nil {
		t.Fatalf("read all after reopen: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("after reopen read %d records, want 2", len(all))
	}
	recordEqual(t, all[0], r0)
	recordEqual(t, all[1], r1)
	end, ok, _ := access2.EndVersion()
	if !ok || end.Compare(r1.ResultingVersion) != 0 {
		t.Errorf("end version after reopen = %v, want %v", end, r1.ResultingVersion)
	}
}

// TestFTS5AndJSON1Available asserts the modernc.org/sqlite build bundles the
// FTS5 and JSON1 extensions (search needs FTS5 in Phase 7; accounts use JSON).
// Guards against a future driver version dropping them. Fails the cgo-free build
// path if they're absent.
func TestFTS5AndJSON1Available(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE ft USING fts5(content)`); err != nil {
		t.Errorf("FTS5 not available: %v", err)
	}
	var out string
	if err := db.QueryRow(`SELECT json_extract('{"a":42}', '$.a')`).Scan(&out); err != nil {
		t.Errorf("JSON1 not available: %v", err)
	} else if out != "42" {
		t.Errorf("json_extract = %q, want 42", out)
	}
}

// A committed delta must be visible to an independent connection to the same
// file BEFORE the writer is closed/checkpointed — i.e. the commit is durably in
// the WAL (synchronous=FULL), not buffered in the writer's memory.
func TestCommitVisibleWithoutClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wave.db")
	name := waveletName(t, "w+a", "conv+root")
	alice := pid(t, "alice@example.com")

	store := openStore(t, path)
	defer store.Close()
	access, _ := store.Open(name)
	rec := chainRecord(version.Zero(name), alice, op.NewDocOp([]op.Component{op.Characters{Text: "durable"}}))
	if err := access.Append([]storage.DeltaRecord{rec}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Open a separate handle to the same file without closing the writer.
	reader, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	var count int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM deltas`).Scan(&count); err != nil {
		t.Fatalf("read committed rows: %v", err)
	}
	if count != 1 {
		t.Errorf("independent reader sees %d committed deltas, want 1", count)
	}
}

// A corrupt transformed_blob must surface as an error from the read path, not a
// panic and not a silently-dropped record.
func TestCorruptBlobErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wave.db")
	name := waveletName(t, "w+a", "conv+root")
	alice := pid(t, "alice@example.com")

	store := openStore(t, path)
	access, _ := store.Open(name)
	rec := chainRecord(version.Zero(name), alice, op.NewDocOp([]op.Component{op.Characters{Text: "hi"}}))
	if err := access.Append([]storage.DeltaRecord{rec}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Corrupt the stored blob via a raw connection.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE deltas SET transformed_blob = ?`, []byte{0xff, 0x00, 0x13}); err != nil {
		t.Fatalf("corrupt blob: %v", err)
	}
	raw.Close()

	if _, _, err := access.GetDelta(0); err == nil {
		t.Error("GetDelta on a corrupt blob should error")
	}
	if _, err := access.ReadAll(); err == nil {
		t.Error("ReadAll on a corrupt blob should error")
	}
	store.Close()
}

func TestGetDeltaMiss(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	access, _ := store.Open(waveletName(t, "w+a", "conv+root"))
	if _, ok, err := access.GetDelta(99); err != nil || ok {
		t.Errorf("GetDelta(99) on empty log = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if _, ok, err := access.EndVersion(); err != nil || ok {
		t.Errorf("EndVersion on empty log = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// A multi-op delta exercises resulting = appliedAt + N with N > 1, which the
// GetDelta-by-applied-at keying of the next record depends on.
func TestMultiOpDelta(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	name := waveletName(t, "w+a", "conv+root")
	access, _ := store.Open(name)
	alice := pid(t, "alice@example.com")

	zero := version.Zero(name)
	// Two ops in one delta: add a participant + a blip edit.
	ops := []waveop.Operation{
		waveop.AddParticipant{Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}, Participant: pid(t, "bob@example.com")},
		waveop.WaveletBlipOperation{BlipID: "b", BlipOp: waveop.BlipContentOperation{
			Ctx: waveop.Context{Creator: alice, Timestamp: 1000, VersionIncrement: 1}, ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "x"}})}},
	}
	bytes := codec.HashBytes(alice, 0, 1000, ops)
	resulting := version.Apply(zero, bytes, uint64(len(ops)))
	rec := storage.DeltaRecord{Author: alice, AppliedAtVersion: 0, ResultingVersion: resulting, Timestamp: 1000, Ops: ops}
	if err := access.Append([]storage.DeltaRecord{rec}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if resulting.Version() != 2 {
		t.Fatalf("resulting version = %d, want 2 (2 ops)", resulting.Version())
	}
	all, _ := access.ReadAll()
	if len(all) != 1 || len(all[0].Ops) != 2 {
		t.Fatalf("read %d records / first has %d ops, want 1 record / 2 ops", len(all), len(all[0].Ops))
	}
	end, _, _ := access.EndVersion()
	if end.Version() != 2 {
		t.Errorf("end version = %d, want 2", end.Version())
	}
}

func TestMultipleWaveletsIsolated(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "wave.db"))
	defer store.Close()
	alice := pid(t, "alice@example.com")

	nameA := waveletName(t, "w+a", "conv+root")
	nameB := waveletName(t, "w+b", "conv+root")
	accessA, _ := store.Open(nameA)
	accessB, _ := store.Open(nameB)

	rA := chainRecord(version.Zero(nameA), alice, op.NewDocOp([]op.Component{op.Characters{Text: "A"}}))
	if err := accessA.Append([]storage.DeltaRecord{rA}); err != nil {
		t.Fatal(err)
	}
	// B is still empty and unaffected.
	if empty, _ := accessB.IsEmpty(); !empty {
		t.Error("wavelet B should be empty")
	}
	allA, _ := accessA.ReadAll()
	if len(allA) != 1 {
		t.Errorf("wavelet A has %d records, want 1", len(allA))
	}
}
