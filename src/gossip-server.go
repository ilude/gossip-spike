package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	defaultPort       = 9999             // Default port for gossip communication
	bufSize           = 1024             // Buffer size for UDP packets
	gossipInterval    = 2 * time.Second  // How often to send gossip messages
	timeoutInterval   = 10 * time.Second // Time after which a node is considered dead
	dnsLookupInterval = 5 * time.Second  // How often to perform DNS lookups
	defaultHostname   = "joyride"        // Default hostname to discover
)

// Node represents a discovered peer in the network
type Node struct {
	ID       string    `json:"id"`
	Addr     string    `json:"addr"`
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
}

// Message is the structure of messages exchanged between nodes
type Message struct {
	Type      string `json:"type"`
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name"`
	NodeAddr  string `json:"node_addr"`
	Timestamp int64  `json:"timestamp"`
}

// Service manages the gossip discovery
type Service struct {
	nodeID         string
	nodeName       string
	localAddr      string
	udpConn        *net.UDPConn
	nodes          map[string]Node
	nodesMutex     sync.RWMutex
	stopChan       chan struct{}
	wg             sync.WaitGroup
	discoveryHost  string
	discoveryPort  int
	knownAddrs     map[string]bool
	knownAddrMutex sync.RWMutex
}

// NewService creates a new gossip discovery service
func NewService(nodeName string, discoveryHost string, port int) (*Service, error) {
	// Generate a random ID for this node
	nodeID := generateNodeID()

	// Get the local IP address
	localIP, err := getLocalIP()
	if err != nil {
		return nil, fmt.Errorf("failed to get local IP: %w", err)
	}

	// Create a UDP connection for gossip communication
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP port %d: %w", port, err)
	}

	return &Service{
		nodeID:        nodeID,
		nodeName:      nodeName,
		localAddr:     localIP,
		udpConn:       conn,
		nodes:         make(map[string]Node),
		stopChan:      make(chan struct{}),
		discoveryHost: discoveryHost,
		discoveryPort: port,
		knownAddrs:    make(map[string]bool),
	}, nil
}

// Start begins the gossip service
func (s *Service) Start() {
	s.wg.Add(3) // Three goroutines: receive, broadcast, DNS discovery
	go s.receiveGossip()
	go s.broadcastLoop()
	go s.dnsDiscoveryLoop()

	log.Printf("Gossip service started. Node ID: %s, Name: %s, IP: %s", s.nodeID, s.nodeName, s.localAddr)
	log.Printf("Using DNS discovery with hostname: %s on port: %d", s.discoveryHost, s.discoveryPort)

	// Register self in the nodes map
	s.nodesMutex.Lock()
	s.nodes[s.nodeID] = Node{
		ID:       s.nodeID,
		Name:     s.nodeName,
		Addr:     s.localAddr,
		LastSeen: time.Now(),
	}
	s.nodesMutex.Unlock()
}

// Stop halts the gossip service
func (s *Service) Stop() {
	close(s.stopChan)
	s.wg.Wait()
	s.udpConn.Close()
	log.Println("Gossip service stopped")
}

// GetNodes returns a copy of the current known nodes
func (s *Service) GetNodes() []Node {
	s.nodesMutex.RLock()
	defer s.nodesMutex.RUnlock()

	nodes := make([]Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		// Only include nodes seen recently
		if time.Since(node.LastSeen) < timeoutInterval {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// dnsDiscoveryLoop periodically performs DNS lookups to discover peers
func (s *Service) dnsDiscoveryLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(dnsLookupInterval)
	defer ticker.Stop()

	// Run DNS discovery immediately at startup
	s.performDNSDiscovery()

	for {
		select {
		case <-ticker.C:
			s.performDNSDiscovery()
		case <-s.stopChan:
			return
		}
	}
}

// performDNSDiscovery looks up the discovery hostname and sends gossip messages
// to any newly discovered IP addresses
func (s *Service) performDNSDiscovery() {
	ips, err := net.LookupIP(s.discoveryHost)
	if err != nil {
		log.Printf("DNS lookup failed for %s: %v", s.discoveryHost, err)
		return
	}

	// Check if we got any IPs
	if len(ips) == 0 {
		log.Printf("No IPs found for hostname %s", s.discoveryHost)
		return
	}

	// Process discovered IPs
	newFound := false
	s.knownAddrMutex.Lock()
	defer s.knownAddrMutex.Unlock()

	for _, ip := range ips {
		ipStr := ip.String()

		// Skip our own IP
		if ipStr == s.localAddr {
			continue
		}

		// Check if this is a new IP
		if _, known := s.knownAddrs[ipStr]; !known {
			log.Printf("Discovered new peer via DNS: %s", ipStr)
			s.knownAddrs[ipStr] = true
			newFound = true

			// Send an immediate gossip message to this new peer
			go s.sendGossipMessageTo(ipStr)
		}
	}

	if newFound {
		// If we found new peers, send a gossip message to everyone
		go s.broadcastToAllPeers()
	}
}

// broadcastLoop periodically sends gossip messages
func (s *Service) broadcastLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(gossipInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.broadcastToAllPeers()
			s.cleanupStaleNodes()
		case <-s.stopChan:
			return
		}
	}
}

