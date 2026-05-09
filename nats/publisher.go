package nats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	commandsv1 "medsage/proto/medsage/commands/v1"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

const (
	CommandsStreamName = "COMMANDS"
	SubjectEmailSend   = "medsage.commands.email.send"
)

// Publisher publishes commands to the COMMANDS JetStream stream. Commands
// describe pending work for some other service to perform (e.g., send an
// email). Failure to publish surfaces as an error to the caller; once
// published, JetStream durably retains the message until consumed.
type Publisher struct {
	conn *nats.Conn
	js   jetstream.JetStream
}

func ConnectPublisher(url string) (*Publisher, error) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	if _, err := js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name:     CommandsStreamName,
		Subjects: []string{"medsage.commands.>"},
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream create stream: %w", err)
	}

	slog.Info("NATS commands publisher connected", "url", url, "stream", CommandsStreamName)
	return &Publisher{conn: nc, js: js}, nil
}

// PublishSendEmail publishes a SendEmail command. JetStream durably persists
// the message; callers should treat success as "queued for delivery," not
// "delivered."
func (p *Publisher) PublishSendEmail(ctx context.Context, cmd *commandsv1.SendEmail) error {
	data, err := proto.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshal SendEmail: %w", err)
	}
	if _, err := p.js.Publish(ctx, SubjectEmailSend, data); err != nil {
		return fmt.Errorf("publish %s: %w", SubjectEmailSend, err)
	}
	slog.Debug("Published SendEmail command",
		"command_id", cmd.CommandId,
		"to", cmd.To,
		"source_ref", cmd.SourceRef,
	)
	return nil
}

func (p *Publisher) Close() {
	if p.conn != nil {
		p.conn.Drain()
	}
}
