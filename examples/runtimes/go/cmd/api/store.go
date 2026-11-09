package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// flag is a feature flag as the table keeps it. Version counts its changes, from 1, so a change
// can say which version it was made to.
type flag struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

// The errors of a store.
var (
	errNotFound = errors.New("no such flag")
	errConflict = errors.New("the flag is not at the version the change was made to")
)

// store is the flags table, as the api uses it.
type store interface {
	// get returns the flag called name, or errNotFound.
	get(ctx context.Context, name string) (flag, error)
	// put stores changed when the stored flag is at changed.Version - 1, or when there is none
	// and changed.Version is 1, and returns errConflict otherwise.
	put(ctx context.Context, changed flag) error
}

// key is the attribute the flags table is keyed by, kdst.table("name") in koldstart.pkl.
const key = "name"

// table is the flags table in dynamodb, keyed by name.
type table struct {
	client *dynamodb.Client
	name   string
}

func (database table) get(ctx context.Context, name string) (flag, error) {
	output, err := database.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(database.name),
		Key:            map[string]types.AttributeValue{key: &types.AttributeValueMemberS{Value: name}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return flag{}, fmt.Errorf("reading flag %s: %w", name, err)
	}
	if output.Item == nil {
		return flag{}, errNotFound
	}
	return decode(output.Item)
}

func (database table) put(ctx context.Context, changed flag) error {
	input := &dynamodb.PutItemInput{
		TableName: aws.String(database.name),
		Item: map[string]types.AttributeValue{
			key:           &types.AttributeValueMemberS{Value: changed.Name},
			"enabled":     &types.AttributeValueMemberBOOL{Value: changed.Enabled},
			"description": &types.AttributeValueMemberS{Value: changed.Description},
			"version":     &types.AttributeValueMemberN{Value: strconv.Itoa(changed.Version)},
			"updatedAt":   &types.AttributeValueMemberS{Value: changed.UpdatedAt},
		},
		ExpressionAttributeNames: map[string]string{"#name": key},
		ConditionExpression:      aws.String("attribute_not_exists(#name)"),
	}
	if changed.Version > 1 {
		input.ExpressionAttributeNames = map[string]string{"#version": "version"}
		input.ConditionExpression = aws.String("#version = :previous")
		input.ExpressionAttributeValues = map[string]types.AttributeValue{
			":previous": &types.AttributeValueMemberN{Value: strconv.Itoa(changed.Version - 1)},
		}
	}
	_, err := database.client.PutItem(ctx, input)
	if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
		return errConflict
	}
	if err != nil {
		return fmt.Errorf("storing flag %s: %w", changed.Name, err)
	}
	return nil
}

// decode reads a flag from an item of the table.
func decode(item map[string]types.AttributeValue) (flag, error) {
	var decoded flag
	if value, ok := item[key].(*types.AttributeValueMemberS); ok {
		decoded.Name = value.Value
	}
	if value, ok := item["enabled"].(*types.AttributeValueMemberBOOL); ok {
		decoded.Enabled = value.Value
	}
	if value, ok := item["description"].(*types.AttributeValueMemberS); ok {
		decoded.Description = value.Value
	}
	if value, ok := item["updatedAt"].(*types.AttributeValueMemberS); ok {
		decoded.UpdatedAt = value.Value
	}
	value, ok := item["version"].(*types.AttributeValueMemberN)
	if !ok {
		return flag{}, fmt.Errorf("flag %s has no version", decoded.Name)
	}
	version, err := strconv.Atoi(value.Value)
	if err != nil {
		return flag{}, fmt.Errorf("flag %s has the version %q: %w", decoded.Name, value.Value, err)
	}
	decoded.Version = version
	return decoded, nil
}
