package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-lambda-go/events"
)

// line is one line of an order: a sku and how many of it.
type line struct {
	SKU      string `json:"sku" dynamodbav:"sku"`
	Quantity int    `json:"quantity" dynamodbav:"quantity"`
}

// order is an order the api accepted and queued, as the worker stores it.
type order struct {
	ID         string `json:"id" dynamodbav:"id"`
	CustomerID string `json:"customerId" dynamodbav:"customerId"`
	Currency   string `json:"currency" dynamodbav:"currency"`
	Lines      []line `json:"lines" dynamodbav:"lines"`
	Status     string `json:"status" dynamodbav:"status"`
	CreatedAt  string `json:"createdAt" dynamodbav:"createdAt"`
}

// errStored is what a store returns for an order it holds already.
var errStored = errors.New("the order is stored already")

// store is the orders table, as the worker uses it.
type store interface {
	// put stores placed, or returns errStored when an order with its id is stored already.
	put(ctx context.Context, placed order) error
}

// worker stores the orders of the jobs queue.
type worker struct {
	orders store
}

// handle stores each order of the batch once, and reports the ones it could not, so only those
// are retried. An order it can never store fails every time, and lands in jobs-dead-letters once
// the queue's redrive policy gives up on it.
func (consumer worker) handle(ctx context.Context, batch events.SQSEvent) (events.SQSEventResponse, error) {
	var failed []events.SQSBatchItemFailure
	for _, message := range batch.Records {
		log := slog.With("messageId", message.MessageId, "receives", message.Attributes["ApproximateReceiveCount"])
		placed, err := decode(message.Body)
		if err != nil {
			log.Error("reading an order", "error", err)
			failed = append(failed, events.SQSBatchItemFailure{ItemIdentifier: message.MessageId})
			continue
		}
		log = log.With("order", placed.ID)
		switch err := consumer.orders.put(ctx, placed); {
		case errors.Is(err, errStored):
			log.Info("the order was stored already")
		case err != nil:
			log.Error("storing an order", "error", err)
			failed = append(failed, events.SQSBatchItemFailure{ItemIdentifier: message.MessageId})
		default:
			log.Info("stored an order", "customer", placed.CustomerID, "lines", len(placed.Lines))
		}
	}
	return events.SQSEventResponse{BatchItemFailures: failed}, nil
}

// decode reads an order from a message's body, and checks it has what the table needs.
func decode(body string) (order, error) {
	var decoded order
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return order{}, fmt.Errorf("the body is not an order: %w", err)
	}
	switch {
	case decoded.ID == "":
		return order{}, errors.New("the order has no id")
	case decoded.CustomerID == "" || decoded.CreatedAt == "":
		return order{}, fmt.Errorf("order %s has no customerId or createdAt, which the byCustomer index needs", decoded.ID)
	case len(decoded.Lines) == 0:
		return order{}, fmt.Errorf("order %s has no lines", decoded.ID)
	}
	return decoded, nil
}
