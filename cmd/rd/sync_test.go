package main

// sync_test.go — unit tests for clientLister.ListMessages() branches.
//
// Branches exercised:
//   - afterTimestamp == 0: all messages returned
//   - afterTimestamp > 0: messages with timestamp <= afterTimestamp filtered out
//   - filter with Tags set: tags extracted and forwarded to Read
//   - no messages from client: returns nil (empty) slice

import (
	"errors"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// fakeMessageReadClient implements campfireReadClient with a pre-configured
// set of messages and an optional error.
type fakeMessageReadClient struct {
	messages []protocol.Message
	err      error
	// lastReq captures the most recent Read call for assertion.
	lastReq protocol.ReadRequest
}

func (f *fakeMessageReadClient) Read(req protocol.ReadRequest) (*protocol.ReadResult, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &protocol.ReadResult{Messages: f.messages}, nil
}

// makeMsg builds a minimal protocol.Message for testing.
func makeMsg(id string, ts int64, tags []string) protocol.Message {
	return protocol.Message{
		ID:        id,
		Timestamp: ts,
		Tags:      tags,
		Payload:   []byte("payload-" + id),
		Sender:    "aabbcc",
	}
}

func TestClientListerListMessages_NoTimestampFilter(t *testing.T) {
	fake := &fakeMessageReadClient{
		messages: []protocol.Message{
			makeMsg("msg1", 100, []string{"work:create"}),
			makeMsg("msg2", 200, []string{"work:update"}),
			makeMsg("msg3", 300, []string{"work:close"}),
		},
	}
	cl := &clientLister{client: fake}

	got, err := cl.ListMessages("campfire-abc", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 records, got %d", len(got))
	}
	// Verify IDs in order.
	for i, want := range []string{"msg1", "msg2", "msg3"} {
		if got[i].ID != want {
			t.Errorf("record[%d].ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestClientListerListMessages_TimestampFilter(t *testing.T) {
	fake := &fakeMessageReadClient{
		messages: []protocol.Message{
			makeMsg("old1", 100, nil),
			makeMsg("old2", 200, nil),
			makeMsg("new1", 300, nil),
			makeMsg("new2", 400, nil),
		},
	}
	cl := &clientLister{client: fake}

	// afterTimestamp=200: should exclude msg with ts<=200.
	got, err := cl.ListMessages("campfire-abc", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records after timestamp filter, got %d", len(got))
	}
	if got[0].ID != "new1" {
		t.Errorf("got[0].ID = %q, want %q", got[0].ID, "new1")
	}
	if got[1].ID != "new2" {
		t.Errorf("got[1].ID = %q, want %q", got[1].ID, "new2")
	}
	// Verify excluded messages not present.
	for _, r := range got {
		if r.Timestamp <= 200 {
			t.Errorf("record %q has timestamp %d <= afterTimestamp 200", r.ID, r.Timestamp)
		}
	}
}

func TestClientListerListMessages_TagExtraction(t *testing.T) {
	fake := &fakeMessageReadClient{
		messages: []protocol.Message{
			makeMsg("msg1", 100, []string{"work:create"}),
		},
	}
	cl := &clientLister{client: fake}

	filter := store.MessageFilter{
		Tags: []string{"work:create", "work:update"},
	}
	_, err := cl.ListMessages("campfire-xyz", 0, filter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify that the tags were forwarded to Read.
	got := fake.lastReq.Tags
	if len(got) != 2 {
		t.Fatalf("want 2 tags forwarded to Read, got %d: %v", len(got), got)
	}
	if got[0] != "work:create" || got[1] != "work:update" {
		t.Errorf("forwarded tags = %v, want [work:create work:update]", got)
	}
	// CampfireID must also be forwarded.
	if fake.lastReq.CampfireID != "campfire-xyz" {
		t.Errorf("CampfireID = %q, want %q", fake.lastReq.CampfireID, "campfire-xyz")
	}
}

func TestClientListerListMessages_MultipleFilters_OnlyFirstTagsUsed(t *testing.T) {
	// When multiple MessageFilter values are passed, only filter[0].Tags is used.
	fake := &fakeMessageReadClient{
		messages: []protocol.Message{
			makeMsg("msg1", 100, []string{"work:create"}),
		},
	}
	cl := &clientLister{client: fake}

	f1 := store.MessageFilter{Tags: []string{"work:create"}}
	f2 := store.MessageFilter{Tags: []string{"work:update"}}
	_, err := cl.ListMessages("campfire-abc", 0, f1, f2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only f1.Tags should reach Read.
	if len(fake.lastReq.Tags) != 1 || fake.lastReq.Tags[0] != "work:create" {
		t.Errorf("want [work:create] forwarded, got %v", fake.lastReq.Tags)
	}
}

func TestClientListerListMessages_EmptyFilter_NoTags(t *testing.T) {
	// A filter with no Tags should forward nil/empty tags to Read (no tag filtering).
	fake := &fakeMessageReadClient{
		messages: []protocol.Message{
			makeMsg("msg1", 100, nil),
		},
	}
	cl := &clientLister{client: fake}

	filter := store.MessageFilter{} // Tags is nil
	_, err := cl.ListMessages("campfire-abc", 0, filter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.lastReq.Tags) != 0 {
		t.Errorf("want no tags forwarded when filter.Tags is empty, got %v", fake.lastReq.Tags)
	}
}

func TestClientListerListMessages_NoMessages(t *testing.T) {
	fake := &fakeMessageReadClient{
		messages: nil, // client returns zero messages
	}
	cl := &clientLister{client: fake}

	got, err := cl.ListMessages("campfire-abc", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %d records", len(got))
	}
}

func TestClientListerListMessages_FieldMapping(t *testing.T) {
	// Verify that all fields are correctly mapped from protocol.Message to store.MessageRecord.
	fake := &fakeMessageReadClient{
		messages: []protocol.Message{
			{
				ID:        "msg-abc",
				Timestamp: 12345,
				Tags:      []string{"work:create"},
				Payload:   []byte("hello"),
				Sender:    "deadbeef",
			},
		},
	}
	cl := &clientLister{client: fake}

	got, err := cl.ListMessages("campfire-id-42", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	r := got[0]
	if r.ID != "msg-abc" {
		t.Errorf("ID = %q, want %q", r.ID, "msg-abc")
	}
	if r.CampfireID != "campfire-id-42" {
		t.Errorf("CampfireID = %q, want %q", r.CampfireID, "campfire-id-42")
	}
	if r.Timestamp != 12345 {
		t.Errorf("Timestamp = %d, want %d", r.Timestamp, 12345)
	}
	if r.ReceivedAt != 12345 {
		t.Errorf("ReceivedAt = %d, want %d", r.ReceivedAt, 12345)
	}
	if string(r.Payload) != "hello" {
		t.Errorf("Payload = %q, want %q", r.Payload, "hello")
	}
	if r.Sender != "deadbeef" {
		t.Errorf("Sender = %q, want %q", r.Sender, "deadbeef")
	}
}

func TestClientListerListMessages_ClientError(t *testing.T) {
	sentinelErr := errors.New("transport failure")
	fake := &fakeMessageReadClient{err: sentinelErr}
	cl := &clientLister{client: fake}

	_, err := cl.ListMessages("campfire-abc", 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinelErr) {
		t.Errorf("error = %v, want to wrap %v", err, sentinelErr)
	}
}
