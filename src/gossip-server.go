package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultPort       = 4709             // Default port for gossip communication
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

// GossipService creates a new gossip discovery service
func GossipService() (*Service, error) {
	// Generate a random ID for this node
	nodeID := generateNodeID()

	// Get node name from environment or default to hostname
	nodeName := os.Getenv("JOYRIDE_NODENAME")
	if nodeName == "" {
		var err error
		nodeName, err = os.Hostname()
		if err != nil {
			log.Printf("Error getting hostname: %v", err)
			nodeName = "unknown-" + strconv.Itoa(rand.Intn(1000))
		}
	}

	// Get port from environment or use default
	port := defaultPort
	if portStr := os.Getenv("DISCOVER_PORT"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		} else {
			log.Printf("Invalid DISCOVER_PORT value: %s, using default: %d", portStr, defaultPort)
		}
	}

	// Get discovery hostname from environment or use default
	discoveryHost := os.Getenv("DISCOVER_HOSTNAME")
	if discoveryHost == "" {
		discoveryHost = defaultHostname
	}

	// Get the local IP address
	localIP, err := getBestLocalIP()
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
		nodes = append(nodes, node)
		// Uncomment the following lines to filter nodes based on last seen time
		// Only include nodes seen recently
		// if time.Since(node.LastSeen) < timeoutInterval {
		// 	nodes = append(nodes, node)
		// }
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

	var ips []net.IP

	if ip := net.ParseIP(s.discoveryHost); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		ips, err = net.LookupIP(s.discoveryHost)
		if err != nil {
			log.Printf("DNS lookup failed for %s: %v", s.discoveryHost, err)
			return
		}
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

	_, exists := s.nodes[msg.NodeID]
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

// getBestLocalIP attempts to determine the best IP address to use for gossiping,
// trying to find Docker host IP if possible
func getBestLocalIP() (string, error) {
	// First try to get Docker host IP via gateway or environment variables
	dockerHostIP := getDockerHostIP()
	if dockerHostIP != "" {
		log.Printf("Using Docker host IP: %s", dockerHostIP)
		return dockerHostIP, nil
	}

	// If that fails, fall back to container's IP
	return getContainerIP()
}

// getDockerHostIP attempts several methods to find the Docker host IP
func getDockerHostIP() string {
	// Method 1: Check if Docker host address is defined by environment variable
	if hostIP := os.Getenv("DOCKER_HOST_IP"); hostIP != "" {
		return hostIP
	}

	// Method 2: Check if we can resolve special Docker DNS names
	// In some Docker setups, 'host.docker.internal' points to the host
	ips, err := net.LookupIP("host.docker.internal")
	if err == nil && len(ips) > 0 {
		return ips[0].String()
	}

	// Method 3: Try to get the gateway IP by looking at routes
	routes, err := net.InterfaceAddrs()
	if err == nil {
		for _, route := range routes {
			if ipnet, ok := route.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					// Try to get the default gateway (typically .1 in the subnet)
					ipParts := strings.Split(ipnet.IP.String(), ".")
					if len(ipParts) == 4 {
						// Guess that the gateway is likely to be X.X.X.1
						gateway := fmt.Sprintf("%s.%s.%s.1", ipParts[0], ipParts[1], ipParts[2])
						if gateway != ipnet.IP.String() { // Avoid using our own IP
							return gateway
						}
					}
				}
			}
		}
	}

	// No Docker host IP found
	return ""
}

// getContainerIP returns the container's own IP address
func getContainerIP() (string, error) {
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
