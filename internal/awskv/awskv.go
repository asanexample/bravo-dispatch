// Package awskv is a tiny JSON-document store keyed by a single string id, backed by DynamoDB when a table
// is configured and by memory otherwise. It matches the platform's self-service keyvalue contract (ADR-073):
// the Environment Composition provisions a PAY_PER_REQUEST table whose ONLY key is a String hash key `id`, and
// publishes its name into the service's <svc>-resources ConfigMap (e.g. ITEMS_TABLE). Credentials come from
// EKS Pod Identity (LoadDefaultConfig picks them up); AWS_REGION is set by the manifest. Memory mode keeps
// local dev / tests dependency-free.
package awskv

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Store is a get/put/delete of an opaque JSON document by id.
type Store interface {
	Get(ctx context.Context, id string) (doc []byte, found bool, err error)
	Put(ctx context.Context, id string, doc []byte) error
	Delete(ctx context.Context, id string) error
	// Backend reports "dynamodb" or "memory" for startup logging.
	Backend() string
}

// Open returns a DynamoDB-backed store when table is non-empty, else an in-memory store. The DynamoDB path
// loads AWS config from the ambient environment (Pod Identity creds + AWS_REGION).
func Open(ctx context.Context, table string) (Store, error) {
	if table == "" {
		return NewMemory(), nil
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &dynamoStore{db: dynamodb.NewFromConfig(cfg), table: table}, nil
}

// --- DynamoDB ---

type dynamoStore struct {
	db    *dynamodb.Client
	table string
}

func (s *dynamoStore) Backend() string { return "dynamodb" }

func (s *dynamoStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table),
		Key:       map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: id}},
	})
	if err != nil {
		return nil, false, err
	}
	if out.Item == nil {
		return nil, false, nil
	}
	if v, ok := out.Item["doc"].(*ddbtypes.AttributeValueMemberS); ok {
		return []byte(v.Value), true, nil
	}
	return nil, false, nil
}

func (s *dynamoStore) Put(ctx context.Context, id string, doc []byte) error {
	_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table),
		Item: map[string]ddbtypes.AttributeValue{
			"id":  &ddbtypes.AttributeValueMemberS{Value: id},
			"doc": &ddbtypes.AttributeValueMemberS{Value: string(doc)},
		},
	})
	return err
}

func (s *dynamoStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.table),
		Key:       map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: id}},
	})
	return err
}

// --- Memory ---

type memStore struct {
	mu sync.RWMutex
	m  map[string][]byte
}

// NewMemory returns an in-memory store (local dev / tests).
func NewMemory() Store { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Backend() string { return "memory" }

func (s *memStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[id]
	return v, ok, nil
}

func (s *memStore) Put(_ context.Context, id string, doc []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(doc))
	copy(cp, doc)
	s.m[id] = cp
	return nil
}

func (s *memStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}