// broadcastToAllPeers sends a gossip message to all known peer addresses
func (s *Service) broadcastToAllPeers() {
	s.knownAddrMutex.RLock()
	defer s.knownAddrMutex.RUnlock()

	for addr := range s.knownAddrs {
		s.sendGossipMessageTo(addr)
	}
}

// sendGossipMessageTo sends a heartbeat message to a specific IP address
func (s *Service) sendGossipMessageTo(ipAddr string) {
	msg := Message{
		Type:      "heartbeat",
		NodeID:    s.nodeID,
		NodeName:  s.nodeName,
		NodeAddr:  s.localAddr,
		Timestamp: time.Now().UnixNano(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error marshaling message: %v", err)
		return
	}

	addr := &net.UDPAddr{
		IP:   net.ParseIP(ipAddr),
		Port: s.discoveryPort,
	}

	_, err = s.udpConn.WriteToUDP(data, addr)
	if err != nil {
		log.Printf("Error sending gossip message to %s: %v", ipAddr, err)
		return
	}
}

// receiveGossip listens for incoming gossip messages
func (s *Service) receiveGossip() {
	defer s.wg.Done()

	buffer := make([]byte, bufSize)

	for {
		select {
		case <-s.stopChan:
			return
		default:
			// Set a read deadline so we can check the stop channel periodically
			s.udpConn.SetReadDeadline(time.Now().Add(1 * time.Second))

			n, addr, err := s.udpConn.ReadFromUDP(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				log.Printf("Error reading UDP: %v", err)
				continue
			}

			var msg Message
			if err := json.Unmarshal(buffer[:n], &msg); err != nil {
				log.Printf("Error unmarshaling message: %v", err)
				continue
			}

			// Ignore our own messages
			if msg.NodeID == s.nodeID {
				continue
			}

			// Add the sender to our known addresses
			senderIP := addr.IP.String()
			s.knownAddrMutex.Lock()
			if _, known := s.knownAddrs[senderIP]; !known {
				s.knownAddrs[senderIP] = true
				log.Printf("Added new peer from incoming message: %s", senderIP)
			}
			s.knownAddrMutex.Unlock()

			// Update node information
			s.updateNode(msg, senderIP)
		}
	}
}

// updateNode processes a received message and updates the node registry
func (s *Service) updateNode(msg Message, senderIP string) {
	s.nodesMutex.Lock()
	defer s.nodesMutex.Unlock()

	nodeAddr := msg.NodeAddr
	if nodeAddr == "" {
		nodeAddr = senderIP
	}

	node, exists := s.nodes[msg.NodeID]
	if !exists {
		// New node discovered
		log.Printf("Discovered new node: %s (%s) at %s", msg.NodeName, msg.NodeID, nodeAddr)
	}

	// Update or create node
	s.nodes[msg.NodeID] = Node{
		ID:       msg.NodeID,
		Name:     msg.NodeName,
		Addr:     nodeAddr,
		LastSeen: time.Now(),
	}
}

// cleanupStaleNodes removes nodes that haven't been seen recently
func (s *Service) cleanupStaleNodes() {
	s.nodesMutex.Lock()
	defer s.nodesMutex.Unlock()

	for id, node := range s.nodes {
		// Never remove ourselves
		if id == s.nodeID {
			continue
		}

		if time.Since(node.LastSeen) > timeoutInterval {
			log.Printf("Node timed out: %s (%s)", node.Name, node.ID)
			delete(s.nodes, id)
		}
	}
}

// getLocalIP returns the non-loopback local IP of the host
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, address := range addrs {
		// Check the address type and if it's not a loopback
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no suitable local IP address found")
}

// generateNodeID creates a random node identifier
func generateNodeID() string {
	rand.Seed(time.Now().UnixNano())
	return fmt.Sprintf("%x", rand.Int63())
}

func main() {
	// Parse command-line flags
	port := flag.Int("port", defaultPort, "UDP port to use for gossip communication")
	flag.Parse()

	// Check for JOYRIDE_NODENAME environment variable
	nodeName := os.Getenv("JOYRIDE_NODENAME")
	if nodeName == "" {
		// If not set, use the hostname as default
		var err error
		nodeName, err = os.Hostname()
		if err != nil {
			log.Printf("Error getting hostname: %v", err)
			nodeName = fmt.Sprintf("unknown-%x", rand.Int31n(1000))
		}
	}

	// Get discovery hostname from environment variable or use default
	discoveryHost := os.Getenv("DISCOVER_HOSTNAME")
	if discoveryHost == "" {
		discoveryHost = defaultHostname
	}

	// Create and start the gossip service
	service, err := NewService(nodeName, discoveryHost, *port)
	if err != nil {
		log.Fatalf("Failed to create gossip service: %v", err)
	}

	service.Start()

	// Print discovered nodes periodically
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Handle graceful shutdown
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
