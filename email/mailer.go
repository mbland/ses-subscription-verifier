package email

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
)

type Mailer interface {
	Send(
		ctx context.Context, recipient string, msg []byte,
	) (messageId string, err error)
}

type Bouncer interface {
	Bounce(
		ctx context.Context,
		emailDomain,
		messageId string,
		recipients []string,
		timestamp time.Time,
	) (string, error)
}

type SesMailer struct {
	Client    SesApi
	ConfigSet string
}

type SesApi interface {
	SendRawEmail(
		context.Context, *ses.SendRawEmailInput, ...func(*ses.Options),
	) (*ses.SendRawEmailOutput, error)

	SendBounce(
		context.Context, *ses.SendBounceInput, ...func(*ses.Options),
	) (*ses.SendBounceOutput, error)
}

func (mailer *SesMailer) Send(
	ctx context.Context, recipient string, msg []byte,
) (messageId string, err error) {
	sesMsg := &ses.SendRawEmailInput{
		Destinations:         []string{recipient},
		ConfigurationSetName: aws.String(mailer.ConfigSet),
		RawMessage:           &types.RawMessage{Data: msg},
	}
	var output *ses.SendRawEmailOutput

	if output, err = mailer.Client.SendRawEmail(ctx, sesMsg); err != nil {
		err = fmt.Errorf("send failed: %s", err)
	} else {
		messageId = aws.ToString(output.MessageId)
	}
	return
}

// https://docs.aws.amazon.com/ses/latest/dg/receiving-email-action-lambda-example-functions.html
func (mailer *SesMailer) Bounce(
	ctx context.Context,
	emailDomain,
	messageId string,
	recipients []string,
	timestamp time.Time,
) (bounceMessageId string, err error) {
	recipientInfo := make([]types.BouncedRecipientInfo, len(recipients))

	for i, recipient := range recipients {
		recipientInfo[i].Recipient = aws.String(recipient)
		recipientInfo[i].BounceType = types.BounceTypeContentRejected
	}

	input := &ses.SendBounceInput{
		BounceSender:      aws.String("mailer-daemon@" + emailDomain),
		OriginalMessageId: aws.String(messageId),
		MessageDsn: &types.MessageDsn{
			ReportingMta: aws.String("dns; " + emailDomain),
			ArrivalDate:  aws.Time(timestamp.Truncate(time.Second)),
		},
		Explanation: aws.String(
			"Unauthenticated email is not accepted due to " +
				"the sending domain's DMARC policy.",
		),
		BouncedRecipientInfoList: recipientInfo,
	}
	var output *ses.SendBounceOutput

	if output, err = mailer.Client.SendBounce(ctx, input); err != nil {
		err = fmt.Errorf("sending bounce failed: %s", err)
	} else {
		bounceMessageId = aws.ToString(output.MessageId)
	}
	return
}
