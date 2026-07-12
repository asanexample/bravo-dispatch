// Package awsqueue is a producer+consumer wrapper around a single SQS queue: send a message, and long-poll
// receive/delete for a background worker. It matches the platform's self-service stream contract (ADR-073):
// the Environment Composition provisions an SSE-enabled queue and publishes its URL into the <svc>-resources
// ConfigMap (e.g. REQUESTS_QUEUE_URL). Credentials come from EKS Pod Identity; AWS_REGION is set by the
// manifest.
//
// Unlike alpha-shop's producer-only sibling, dispatch-worker is BOTH the producer (its HTTP handler enqueues)
// and the consumer (its background loop long-polls) of the same queue, in one service — this platform's
// Crossplane Composition scopes every self-service resource to exactly one owning (service, resourceName)
// pair, so a queue can never be shared across services; producer+consumer-in-one-service is how a single
// owner still gets async decoupling.
//
// When queueURL is unset, Open returns a real in-process memory queue (a buffered channel) rather than a bare
// no-op — the same "degrades to a fully-working local backend" contract awskv's memory Store already has, so
// local dev/tests can drive the full enqueue → background-consume path without real AWS.
package awsqueue

import (
	"context"
	"strconv"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Message is one received queue entry: its body, and the receipt handle Delete needs to acknowledge it.
type Message struct {
	Body          string
	ReceiptHandle string
}

// Queue sends messages and long-polls/acks them for a background consumer.
type Queue interface {
	Send(ctx context.Context, body string) error
	// Receive long-polls for up to a batch of messages, blocking until at least one arrives, the queue is
	// closed, or ctx is done.
	Receive(ctx context.Context) ([]Message, error)
	// Delete acknowledges successful processing of a received message so it isn't redelivered.
	Delete(ctx context.Context, receiptHandle string) error
	// Backend reports "sqs" or "memory" for startup logging.
	Backend() string
}

// Open returns an SQS-backed Queue when queueURL is non-empty, else an in-memory Queue. The SQS path loads AWS
// config from the ambient environment (Pod Identity creds + AWS_REGION).
func Open(ctx context.Context, queueURL string) (Queue, error) {
	if queueURL == "" {
		return NewMemory(), nil
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &sqsQueue{cl: sqs.NewFromConfig(cfg), url: queueURL}, nil
}

// --- SQS ---

type sqsQueue struct {
	cl  *sqs.Client
	url string
}

func (q *sqsQueue) Backend() string { return "sqs" }

func (q *sqsQueue) Send(ctx context.Context, body string) error {
	_, err := q.cl.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.url),
		MessageBody: aws.String(body),
	})
	return err
}

func (q *sqsQueue) Receive(ctx context.Context) ([]Message, error) {
	out, err := q.cl.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.url),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     20, // long poll
	})
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		msgs = append(msgs, Message{Body: aws.ToString(m.Body), ReceiptHandle: aws.ToString(m.ReceiptHandle)})
	}
	return msgs, nil
}

func (q *sqsQueue) Delete(ctx context.Context, receiptHandle string) error {
	_, err := q.cl.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.url),
		ReceiptHandle: aws.String(receiptHandle),
	})
	return err
}

// --- Memory ---

// memQueue is a single-process, buffered-channel stand-in for SQS: Send deposits, Receive blocks for the next
// message (or ctx cancellation), Delete is a no-op ack (there's nothing to redeliver from in this mode).
type memQueue struct {
	ch  chan Message
	seq atomic.Uint64
}

// NewMemory returns an in-memory Queue (local dev / tests). Buffered generously so a burst of Sends never
// blocks the HTTP handler on the consumer keeping up.
func NewMemory() Queue { return &memQueue{ch: make(chan Message, 256)} }

func (q *memQueue) Backend() string { return "memory" }

func (q *memQueue) Send(ctx context.Context, body string) error {
	handle := q.seq.Add(1)
	select {
	case q.ch <- Message{Body: body, ReceiptHandle: "memory-" + strconv.FormatUint(handle, 10)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *memQueue) Receive(ctx context.Context) ([]Message, error) {
	select {
	case m := <-q.ch:
		return []Message{m}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *memQueue) Delete(_ context.Context, _ string) error { return nil }
