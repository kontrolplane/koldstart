package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

// fakeStore holds orders by id, as the table does, and fails to store the ids in broken.
type fakeStore struct {
	stored map[string]order
	broken map[string]bool
}

func (fake *fakeStore) put(_ context.Context, placed order) error {
	if fake.broken[placed.ID] {
		return errors.New("dynamodb is down")
	}
	if _, ok := fake.stored[placed.ID]; ok {
		return errStored
	}
	fake.stored[placed.ID] = placed
	return nil
}

// valid is the body of an order the api queues, by its id.
func valid(id string) string {
	return `{"id": "` + id + `", "customerId": "customer-1", "currency": "EUR", "lines": [{"sku": "socks", "quantity": 2}], "status": "received", "createdAt": "2026-11-09T10:00:00Z"}`
}

// TestHandle checks the worker stores each order of a batch once, and reports as failed only
// the messages it could not store, so sqs retries those alone.
func TestHandle(t *testing.T) {
	tests := []struct {
		name   string
		bodies []string
		stored []string
		broken []string
		failed []string
		want   []string
	}{
		{
			name:   "a batch of orders",
			bodies: []string{valid("order-1"), valid("order-2")},
			want:   []string{"order-1", "order-2"},
		},
		{
			name:   "an order delivered twice is stored once",
			bodies: []string{valid("order-1")},
			stored: []string{"order-1"},
			want:   []string{"order-1"},
		},
		{
			name:   "a body that is not an order",
			bodies: []string{"socks", valid("order-2")},
			failed: []string{"message-0"},
			want:   []string{"order-2"},
		},
		{
			name:   "an order without lines",
			bodies: []string{`{"id": "order-1", "customerId": "customer-1", "createdAt": "2026-11-09T10:00:00Z", "lines": []}`},
			failed: []string{"message-0"},
		},
		{
			name:   "an order without a customer",
			bodies: []string{`{"id": "order-1", "lines": [{"sku": "socks", "quantity": 1}]}`},
			failed: []string{"message-0"},
		},
		{
			name:   "a table that fails on one order",
			bodies: []string{valid("order-1"), valid("order-2")},
			broken: []string{"order-1"},
			failed: []string{"message-0"},
			want:   []string{"order-2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeStore{stored: map[string]order{}, broken: map[string]bool{}}
			for _, id := range tt.stored {
				fake.stored[id] = order{ID: id}
			}
			for _, id := range tt.broken {
				fake.broken[id] = true
			}
			var batch events.SQSEvent
			for i, body := range tt.bodies {
				batch.Records = append(batch.Records, events.SQSMessage{MessageId: "message-" + strconv.Itoa(i), Body: body})
			}
			response, err := worker{orders: fake}.handle(context.Background(), batch)
			if err != nil {
				t.Fatal(err)
			}
			failed := make([]string, 0, len(response.BatchItemFailures))
			for _, item := range response.BatchItemFailures {
				failed = append(failed, item.ItemIdentifier)
			}
			if !slices.Equal(failed, tt.failed) {
				t.Errorf("failed = %v, want %v", failed, tt.failed)
			}
			stored := make([]string, 0, len(fake.stored))
			for id := range fake.stored {
				stored = append(stored, id)
			}
			slices.Sort(stored)
			if !slices.Equal(stored, tt.want) {
				t.Errorf("stored = %v, want %v", stored, tt.want)
			}
		})
	}
}

// TestDecode checks an order keeps what the api queued, lines and all.
func TestDecode(t *testing.T) {
	decoded, err := decode(valid("order-1"))
	if err != nil {
		t.Fatal(err)
	}
	want := order{ID: "order-1", CustomerID: "customer-1", Currency: "EUR", Lines: []line{{SKU: "socks", Quantity: 2}}, Status: "received", CreatedAt: "2026-11-09T10:00:00Z"}
	if !reflect.DeepEqual(decoded, want) {
		t.Errorf("decode = %+v, want %+v", decoded, want)
	}
}
