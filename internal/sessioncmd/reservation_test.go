package sessioncmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoanWai/agent-manager/internal/lifecyclehook"
	"github.com/YoanWai/agent-manager/internal/status"
	"github.com/YoanWai/agent-manager/internal/store"
	"github.com/google/uuid"
)

func TestReservationsSurfaceOverlapWithoutBlockingIt(t *testing.T) {
	h := newStoreSessionHarness(t)
	rivalID := h.addSessionRow(t, "rival")
	held, err := h.sessions.Reserve(h.caller.ID, []string{"internal/store/*.go"}, "", "adding the inbox table", 0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(held.Reserved) != 1 || len(held.Conflicts) != 0 {
		t.Fatalf("first reservation = %+v", held)
	}
	if !held.Reserved[0].Mine || held.Reserved[0].Mode != "exclusive" {
		t.Fatalf("reservation = %+v", held.Reserved[0])
	}

	// A literal path under a reserved glob is the collision worktrees miss.
	clash, err := h.sessions.Reserve(rivalID, []string{"internal/store/store.go"}, "", "", 0)
	if err != nil {
		t.Fatalf("Reserve rival: %v", err)
	}
	if len(clash.Conflicts) != 1 {
		t.Fatalf("overlap was not reported: %+v", clash)
	}
	if clash.Conflicts[0].Holder != "calling-agent" || clash.Conflicts[0].Note != "adding the inbox table" {
		t.Fatalf("conflict = %+v", clash.Conflicts[0])
	}
	// Advisory: the second lease is still granted, so neither agent is stuck.
	if len(clash.Reserved) != 1 {
		t.Fatalf("an advisory lease must still be granted: %+v", clash)
	}

	listed, err := h.sessions.Reservations(h.caller.ID)
	if err != nil {
		t.Fatalf("Reservations: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestSharedLeasesOnlyClashWithExclusiveOnes(t *testing.T) {
	h := newStoreSessionHarness(t)
	rivalID := h.addSessionRow(t, "rival")
	if _, err := h.sessions.Reserve(h.caller.ID, []string{"docs/usage.md"}, "shared", "", 0); err != nil {
		t.Fatalf("Reserve shared: %v", err)
	}
	quiet, err := h.sessions.Reserve(rivalID, []string{"docs/usage.md"}, "shared", "", 0)
	if err != nil {
		t.Fatalf("Reserve second shared: %v", err)
	}
	if len(quiet.Conflicts) != 0 {
		t.Fatalf("two shared leases should not clash: %+v", quiet.Conflicts)
	}
	loud, err := h.sessions.Reserve(rivalID, []string{"docs/usage.md"}, "exclusive", "", 0)
	if err != nil {
		t.Fatalf("Reserve exclusive: %v", err)
	}
	if len(loud.Conflicts) != 1 {
		t.Fatalf("an exclusive lease over a shared one should clash: %+v", loud)
	}
	if _, err := h.sessions.Reserve(h.caller.ID, []string{"docs/usage.md"}, "sideways", "", 0); err == nil ||
		!strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("bad mode error = %v", err)
	}
	if _, err := h.sessions.Reserve(h.caller.ID, nil, "", "", 0); err == nil {
		t.Fatal("reserving nothing should be refused")
	}
	for _, ttl := range []time.Duration{-time.Minute, MaxReservationTTL + time.Minute} {
		if _, err := h.sessions.Reserve(h.caller.ID, []string{"docs/usage.md"}, "", "", ttl); err == nil ||
			!strings.Contains(err.Error(), "outside 0 to "+MaxReservationTTL.String()) {
			t.Fatalf("a %s lease = %v, want a refusal naming the bound", ttl, err)
		}
	}
}

func TestALapsedLeaseStopsBlockingAndReleaseClearsTheRest(t *testing.T) {
	h := newStoreSessionHarness(t)
	rivalID := h.addSessionRow(t, "rival")
	// A lease whose expiry is already behind us stands in for an agent that
	// died holding one, which is the case the whole advisory design rests on.
	lapsed := time.Now().Add(-time.Minute)
	if _, err := h.store.Reserve([]store.Reservation{{
		ID:         "lapsed01",
		SessionID:  rivalID,
		Pattern:    "internal/ui/*.go",
		Mode:       store.ReservationExclusive,
		AcquiredAt: lapsed.Add(-time.Hour),
		ExpiresAt:  lapsed,
	}}); err != nil {
		t.Fatalf("write a lapsed lease: %v", err)
	}
	fresh, err := h.sessions.Reserve(h.caller.ID, []string{"internal/ui/model.go"}, "", "", 0)
	if err != nil {
		t.Fatalf("Reserve after lapse: %v", err)
	}
	if len(fresh.Conflicts) != 0 {
		t.Fatalf("a lapsed lease still blocks: %+v", fresh.Conflicts)
	}

	if _, err := h.sessions.Reserve(h.caller.ID, []string{"a.go", "b.go"}, "", "", 0); err != nil {
		t.Fatalf("Reserve more: %v", err)
	}
	released, err := h.sessions.ReleaseFiles(h.caller.ID, []string{"a.go"})
	if err != nil || released != 1 {
		t.Fatalf("ReleaseFiles = %d, %v", released, err)
	}
	released, err = h.sessions.ReleaseFiles(h.caller.ID, nil)
	if err != nil || released != 2 {
		t.Fatalf("ReleaseFiles all = %d, %v", released, err)
	}
	listed, err := h.sessions.Reservations(h.caller.ID)
	if err != nil {
		t.Fatalf("Reservations: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("leases survived a full release: %+v", listed)
	}
}

func TestReleasingBlankPathsDoesNotDropEveryLease(t *testing.T) {
	h := newStoreSessionHarness(t)
	if _, err := h.sessions.Reserve(h.caller.ID, []string{"a.go", "b.go"}, "", "", 0); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := h.sessions.ReleaseFiles(h.caller.ID, []string{"  "}); err == nil ||
		!strings.Contains(err.Error(), "omit paths entirely") {
		t.Fatalf("blank release error = %v", err)
	}
	listed, err := h.sessions.Reservations(h.caller.ID)
	if err != nil {
		t.Fatalf("Reservations: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("a blank path released real leases: %+v", listed)
	}
}

type reservationHarness struct {
	store    *store.Store
	sessions *Sessions
	events   *lifecyclehook.Recorder
}

// newReservationHarness backs Sessions with an open store directly, so
// reservation resolution tests run without requiring tmux.
func newReservationHarness(t *testing.T) *reservationHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	rec := &lifecyclehook.Recorder{}
	backend := &Backend{
		opened: true,
		runtime: Runtime{
			Store:  st,
			Events: rec,
		},
	}
	return &reservationHarness{
		store:    st,
		sessions: NewSessionsWithBackend(backend, MCPVocabulary()),
		events:   rec,
	}
}

func (h *reservationHarness) addSession(t *testing.T, name, cwd string) string {
	t.Helper()
	id := uuid.NewString()[:8]
	if err := h.store.CreateSession(store.Session{
		ID:     id,
		Name:   name,
		Tool:   "echoer",
		Cwd:    cwd,
		Status: status.Idle,
	}); err != nil {
		t.Fatalf("create session %s: %v", name, err)
	}
	return id
}

func TestRelativeAndAbsolutePathsConflictInSameCheckout(t *testing.T) {
	h := newReservationHarness(t)
	repo := t.TempDir()
	first := h.addSession(t, "first", repo)
	second := h.addSession(t, "second", repo)
	third := h.addSession(t, "third", repo)

	absFile := filepath.Join(repo, "dir", "file.md")
	wantPattern := resolveReservationPattern(resolveBaseDir(repo), absFile)

	held, err := h.sessions.Reserve(first, []string{"dir/file.md"}, "exclusive", "relative claim", 0)
	if err != nil {
		t.Fatalf("Reserve first: %v", err)
	}
	if len(held.Reserved) != 1 || held.Reserved[0].Pattern != wantPattern {
		t.Fatalf("first reserved = %+v, want pattern %q", held.Reserved, wantPattern)
	}

	clash, err := h.sessions.Reserve(second, []string{absFile}, "exclusive", "absolute claim", 0)
	if err != nil {
		t.Fatalf("Reserve second: %v", err)
	}
	if len(clash.Conflicts) != 1 || clash.Conflicts[0].Holder != "first" {
		t.Fatalf("second conflicts = %+v, want first", clash.Conflicts)
	}

	both, err := h.sessions.Reserve(third, []string{absFile}, "exclusive", "", 0)
	if err != nil {
		t.Fatalf("Reserve third: %v", err)
	}
	if len(both.Conflicts) != 2 {
		t.Fatalf("third conflicts = %+v, want both first and second", both.Conflicts)
	}

	// Relative glob and absolute literal path overlap in both directions.
	globHolder := h.addSession(t, "glob-holder", repo)
	globClash, err := h.sessions.Reserve(globHolder, []string{"./dir/*.md"}, "exclusive", "", 0)
	if err != nil {
		t.Fatalf("Reserve glob: %v", err)
	}
	if len(globClash.Conflicts) != 3 {
		t.Fatalf("glob conflicts = %+v, want all three file holders", globClash.Conflicts)
	}
}

func TestReservationsResolveAgainstEachSessionsDirectory(t *testing.T) {
	h := newReservationHarness(t)
	repo := t.TempDir()
	subDir := filepath.Join(repo, "dir")
	otherSub := filepath.Join(repo, "other")
	otherRepo := t.TempDir()

	rootSess := h.addSession(t, "root", repo)
	subSess := h.addSession(t, "subdir", subDir)
	siblingSess := h.addSession(t, "sibling", otherSub)
	separateSess := h.addSession(t, "other-checkout", otherRepo)

	if _, err := h.sessions.Reserve(rootSess, []string{"dir/file.md"}, "exclusive", "", 0); err != nil {
		t.Fatalf("Reserve root: %v", err)
	}
	fromSub, err := h.sessions.Reserve(subSess, []string{"file.md"}, "exclusive", "", 0)
	if err != nil {
		t.Fatalf("Reserve subdir: %v", err)
	}
	if len(fromSub.Conflicts) != 1 || fromSub.Conflicts[0].Holder != "root" {
		t.Fatalf("subdir conflicts = %+v, want root", fromSub.Conflicts)
	}
	fromSibling, err := h.sessions.Reserve(siblingSess, []string{"../dir/file.md"}, "exclusive", "", 0)
	if err != nil {
		t.Fatalf("Reserve sibling: %v", err)
	}
	if len(fromSibling.Conflicts) != 2 {
		t.Fatalf("sibling conflicts = %+v, want root and subdir", fromSibling.Conflicts)
	}
	fromOtherRepo, err := h.sessions.Reserve(separateSess, []string{"dir/file.md"}, "exclusive", "", 0)
	if err != nil {
		t.Fatalf("Reserve other checkout: %v", err)
	}
	if len(fromOtherRepo.Conflicts) != 0 {
		t.Fatalf("separate checkout falsely conflicted: %+v", fromOtherRepo.Conflicts)
	}
}

func TestReleaseAndRenewalMatchAcrossRelativeAndAbsoluteForms(t *testing.T) {
	h := newReservationHarness(t)
	repo := t.TempDir()
	sess := h.addSession(t, "worker", repo)
	absFile := filepath.Join(repo, "dir", "file.md")

	h.events.Reset()
	first, err := h.sessions.Reserve(sess, []string{"dir/file.md", absFile}, "exclusive", "v1", 0)
	if err != nil {
		t.Fatalf("Reserve duplicate forms: %v", err)
	}
	if len(first.Reserved) != 1 {
		t.Fatalf("equivalent paths were not deduplicated: %+v", first.Reserved)
	}
	if got := h.events.Events(); len(got) != 1 {
		t.Fatalf("reserve events = %d, want 1", len(got))
	}

	// Renewing under the other spelling updates the same lease rather than adding a second row.
	if _, err := h.sessions.Reserve(sess, []string{absFile}, "exclusive", "v2", 0); err != nil {
		t.Fatalf("Reserve renew: %v", err)
	}
	listed, err := h.sessions.Reservations(sess)
	if err != nil {
		t.Fatalf("Reservations: %v", err)
	}
	if len(listed) != 1 || listed[0].Note != "v2" {
		t.Fatalf("listed after renew = %+v, want one updated lease", listed)
	}

	// Releasing by absolute path clears a lease taken by relative path.
	released, err := h.sessions.ReleaseFiles(sess, []string{absFile})
	if err != nil || released != 1 {
		t.Fatalf("ReleaseFiles absolute = %d, %v; want 1", released, err)
	}

	// Releasing by relative path clears a lease taken by absolute path.
	if _, err := h.sessions.Reserve(sess, []string{absFile}, "exclusive", "", 0); err != nil {
		t.Fatalf("Reserve absolute: %v", err)
	}
	released, err = h.sessions.ReleaseFiles(sess, []string{"./dir/file.md"})
	if err != nil || released != 1 {
		t.Fatalf("ReleaseFiles relative = %d, %v; want 1", released, err)
	}
	listed, err = h.sessions.Reservations(sess)
	if err != nil {
		t.Fatalf("Reservations final: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("leases survived release: %+v", listed)
	}
}

func TestReservationsResolveSymlinkedDirectoriesAndGlobs(t *testing.T) {
	h := newReservationHarness(t)
	root := t.TempDir()
	realRepo := filepath.Join(root, "real-repo")
	if err := os.MkdirAll(filepath.Join(realRepo, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linkRepo := filepath.Join(root, "link-repo")
	if err := os.Symlink(realRepo, linkRepo); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	viaLink := h.addSession(t, "via-link", linkRepo)
	viaReal := h.addSession(t, "via-real", realRepo)

	if _, err := h.sessions.Reserve(viaLink, []string{"dir/*.md"}, "exclusive", "", 0); err != nil {
		t.Fatalf("Reserve via symlink: %v", err)
	}
	clash, err := h.sessions.Reserve(viaReal, []string{filepath.Join(realRepo, "dir", "not-created-yet.md")}, "exclusive", "", 0)
	if err != nil {
		t.Fatalf("Reserve via real path: %v", err)
	}
	if len(clash.Conflicts) != 1 || clash.Conflicts[0].Holder != "via-link" {
		t.Fatalf("symlink vs real conflicts = %+v, want via-link", clash.Conflicts)
	}
}

func TestResolveReservationPatternEdgeCases(t *testing.T) {
	if got := resolveReservationPattern("", "dir/./file.md"); got != filepath.Clean("dir/file.md") {
		t.Fatalf("empty baseDir = %q", got)
	}
	if home, err := os.UserHomeDir(); err == nil {
		want := resolveReservationPattern("", filepath.Join(home, "repo", "file.md"))
		if got := resolveReservationPattern(t.TempDir(), "~/repo/file.md"); got != want {
			t.Fatalf("tilde path = %q, want %q", got, want)
		}
	}
	prefix, tail := splitGlobPrefix(filepath.FromSlash("/repo/dir/*.go"))
	if prefix != filepath.FromSlash("/repo/dir") || tail != "*.go" {
		t.Fatalf("splitGlobPrefix = (%q, %q)", prefix, tail)
	}
	prefix, tail = splitGlobPrefix("*.go")
	if prefix != "" || tail != "*.go" {
		t.Fatalf("splitGlobPrefix bare glob = (%q, %q)", prefix, tail)
	}
}
