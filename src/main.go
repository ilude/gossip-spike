package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Create and start the gossip service
	service, err := GossipService()
	if err != nil {
		log.Fatalf("Failed to create gossip service: %v", err)
	}
	service.Start()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			nodes := service.GetNodes()
			log.Printf("Known nodes (%d):", len(nodes))
			for _, node := range nodes {
				log.Printf("  - %s (%s) at %s, last seen %s ago",
					node.Name, node.ID, node.Addr,
					time.Since(node.LastSeen).Round(time.Second))
			}
		case <-stop:
			log.Println("Shutting down...")
			service.Stop()
			return
		}
	}
}
