package main

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

func Handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	
	log.Printf("Processing Lambda request %s\n", req.RequestContext.RequestID)

	name := req.QueryStringParameters["name"]
	if name == "" {
		name = "World"
	}

	resp := events.APIGatewayProxyResponse{
		Body:       fmt.Sprintf("Hello, %s!", name),
		StatusCode: 200,
	}

	return resp, nil
}

func main() {
	lambda.Start(Handler)
}