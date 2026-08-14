package v1

import "testing"

// A phone whose tab is swiped away often leaves its websocket open: no FIN
// reaches us, so the session stays registered until the 90s heartbeat sweep.
// When that phone comes back it reconnects under the same peer id, and the two
// sessions used to coexist -- the stale one still listed as an online device.
// Everyone then saw the phone twice, and dialling the corpse failed 15s later
// with "cannot connect" on both devices while real transfers worked fine
// (2026-08-13 acceptance). Registering a session must evict the older one for
// that id, and the evicted session must not take the live one down with it.
func TestRegisterClientReplacesEarlierSessionForSameID(t *testing.T) {
	ch := &CenterHandler{clients: map[string]*Client{}}
	old := &Client{ID: "phone"}
	fresh := &Client{ID: "phone"}

	if superseded := ch.registerClient(old); superseded != nil {
		t.Fatalf("registering into an empty set superseded %v, want nil", superseded)
	}

	superseded := ch.registerClient(fresh)

	if superseded != old {
		t.Fatalf("registerClient superseded %v, want the earlier session %v", superseded, old)
	}
	if ch.clients["phone"] != fresh {
		t.Fatalf("clients[phone] = %v, want the fresh session", ch.clients["phone"])
	}
	if !old.isSuperseded() {
		t.Fatal("the replaced session is not marked superseded, so its readPump will announce peer-left for an id that is online again")
	}
	if fresh.isSuperseded() {
		t.Fatal("the live session must not be marked superseded")
	}
}

func TestUnregisterClientKeepsALiveSessionUnderTheSameID(t *testing.T) {
	ch := &CenterHandler{clients: map[string]*Client{}}
	old := &Client{ID: "phone"}
	fresh := &Client{ID: "phone"}
	ch.registerClient(old)
	ch.registerClient(fresh)

	// The old socket finally errors out and unregisters, seconds after the
	// phone is already back. Deleting by id alone would drop the live session
	// and the phone would silently vanish from every device list.
	if removed := ch.unregisterClient(old); removed {
		t.Fatal("unregistering a superseded session removed the live one")
	}
	if ch.clients["phone"] != fresh {
		t.Fatalf("clients[phone] = %v, want the fresh session still registered", ch.clients["phone"])
	}

	if removed := ch.unregisterClient(fresh); !removed {
		t.Fatal("unregistering the live session did not remove it")
	}
	if _, ok := ch.clients["phone"]; ok {
		t.Fatal("clients[phone] still present after the live session left")
	}
}

// The peer table is capped at 10. The eviction walked the list from the end,
// where the row for the peer that just connected sits (the list is ordered by
// `updated desc` and the new row is appended) -- and that peer is not in the
// client set yet, because registration is the last step of the handshake. So a
// full table made every new device delete its own row on arrival: it got a
// fresh uuid and a fresh "Android Chrome_N" name on every single connect, which
// is how one phone ends up owning several entries.
func TestKickoutIDsNeverEvictsThePeerThatJustConnected(t *testing.T) {
	// updated desc: newest first, and the just-created row appended last.
	ids := []string{"newest", "older", "oldest", "arriving"}
	online := map[string]bool{"newest": true}

	got := kickoutIDs(ids, func(id string) bool { return online[id] }, "arriving", 3)

	if len(got) != 1 || got[0] != "oldest" {
		t.Fatalf("kickoutIDs() = %v, want [oldest]", got)
	}
}

func TestKickoutIDsSkipsOnlinePeersAndStopsAtTheLimit(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e"}
	online := map[string]bool{"d": true, "e": true}

	got := kickoutIDs(ids, func(id string) bool { return online[id] }, "a", 2)

	// Needs 3 gone, but only b and c are eligible (a just arrived, d/e online).
	if len(got) != 2 || got[0] != "c" || got[1] != "b" {
		t.Fatalf("kickoutIDs() = %v, want [c b] (oldest eligible first)", got)
	}
}

func TestKickoutIDsReturnsNothingWhenUnderTheLimit(t *testing.T) {
	if got := kickoutIDs([]string{"a", "b"}, func(string) bool { return false }, "a", 10); got != nil {
		t.Fatalf("kickoutIDs() = %v, want nil", got)
	}
}
