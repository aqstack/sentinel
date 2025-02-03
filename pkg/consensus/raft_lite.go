// Package consensus implements a lightweight Raft-inspired consensus protocol
// optimized for small edge clusters (3-10 nodes) during network partitions.
//
// This is a key novel contribution: enabling autonomous local decision-making
// when edge nodes are partitioned from the Kubernetes control plane.
package consensus

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net"
	"sync"
	"time"
)

// NodeState represents the state of a node in the consensus protocol.
type NodeState int

const (
	Follower NodeState = iota
	Candidate
	Leader
)

func (s NodeState) String() string {
	switch s {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	default:
		return "unknown"
	}
}

// Decision represents an autonomous decision made during partition.
type Decision struct {
	ID        string          `json:"id"`
	Type      DecisionType    `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Term      uint64          `json:"term"`
	LeaderID  string          `json:"leader_id"`
	Payload   json.RawMessage `json:"payload"`
	Committed bool            `json:"committed"`
}

// DecisionType categorizes autonomous decisions.
type DecisionType string

const (
	DecisionPodReschedule   DecisionType = "pod_reschedule"
	DecisionNodeCordon      DecisionType = "node_cordon"
	DecisionServiceFailover DecisionType = "service_failover"
	DecisionResourceScale   DecisionType = "resource_scale"
)

// RateLimitConfig configures rate limiting for consensus messages.
type RateLimitConfig struct {
	// MaxMessagesPerSecond is the maximum messages allowed per second per peer.
	MaxMessagesPerSecond int
	// BurstSize is the maximum burst of messages allowed.
	BurstSize int
	// Enabled determines if rate limiting is active.
	Enabled bool
}

// DefaultRateLimitConfig returns a sensible default rate limit configuration.
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		MaxMessagesPerSecond: 100,
		BurstSize:            20,
		Enabled:              true,
	}
}

// Config configures the Raft-lite consensus node.
type Config struct {
	// NodeID is the unique identifier for this node.
	NodeID string

	// Peers is the list of peer node addresses.
	Peers []string

	// ListenAddr is the address to listen for peer connections.
	ListenAddr string

	// ElectionTimeout is the base timeout for leader election.
	// Actual timeout is randomized: [ElectionTimeout, 2*ElectionTimeout]
	ElectionTimeout time.Duration

	// HeartbeatInterval is how often the leader sends heartbeats.
	HeartbeatInterval time.Duration

	// RateLimit configures rate limiting for incoming messages.
	RateLimit *RateLimitConfig

	// DecisionCallback is called when a decision is committed.
	DecisionCallback func(Decision)

	// PartitionCallback is called when partition status changes.
	PartitionCallback func(partitioned bool)
}

// DefaultConfig returns a default configuration suitable for edge clusters.
func DefaultConfig(nodeID string) *Config {
	return &Config{
		NodeID:            nodeID,
		ElectionTimeout:   150 * time.Millisecond,
		HeartbeatInterval: 50 * time.Millisecond,
		RateLimit:         DefaultRateLimitConfig(),
	}
}

// Node represents a Raft-lite consensus node.
type Node struct {
	config *Config

	mu          sync.RWMutex
	state       NodeState
	currentTerm uint64
	votedFor    string
	leaderID    string
	lastContact time.Time

	// Decision log
	decisions   []Decision
	commitIndex int
	lastApplied int

	// Partition detection
	partitioned    bool
	partitionStart time.Time

	// Peer connections
	peers    map[string]*peerConn
	listener net.Listener

	// Rate limiting for incoming connections
	incomingLimiters   map[string]*tokenBucket
	incomingLimitersMu sync.RWMutex
	rateLimitDropped   uint64 // Counter for dropped messages

	// Control
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type peerConn struct {
	addr        string
	conn        net.Conn
	lastSeen    time.Time
	healthy     bool
	rateLimiter *tokenBucket
}

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	mu         sync.Mutex
	tokens     int
	maxTokens  int
	refillRate int // tokens per second
	lastRefill time.Time
}

func newTokenBucket(maxTokens, refillRate int) *tokenBucket {
	return &tokenBucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// Allow checks if a request is allowed and consumes a token if so.
func (tb *tokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	// Refill tokens based on time elapsed
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill)
	tokensToAdd := int(elapsed.Seconds() * float64(tb.refillRate))
	if tokensToAdd > 0 {
		tb.tokens = min(tb.tokens+tokensToAdd, tb.maxTokens)
		tb.lastRefill = now
	}

	if tb.tokens > 0 {
		tb.tokens--
		return true
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// getOrCreateLimiter returns a rate limiter for the given peer address.
func (n *Node) getOrCreateLimiter(addr string) *tokenBucket {
	n.incomingLimitersMu.RLock()
	limiter, exists := n.incomingLimiters[addr]
	n.incomingLimitersMu.RUnlock()

	if exists {
		return limiter
	}

	n.incomingLimitersMu.Lock()
	defer n.incomingLimitersMu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists = n.incomingLimiters[addr]; exists {
		return limiter
	}

	limiter = newTokenBucket(
		n.config.RateLimit.BurstSize,
		n.config.RateLimit.MaxMessagesPerSecond,
	)
	n.incomingLimiters[addr] = limiter
	return limiter
}

// RateLimitStats returns rate limiting statistics.
type RateLimitStats struct {
	DroppedMessages uint64 `json:"dropped_messages"`
	Enabled         bool   `json:"enabled"`
}

// GetRateLimitStats returns current rate limiting statistics.
func (n *Node) GetRateLimitStats() RateLimitStats {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return RateLimitStats{
		DroppedMessages: n.rateLimitDropped,
		Enabled:         n.config.RateLimit.Enabled,
	}
}

// Message types for peer communication.
type MessageType int

const (
	MsgVoteRequest MessageType = iota
	MsgVoteResponse
	MsgHeartbeat
	MsgHeartbeatResponse
	MsgDecisionProposal
	MsgDecisionAck
)

// Message is the wire format for peer communication.
type Message struct {
	Type   MessageType `json:"type"`
	Term   uint64      `json:"term"`
	FromID string      `json:"from_id"`
	ToID   string      `json:"to_id"`

	// Vote request/response fields
	LastLogIndex int    `json:"last_log_index,omitempty"`
	LastLogTerm  uint64 `json:"last_log_term,omitempty"`
	VoteGranted  bool   `json:"vote_granted,omitempty"`

	// Heartbeat fields
	LeaderID     string     `json:"leader_id,omitempty"`
	LeaderCommit int        `json:"leader_commit,omitempty"`
	Decisions    []Decision `json:"decisions,omitempty"`

	// Decision fields
	Decision *Decision `json:"decision,omitempty"`
	Success  bool      `json:"success,omitempty"`
}

// NewNode creates a new Raft-lite consensus node.
func NewNode(config *Config) (*Node, error) {
	if config.NodeID == "" {
		return nil, fmt.Errorf("node ID is required")
	}

	// Seed the random number generator with cryptographically secure randomness
	var seed int64
	if err := binary.Read(rand.Reader, binary.BigEndian, &seed); err != nil {
		// Fallback to time-based seed if crypto/rand fails
		seed = time.Now().UnixNano()
	}
	mathrand.Seed(seed)

	ctx, cancel := context.WithCancel(context.Background())

	// Ensure rate limit config is set
	if config.RateLimit == nil {
		config.RateLimit = DefaultRateLimitConfig()
	}

	n := &Node{
		config:           config,
		state:            Follower,
		lastContact:      time.Now(),
		peers:            make(map[string]*peerConn),
		incomingLimiters: make(map[string]*tokenBucket),
		ctx:              ctx,
		cancel:           cancel,
	}

	return n, nil
}

// Start begins the consensus protocol.
func (n *Node) Start() error {
	// Start listener if address configured
	if n.config.ListenAddr != "" {
		listener, err := net.Listen("tcp", n.config.ListenAddr)
		if err != nil {
			return fmt.Errorf("failed to start listener: %w", err)
		}
		n.listener = listener
		n.wg.Add(1)
		go n.acceptLoop()
	}

	// Connect to peers
	for _, addr := range n.config.Peers {
		n.peers[addr] = &peerConn{addr: addr}
	}
	n.wg.Add(1)
	go n.peerConnector()

	// Start election timer
	n.wg.Add(1)
	go n.electionLoop()

	// Start partition detector
	n.wg.Add(1)
	go n.partitionDetector()

	return nil
}

// Stop gracefully stops the consensus node.
func (n *Node) Stop() error {
	n.cancel()

	if n.listener != nil {
		n.listener.Close()
	}

	// Close peer connections
	n.mu.Lock()
	for _, peer := range n.peers {
		if peer.conn != nil {
			peer.conn.Close()
		}
	}
	n.mu.Unlock()

	n.wg.Wait()
	return nil
}

// State returns the current node state.
func (n *Node) State() NodeState {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state
}

// Term returns the current term.
func (n *Node) Term() uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.currentTerm
}

// IsLeader returns true if this node is the leader.
func (n *Node) IsLeader() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state == Leader
}

// LeaderID returns the current leader's ID.
func (n *Node) LeaderID() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.leaderID
}

// IsPartitioned returns true if the node detects a network partition.
func (n *Node) IsPartitioned() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.partitioned
}

// PartitionDuration returns how long the current partition has lasted.
func (n *Node) PartitionDuration() time.Duration {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if !n.partitioned {
		return 0
	}
	return time.Since(n.partitionStart)
}

// ProposeDecision proposes a new autonomous decision.
// Only the leader can propose decisions.
func (n *Node) ProposeDecision(ctx context.Context, decType DecisionType, payload json.RawMessage) (*Decision, error) {
	n.mu.Lock()
	if n.state != Leader {
		n.mu.Unlock()
		return nil, fmt.Errorf("not the leader")
	}

	decision := Decision{
		ID:        fmt.Sprintf("%s-%d-%d", n.config.NodeID, n.currentTerm, len(n.decisions)),
		Type:      decType,
		Timestamp: time.Now(),
		Term:      n.currentTerm,
		LeaderID:  n.config.NodeID,
		Payload:   payload,
		Committed: false,
	}

	n.decisions = append(n.decisions, decision)
	n.mu.Unlock()

	// Replicate to peers
	acks := 1 // Count self
	required := (len(n.peers) / 2) + 1

	for _, peer := range n.peers {
		if peer.conn == nil {
			continue
		}

		msg := Message{
			Type:     MsgDecisionProposal,
			Term:     n.currentTerm,
			FromID:   n.config.NodeID,
			Decision: &decision,
		}

		if err := n.sendMessage(peer.conn, &msg); err != nil {
			continue
		}

		// Wait for ack (with timeout)
		peer.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		resp, err := n.recvMessage(peer.conn)
		if err == nil && resp.Success {
			acks++
		}
	}

	// Check if we have quorum
	if acks >= required {
		n.mu.Lock()
		decision.Committed = true
		n.decisions[len(n.decisions)-1] = decision
		n.commitIndex = len(n.decisions) - 1
		n.mu.Unlock()

		// Notify callback
		if n.config.DecisionCallback != nil {
			n.config.DecisionCallback(decision)
		}

		return &decision, nil
	}

	return nil, fmt.Errorf("failed to reach quorum: got %d, need %d", acks, required)
}

// GetDecisions returns all committed decisions.
func (n *Node) GetDecisions() []Decision {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var committed []Decision
	for _, d := range n.decisions {
		if d.Committed {
			committed = append(committed, d)
		}
	}
	return committed
}

// GetUnreconciledDecisions returns decisions made during partition that need reconciliation.
func (n *Node) GetUnreconciledDecisions() []Decision {
	n.mu.RLock()
	defer n.mu.RUnlock()

	// Return all committed decisions - caller is responsible for reconciliation
	return n.GetDecisions()
}

func (n *Node) acceptLoop() {
	defer n.wg.Done()

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
		}

		n.listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
		conn, err := n.listener.Accept()
		if err != nil {
			continue
		}

		go n.handleConnection(conn)
	}
}

func (n *Node) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Get peer address for rate limiting
	remoteAddr := conn.RemoteAddr().String()
	var limiter *tokenBucket
	if n.config.RateLimit.Enabled {
		limiter = n.getOrCreateLimiter(remoteAddr)
	}

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(time.Second))
		msg, err := n.recvMessage(conn)
		if err != nil {
			return
		}

		// Apply rate limiting if enabled
		if limiter != nil && !limiter.Allow() {
			n.mu.Lock()
			n.rateLimitDropped++
			n.mu.Unlock()
			// Drop the message silently - don't respond
			continue
		}

		resp := n.handleMessage(msg)
		if resp != nil {
			n.sendMessage(conn, resp)
		}
	}
}

func (n *Node) handleMessage(msg *Message) *Message {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Update term if we see a higher one
	if msg.Term > n.currentTerm {
		n.currentTerm = msg.Term
		n.state = Follower
		n.votedFor = ""
	}

	switch msg.Type {
	case MsgVoteRequest:
		return n.handleVoteRequest(msg)
	case MsgHeartbeat:
		return n.handleHeartbeat(msg)
	case MsgDecisionProposal:
		return n.handleDecisionProposal(msg)
	default:
		return nil
	}
}

func (n *Node) handleVoteRequest(msg *Message) *Message {
	resp := &Message{
		Type:   MsgVoteResponse,
		Term:   n.currentTerm,
		FromID: n.config.NodeID,
		ToID:   msg.FromID,
	}

	// Grant vote if:
	// 1. Candidate's term >= our term
	// 2. We haven't voted for anyone else in this term
	// 3. Candidate's log is at least as up-to-date as ours
	if msg.Term >= n.currentTerm &&
		(n.votedFor == "" || n.votedFor == msg.FromID) &&
		msg.LastLogIndex >= len(n.decisions)-1 {
		resp.VoteGranted = true
		n.votedFor = msg.FromID
		n.lastContact = time.Now()
	}

	return resp
}

func (n *Node) handleHeartbeat(msg *Message) *Message {
	if msg.Term >= n.currentTerm {
		n.state = Follower
		n.leaderID = msg.LeaderID
		n.lastContact = time.Now()

		// Append any new decisions from leader
		for _, d := range msg.Decisions {
			found := false
			for _, existing := range n.decisions {
				if existing.ID == d.ID {
					found = true
					break
				}
			}
			if !found {
				n.decisions = append(n.decisions, d)
			}
		}

		// Update commit index
		if msg.LeaderCommit > n.commitIndex {
			n.commitIndex = msg.LeaderCommit
		}
	}

	return &Message{
		Type:   MsgHeartbeatResponse,
		Term:   n.currentTerm,
		FromID: n.config.NodeID,
		ToID:   msg.FromID,
	}
}

func (n *Node) handleDecisionProposal(msg *Message) *Message {
	resp := &Message{
		Type:   MsgDecisionAck,
		Term:   n.currentTerm,
		FromID: n.config.NodeID,
		ToID:   msg.FromID,
	}

	if msg.Term >= n.currentTerm && msg.Decision != nil {
		n.decisions = append(n.decisions, *msg.Decision)
		resp.Success = true
	}

	return resp
}

func (n *Node) peerConnector() {
	defer n.wg.Done()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.mu.Lock()
			for addr, peer := range n.peers {
				if peer.conn == nil || !peer.healthy {
					conn, err := net.DialTimeout("tcp", addr, time.Second)
					if err == nil {
						peer.conn = conn
						peer.healthy = true
						peer.lastSeen = time.Now()
					}
				}
			}
			n.mu.Unlock()
		}
	}
}

func (n *Node) electionLoop() {
	defer n.wg.Done()

	for {
		select {
		case <-n.ctx.Done():
			return
		default:
		}

		n.mu.RLock()
		state := n.state
		lastContact := n.lastContact
		timeout := n.config.ElectionTimeout + time.Duration(mathrand.Int63n(int64(n.config.ElectionTimeout)))
		n.mu.RUnlock()

		if state != Leader && time.Since(lastContact) > timeout {
			n.startElection()
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (n *Node) startElection() {
	n.mu.Lock()
	n.state = Candidate
	n.currentTerm++
	n.votedFor = n.config.NodeID
	term := n.currentTerm
	lastLogIndex := len(n.decisions) - 1
	var lastLogTerm uint64
	if lastLogIndex >= 0 {
		lastLogTerm = n.decisions[lastLogIndex].Term
	}
	n.mu.Unlock()

	votes := 1 // Vote for self
	required := (len(n.peers) / 2) + 1

	for _, peer := range n.peers {
		if peer.conn == nil {
			continue
		}

		msg := Message{
			Type:         MsgVoteRequest,
			Term:         term,
			FromID:       n.config.NodeID,
			LastLogIndex: lastLogIndex,
			LastLogTerm:  lastLogTerm,
		}

		if err := n.sendMessage(peer.conn, &msg); err != nil {
			continue
		}

		peer.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		resp, err := n.recvMessage(peer.conn)
		if err == nil && resp.VoteGranted {
			votes++
		}
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	// Check if we won
	if n.state == Candidate && n.currentTerm == term && votes >= required {
		n.state = Leader
		n.leaderID = n.config.NodeID
		go n.leaderLoop()
	} else {
		n.state = Follower
	}
}

func (n *Node) leaderLoop() {
	ticker := time.NewTicker(n.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.mu.RLock()
			if n.state != Leader {
				n.mu.RUnlock()
				return
			}

			term := n.currentTerm
			decisions := make([]Decision, len(n.decisions))
			copy(decisions, n.decisions)
			commitIndex := n.commitIndex
			n.mu.RUnlock()

			// Send heartbeats to all peers
			for _, peer := range n.peers {
				if peer.conn == nil {
					continue
				}

				msg := Message{
					Type:         MsgHeartbeat,
					Term:         term,
					FromID:       n.config.NodeID,
					LeaderID:     n.config.NodeID,
					LeaderCommit: commitIndex,
					Decisions:    decisions,
				}

				if err := n.sendMessage(peer.conn, &msg); err != nil {
					peer.healthy = false
				} else {
					peer.lastSeen = time.Now()
				}
			}
		}
	}
}

func (n *Node) partitionDetector() {
	defer n.wg.Done()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.mu.Lock()

			// Count healthy peers
			healthyPeers := 0
			for _, peer := range n.peers {
				if peer.healthy && time.Since(peer.lastSeen) < 5*time.Second {
					healthyPeers++
				}
			}

			// Partition if we can't reach majority of peers
			quorum := len(n.peers) / 2
			wasPartitioned := n.partitioned
			n.partitioned = healthyPeers < quorum

			if n.partitioned && !wasPartitioned {
				n.partitionStart = time.Now()
				if n.config.PartitionCallback != nil {
					go n.config.PartitionCallback(true)
				}
			} else if !n.partitioned && wasPartitioned {
				if n.config.PartitionCallback != nil {
					go n.config.PartitionCallback(false)
				}
			}

			n.mu.Unlock()
		}
	}
}

func (n *Node) sendMessage(conn net.Conn, msg *Message) error {
	encoder := json.NewEncoder(conn)
	return encoder.Encode(msg)
}

func (n *Node) recvMessage(conn net.Conn) (*Message, error) {
	decoder := json.NewDecoder(conn)
	var msg Message
	if err := decoder.Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
