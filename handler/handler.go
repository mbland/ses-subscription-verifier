package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/mbland/elistman/ops"
)

const defaultResponseLocation = "https://github.com/mbland/elistman"

type LambdaHandler struct {
	SubscribeHandler ops.SubscribeHandler
	VerifyHandler    ops.VerifyHandler
}

func getEndpoint(request events.APIGatewayV2HTTPRequest) string {
	if request.RouteKey == "" {
		return request.RawPath
	}
	route_prefix := fmt.Sprintf("/%s", request.RouteKey)
	return strings.TrimPrefix(request.RawPath, route_prefix)
}

func (h LambdaHandler) HandleEvent(event Event) (any, error) {
	switch event.Type {
	case ApiRequest:
		return h.HandleApiRequest(event.ApiRequest)
	case MailtoEvent:
		return nil, h.HandleMailtoEvent(event.MailtoEvent)
	case NullEvent:
		return nil, fmt.Errorf("event payload is null")
	}
	return nil, fmt.Errorf("unknown event: %+v", event)
}

func (h LambdaHandler) HandleApiRequest(
	request events.APIGatewayV2HTTPRequest,
) (events.APIGatewayV2HTTPResponse, error) {
	endpoint := getEndpoint(request)
	response := events.APIGatewayV2HTTPResponse{Headers: make(map[string]string)}
	response.StatusCode = http.StatusSeeOther
	response.Headers["Location"] = defaultResponseLocation

	if endpoint == "/subscribe" {
		h.SubscribeHandler.HandleRequest()

	} else if endpoint == "/verify" {
		h.VerifyHandler.HandleRequest()

	} else {
		response.StatusCode = http.StatusNotFound
	}
	return response, nil
}

func (h LambdaHandler) HandleMailtoEvent(event events.SimpleEmailEvent) error {
	return nil
}
