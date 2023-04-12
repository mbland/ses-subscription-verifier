package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/mbland/elistman/ops"
)

type snsHandler struct {
	Agent ops.SubscriptionAgent
	Log   *log.Logger
}

// https://docs.aws.amazon.com/ses/latest/dg/event-publishing-retrieving-sns-contents.html
// https://docs.aws.amazon.com/ses/latest/dg/event-publishing-retrieving-sns-examples.html
func (h *snsHandler) HandleEvent(e *events.SNSEvent) {
	for _, snsRecord := range e.Records {
		msg := snsRecord.SNS.Message
		if sesHandler, err := newSesEventHandler(h, msg); err != nil {
			h.Log.Printf("parsing SES event from SNS failed: %s: %s", err, msg)
		} else {
			sesHandler.HandleEvent()
		}
	}
}

type sesEventHandler interface {
	HandleEvent()
}

func newSesEventHandler(
	sns *snsHandler, message string,
) (handler sesEventHandler, err error) {
	origEvent := &SesEventRecord{}
	if jsonErr := json.Unmarshal([]byte(message), origEvent); jsonErr != nil {
		return nil, jsonErr
	}

	mail := origEvent.Mail
	base := baseSesEventHandler{
		Type:      origEvent.EventType,
		MessageId: mail.MessageID,
		To:        mail.CommonHeaders.To,
		From:      mail.CommonHeaders.From,
		Subject:   mail.CommonHeaders.Subject,
		Details:   message,
		Agent:     sns.Agent,
		Log:       sns.Log,
	}

	switch base.Type {
	case "Bounce":
		handler = &bounceHandler{
			baseSesEventHandler: base,
			BounceType:          origEvent.Bounce.BounceType,
			BounceSubType:       origEvent.Bounce.BounceSubType,
		}
	case "Complaint":
		handler = &complaintHandler{
			baseSesEventHandler:   base,
			ComplaintSubType:      origEvent.Complaint.ComplaintSubType,
			ComplaintFeedbackType: origEvent.Complaint.ComplaintFeedbackType,
		}
	case "Reject":
		handler = &rejectHandler{
			baseSesEventHandler: base,
			Reason:              origEvent.Reject.Reason,
		}
	case "Send", "Delivery":
		handler = &base
	default:
		err = fmt.Errorf("unimplemented event type: %s", base.Type)
	}
	return
}

type baseSesEventHandler struct {
	Type      string
	MessageId string
	From      []string
	To        []string
	Subject   string
	Details   string
	Agent     ops.SubscriptionAgent
	Log       *log.Logger
}

func (evh *baseSesEventHandler) HandleEvent() {
	evh.logOutcome("success")
}

func (evh *baseSesEventHandler) logOutcome(outcome string) {
	evh.Log.Printf(
		`%s [Id:"%s" From:"%s" To:"%s" Subject:"%s"]: %s: %s`,
		evh.Type,
		evh.MessageId,
		strings.Join(evh.From, ","),
		strings.Join(evh.To, ","),
		evh.Subject,
		outcome,
		evh.Details,
	)
}

func (evh *baseSesEventHandler) removeRecipients(reason string) {
	evh.updateRecipients(
		reason,
		&recipientUpdater{evh.Agent.Remove, "removed", "error removing"},
	)
}

func (evh *baseSesEventHandler) restoreRecipients(reason string) {
	evh.updateRecipients(
		reason,
		&recipientUpdater{evh.Agent.Restore, "restored", "error restoring"},
	)
}

func (evh *baseSesEventHandler) updateRecipients(
	reason string, up *recipientUpdater,
) {
	for _, email := range evh.To {
		evh.logOutcome(up.updateRecipient(email, reason))
	}
}

type recipientUpdater struct {
	action        func(string) error
	successPrefix string
	errPrefix     string
}

func (up *recipientUpdater) updateRecipient(email, reason string) string {
	emailAndReason := " " + email + " due to: " + reason

	if err := up.action(email); err != nil {
		return up.errPrefix + emailAndReason + ": " + err.Error()
	}
	return up.successPrefix + emailAndReason
}

type bounceHandler struct {
	baseSesEventHandler
	BounceType    string
	BounceSubType string
}

func (evh *bounceHandler) HandleEvent() {
	reason := evh.BounceType + "/" + evh.BounceSubType
	if evh.BounceType == "Transient" {
		evh.logOutcome("not removing recipients: " + reason)
	} else {
		evh.removeRecipients(reason)
	}
}

type complaintHandler struct {
	baseSesEventHandler
	ComplaintSubType      string
	ComplaintFeedbackType string
}

func (evh *complaintHandler) HandleEvent() {
	reason := evh.ComplaintSubType
	if reason == "" {
		reason = evh.ComplaintFeedbackType
	}
	if reason == "" {
		reason = "unknown"
	}

	if reason == "not-spam" {
		evh.restoreRecipients(reason)
	} else {
		evh.removeRecipients(reason)
	}
}

type rejectHandler struct {
	baseSesEventHandler
	Reason string
}

func (evh *rejectHandler) HandleEvent() {
	evh.logOutcome(evh.Reason)
}
