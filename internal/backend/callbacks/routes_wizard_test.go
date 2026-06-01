package callbacks

import (
	"testing"
	"time"
)

func TestRouteWizardStoreAddDraftTTLAndScope(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	threadID := int64(77)
	store := NewRouteWizardStore(5 * time.Minute)
	store.Now = func() time.Time { return now }
	store.TokenFunc = fixedTokens("draft1")

	draft := store.PutAddDraft(RouteAddDraft{
		UserID:   42,
		ThreadID: &threadID,
		RouterID: 9,
		Kind:     "dns",
		Name:     "media",
		TunnelID: "awg10",
		Targets:  []string{"example.com"},
	})
	if draft.Token != "draft1" {
		t.Fatalf("token = %q, want draft1", draft.Token)
	}
	if _, ok := store.GetAddDraft(42, &threadID, 9, "draft1"); !ok {
		t.Fatal("expected draft in correct scope")
	}
	if _, ok := store.GetAddDraft(43, &threadID, 9, "draft1"); ok {
		t.Fatal("wrong user should not resolve draft")
	}
	otherThread := int64(78)
	if _, ok := store.GetAddDraft(42, &otherThread, 9, "draft1"); ok {
		t.Fatal("wrong topic should not resolve draft")
	}

	now = now.Add(5*time.Minute + time.Second)
	if _, ok := store.GetAddDraft(42, &threadID, 9, "draft1"); ok {
		t.Fatal("expired draft should not resolve")
	}
}

func TestRouteWizardStoreAddConfirmRejectsWrongActorWithoutConsuming(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	threadID := int64(77)
	store := NewRouteWizardStore(5 * time.Minute)
	store.Now = func() time.Time { return now }
	store.TokenFunc = fixedTokens("draft1", "confirm1")

	draft := store.PutAddDraft(RouteAddDraft{
		UserID:    42,
		ActorTGID: 111,
		ThreadID:  &threadID,
		RouterID:  9,
		Kind:      "dns",
		Name:      "media",
		TunnelID:  "awg10",
		Targets:   []string{"example.com"},
	})
	if _, ok := store.GetAddDraftForActor(42, 222, &threadID, 9, draft.Token); ok {
		t.Fatal("wrong actor should not resolve add draft")
	}
	confirmed, ok := store.SetAddConfirm(42, &threadID, 9, draft.Token, "hash1")
	if !ok {
		t.Fatal("expected confirm token")
	}
	if _, ok := store.ConsumeAddConfirmForActor(42, 222, &threadID, 9, draft.Token, confirmed.ConfirmToken); ok {
		t.Fatal("wrong actor should not consume add confirm")
	}
	got, ok := store.ConsumeAddConfirmForActor(42, 111, &threadID, 9, draft.Token, confirmed.ConfirmToken)
	if !ok {
		t.Fatal("right actor should still consume add confirm")
	}
	if got.Name != "media" {
		t.Fatalf("Name=%q want media", got.Name)
	}
}

func TestRouteWizardStoreDeleteDraftConfirmIsSingleUse(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := NewRouteWizardStore(5 * time.Minute)
	store.Now = func() time.Time { return now }
	store.TokenFunc = fixedTokens("draft1", "confirm1")

	draft := store.PutDeleteDraft(RouteDeleteDraft{
		UserID:      42,
		RouterID:    9,
		Kind:        "static",
		RouteID:     "r1",
		PreviewHash: "hash1",
	})
	confirmed, ok := store.SetDeleteConfirm(42, nil, 9, draft.Token, "hash1")
	if !ok {
		t.Fatal("expected confirm token")
	}
	if confirmed.ConfirmToken != "confirm1" {
		t.Fatalf("confirm token = %q, want confirm1", confirmed.ConfirmToken)
	}
	if _, ok := store.ConsumeDeleteConfirm(42, nil, 9, draft.Token, "wrong"); ok {
		t.Fatal("wrong confirm token should not consume")
	}
	if got, ok := store.ConsumeDeleteConfirm(42, nil, 9, draft.Token, "confirm1"); !ok || got.RouteID != "r1" {
		t.Fatalf("consume failed: %+v ok=%v", got, ok)
	}
	if _, ok := store.ConsumeDeleteConfirm(42, nil, 9, draft.Token, "confirm1"); ok {
		t.Fatal("confirm should be single-use")
	}
}

func TestRouteWizardStoreDeleteConfirmRejectsWrongActorWithoutConsuming(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := NewRouteWizardStore(5 * time.Minute)
	store.Now = func() time.Time { return now }
	store.TokenFunc = fixedTokens("draft1", "confirm1")

	draft := store.PutDeleteDraft(RouteDeleteDraft{
		UserID:      42,
		ActorTGID:   111,
		RouterID:    9,
		Kind:        "static",
		RouteID:     "r1",
		PreviewHash: "hash1",
	})
	confirmed, ok := store.SetDeleteConfirm(42, nil, 9, draft.Token, "hash1")
	if !ok {
		t.Fatal("expected confirm token")
	}
	if _, ok := store.ConsumeDeleteConfirmForActor(42, 222, nil, 9, draft.Token, confirmed.ConfirmToken); ok {
		t.Fatal("wrong actor should not consume delete confirm")
	}
	got, ok := store.ConsumeDeleteConfirmForActor(42, 111, nil, 9, draft.Token, confirmed.ConfirmToken)
	if !ok {
		t.Fatal("right actor should still consume delete confirm")
	}
	if got.RouteID != "r1" {
		t.Fatalf("RouteID=%q want r1", got.RouteID)
	}
}

func TestRouteWizardStoreRouteTokens(t *testing.T) {
	store := NewRouteWizardStore(time.Minute)
	store.TokenFunc = fixedTokens("rt1")
	token := store.PutRouteToken(RouteToken{
		UserID:      42,
		RouterID:    9,
		Kind:        "dns",
		RouteID:     "rule:with:colon",
		PreviewHash: "hash1",
	})
	if token != "rt1" {
		t.Fatalf("token = %q, want rt1", token)
	}
	got, ok := store.GetRouteToken(42, nil, 9, "rt1")
	if !ok {
		t.Fatal("expected route token")
	}
	if got.RouteID != "rule:with:colon" || got.Kind != "dns" || got.PreviewHash != "hash1" {
		t.Fatalf("bad route token: %+v", got)
	}
	if _, ok := store.GetRouteToken(41, nil, 9, "rt1"); ok {
		t.Fatal("wrong user should not resolve route token")
	}
}

func fixedTokens(tokens ...string) func() string {
	i := 0
	return func() string {
		if i >= len(tokens) {
			return "extra"
		}
		tok := tokens[i]
		i++
		return tok
	}
}
