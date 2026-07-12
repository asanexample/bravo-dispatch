// Package awssns publishes JSON messages to an SNS topic when one is configured, and no-ops otherwise. It
// mirrors internal/awsqueue's Open shape exactly. It matches the platform's self-service stream contract
// (ADR-073): the Environment Composition provisions the topic and publishes its ARN into the <svc>-resources
// ConfigMap (e.g. EVENTS_TOPIC_ARN). Creds come from EKS Pod Identity; AWS_REGION is set by the manifest.
//
// dispatch-worker publishes here to prove the platform's SNS capability works end-to-end — nothing currently
// subscribes to this topic (see the call site's comment); a no-op fallback is therefore correct even locally,
// unlike awsqueue's memory fallback which a background consumer actually depends on.
package awssns

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// Publisher publishes a message body to a topic.
type Publisher interface {
	Publish(ctx context.Context, body string) error
	Backend() string
}

// Open returns an SNS publisher when topicArn is non-empty, else a no-op publisher.
func Open(ctx context.Context, topicArn string) (Publisher, error) {
	if topicArn == "" {
		return noop{}, nil
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &snsPublisher{cl: sns.NewFromConfig(cfg), arn: topicArn}, nil
}

type snsPublisher struct {
	cl  *sns.Client
	arn string
}

func (p *snsPublisher) Backend() string { return "sns" }

func (p *snsPublisher) Publish(ctx context.Context, body string) error {
	_, err := p.cl.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(p.arn),
		Message:  aws.String(body),
	})
	return err
}

type noop struct{}

func (noop) Backend() string                           { return "noop" }
func (noop) Publish(_ context.Context, _ string) error { return nil }
