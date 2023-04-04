package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/mbland/elistman/db"
	"github.com/mbland/elistman/email"
	"github.com/mbland/elistman/handler"
	"github.com/mbland/elistman/ops"
)

func buildHandler() (*handler.Handler, error) {
	if cfg, err := config.LoadDefaultConfig(context.TODO()); err != nil {
		return nil, err
	} else if opts, err := handler.GetOptions(os.Getenv); err != nil {
		return nil, err
	} else {
		return handler.NewHandler(
			opts.EmailDomainName,
			opts.EmailSiteTitle,
			&ops.ProdAgent{
				Db:        db.NewDynamoDb(cfg, opts.SubscribersTableName),
				Validator: email.AddressValidatorImpl{},
				Mailer:    email.NewSesMailer(cfg),
			},
			opts.RedirectPaths,
			handler.ResponseTemplate,
			log.Default(),
		)
	}
}

func main() {
	// Disable standard logger flags. The CloudWatch logs show that the Lambda
	// runtime already adds a timestamp at the beginning of every log line
	// emitted by the function.
	log.SetFlags(0)

	if h, err := buildHandler(); err != nil {
		log.Fatalf("Failed to initialize process: %s", err.Error())
	} else {
		lambda.Start(h.HandleEvent)
	}
}
