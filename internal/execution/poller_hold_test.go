package execution

import (
	"testing"

	"github.com/YoanWai/agent-manager/internal/status"
)

func TestHoldSessionsKeepsThePollOffHeldPanesOnly(t *testing.T) {
	m := buildModel(t)
	createSession(t, m, "held", t.TempDir(), "")
	createSession(t, m, "polled", t.TempDir(), "")
	held, polled := m.sessionRows()[0], m.sessionRows()[1]
	testPollMessage(m.poller.Step())
	before, err := m.store.Get(held.ID)
	if err != nil {
		t.Fatal(err)
	}

	err = m.poller.HoldSessions([]string{held.ID}, func() {
		for _, id := range []string{held.ID, polled.ID} {
			if err := m.tmux.Kill(id); err != nil {
				t.Fatal(err)
			}
		}
		testPollMessage(m.poller.Step())
		assertStatus(t, m, held.ID, before.Status)
		assertStatus(t, m, polled.ID, status.Dead)
	})
	if err != nil {
		t.Fatal(err)
	}
	testPollMessage(m.poller.Step())
	assertStatus(t, m, held.ID, status.Dead)
}

func assertStatus(t *testing.T, m *harness, id, want string) {
	t.Helper()
	got, err := m.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != want {
		t.Fatalf("%s status = %q, want %q", id, got.Status, want)
	}
}
