package main

import (
	"context"
	"log"

	"github.com/Luo-root/pulsar-dock/internal/dockmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	service := dockmcp.NewService()
	server := service.CreateServer()
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
