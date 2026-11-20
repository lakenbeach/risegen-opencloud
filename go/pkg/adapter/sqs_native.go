// ────────────────────────────────────────────────────────────────────────────
// pkg/adapter/sqs_native.go
// Cliente SQS nativo (aws-sdk-go-v2) — sem dependência do CDK.
// ────────────────────────────────────────────────────────────────────────────
package adapter

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// NativeSQSClient wraps the aws-sdk-go-v2 SQS client.
type NativeSQSClient struct {
	client *sqs.Client
}

// NativeSQSMessage represents a single SQS message returned by ReceiveMessages.
type NativeSQSMessage struct {
	MessageID     string `json:"message_id"`
	Body          string `json:"body"`
	ReceiptHandle string `json:"receipt_handle"`
}

// NativeSQSReceiveResult holds the messages returned by ReceiveMessages.
type NativeSQSReceiveResult struct {
	Messages []NativeSQSMessage `json:"messages"`
}

// OpenNativeSQSClient creates a new native SQS client.
// Endpoint may be empty for AWS production, or a local URL for LocalStack/testing.
func OpenNativeSQSClient(endpoint, region, accessKeyID, secretAccessKey string) (*NativeSQSClient, error) {
	if region == "" {
		region = "us-east-1"
	}

	resolver := aws.EndpointResolverWithOptionsFunc(
		func(service, r string, options ...interface{}) (aws.Endpoint, error) {
			if endpoint != "" {
				return aws.Endpoint{URL: endpoint, SigningRegion: region}, nil
			}
			return aws.Endpoint{}, &aws.EndpointNotFoundError{}
		},
	)

	cfg := aws.Config{
		Region:                      region,
		Credentials:                 aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
		EndpointResolverWithOptions: resolver,
		HTTPClient:                  &http.Client{},
	}

	client := sqs.NewFromConfig(cfg)

	log.Printf("[SQS] native client opened endpoint=%q region=%q", endpoint, region)
	return &NativeSQSClient{client: client}, nil
}

// NativeSQSSendMessage sends a single message to the specified queue.
func NativeSQSSendMessage(ctx context.Context, c *NativeSQSClient, queueURL, messageBody string) error {
	if c == nil {
		return fmt.Errorf("SQS client is nil")
	}

	input := &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(messageBody),
	}

	out, err := c.client.SendMessage(ctx, input)
	if err != nil {
		log.Printf("[SQS] SendMessage error: %v", err)
		return fmt.Errorf("SQS SendMessage: %w", err)
	}

	log.Printf("[SQS] SendMessage ok id=%s", aws.ToString(out.MessageId))
	return nil
}

// NativeSQSReceiveMessages receives up to maxMessages from the queue.
func NativeSQSReceiveMessages(ctx context.Context, c *NativeSQSClient, queueURL string, maxMessages, waitTimeSeconds, visibilityTimeout int) (*NativeSQSReceiveResult, error) {
	if c == nil {
		return nil, fmt.Errorf("SQS client is nil")
	}

	if maxMessages < 1 {
		maxMessages = 1
	}
	if maxMessages > 10 {
		maxMessages = 10
	}

	input := &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		MaxNumberOfMessages: int32(maxMessages),
		WaitTimeSeconds:     int32(waitTimeSeconds),
		VisibilityTimeout:   int32(visibilityTimeout),
	}

	out, err := c.client.ReceiveMessage(ctx, input)
	if err != nil {
		log.Printf("[SQS] ReceiveMessage error: %v", err)
		return nil, fmt.Errorf("SQS ReceiveMessage: %w", err)
	}

	result := &NativeSQSReceiveResult{
		Messages: make([]NativeSQSMessage, 0, len(out.Messages)),
	}
	for _, m := range out.Messages {
		result.Messages = append(result.Messages, NativeSQSMessage{
			MessageID:     aws.ToString(m.MessageId),
			Body:          aws.ToString(m.Body),
			ReceiptHandle: aws.ToString(m.ReceiptHandle),
		})
	}

	log.Printf("[SQS] ReceiveMessage ok count=%d", len(result.Messages))
	return result, nil
}

// NativeSQSDeleteMessage deletes a message from the queue using its receipt handle.
func NativeSQSDeleteMessage(ctx context.Context, c *NativeSQSClient, queueURL, receiptHandle string) error {
	if c == nil {
		return fmt.Errorf("SQS client is nil")
	}

	input := &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	}

	_, err := c.client.DeleteMessage(ctx, input)
	if err != nil {
		log.Printf("[SQS] DeleteMessage error: %v", err)
		return fmt.Errorf("SQS DeleteMessage: %w", err)
	}

	log.Printf("[SQS] DeleteMessage ok")
	return nil
}
