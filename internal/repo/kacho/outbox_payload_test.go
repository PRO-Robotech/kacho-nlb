// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import (
	"encoding/json"
	"testing"
)

// TestLifecyclePayload_Map_CanonicalKeys — Map() эмитит ровно канонические
// имена ключей (единый источник истины) и опускает пустые поля.
func TestLifecyclePayload_Map_CanonicalKeys(t *testing.T) {
	m := LifecyclePayload{
		ID:               "nlb-listener-1",
		ParentResourceID: "nlb-1",
		ProjectID:        "prj-b",
		RegionID:         "ru-1",
		Name:             "l1",
		Protocol:         "TCP",
		Port:             443,
		Status:           "ACTIVE",
	}.Map()

	if m[PayloadKeyParentResourceID] != "nlb-1" {
		t.Fatalf("parent_resource_id = %v, want nlb-1", m[PayloadKeyParentResourceID])
	}
	// legacy-ключ load_balancer_id больше НЕ пишется.
	if _, ok := m["load_balancer_id"]; ok {
		t.Fatalf("legacy key load_balancer_id must not be emitted: %v", m)
	}
	if m[PayloadKeyPort] != int32(443) {
		t.Fatalf("port = %v, want 443", m[PayloadKeyPort])
	}
	// пустые поля опущены.
	if _, ok := m[PayloadKeyOldProjectID]; ok {
		t.Fatalf("empty old_project_id must be omitted: %v", m)
	}
}

// TestLifecyclePayload_MovedKeys — MOVED-builder эмитит old_project_id/new_project_id
// (не src_/dst_).
func TestLifecyclePayload_MovedKeys(t *testing.T) {
	m := LifecyclePayload{ID: "nlb-1", OldProjectID: "prj-a", NewProjectID: "prj-b"}.Map()
	if m[PayloadKeyOldProjectID] != "prj-a" {
		t.Fatalf("old_project_id = %v, want prj-a", m[PayloadKeyOldProjectID])
	}
	if m[PayloadKeyNewProjectID] != "prj-b" {
		t.Fatalf("new_project_id = %v, want prj-b", m[PayloadKeyNewProjectID])
	}
	if _, ok := m["src_project_id"]; ok {
		t.Fatalf("legacy key src_project_id must not be emitted: %v", m)
	}
}

// TestParseLifecyclePayload_RoundTrip — producer Map() → JSON → consumer Parse
// восстанавливает канонические поля.
func TestParseLifecyclePayload_RoundTrip(t *testing.T) {
	in := LifecyclePayload{ID: "x", ParentResourceID: "nlb-1", OldProjectID: "prj-a", Port: 80}
	raw, err := json.Marshal(in.Map())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseLifecyclePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentResourceID != "nlb-1" || got.OldProjectID != "prj-a" || got.Port != 80 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestParseLifecyclePayload_Tolerant — bad JSON → error; wrong-typed field → пусто
// (graceful), не роняет остальные поля.
func TestParseLifecyclePayload_Tolerant(t *testing.T) {
	if _, err := ParseLifecyclePayload([]byte("not-json")); err == nil {
		t.Fatal("want error on bad JSON")
	}
	// parent_resource_id неверного типа игнорируется, old_project_id валиден.
	got, err := ParseLifecyclePayload([]byte(`{"parent_resource_id":42,"old_project_id":"prj-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentResourceID != "" || got.OldProjectID != "prj-a" {
		t.Fatalf("tolerant parse mismatch: %+v", got)
	}
	// empty payload → zero value, no error.
	if got, err := ParseLifecyclePayload(nil); err != nil || got.ParentResourceID != "" {
		t.Fatalf("empty payload: got %+v err %v", got, err)
	}
}
