package cluster

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/cespare/xxhash/v2"
)

// NodeState represents membership state of a node.
type NodeState int

// Node state enumeration.
const (
	NodeAlive NodeState = iota
	NodeSuspect
	NodeDead
)

// internal constants.
const (
	nodeIDBytes = 8
	byteShift   = 8 // bits per byte for id derivation
)

func (s NodeState) String() string {
	switch s {
	case NodeAlive:
		return "alive"
	case NodeSuspect:
		return "suspect"
	case NodeDead:
		return "dead"
	}

	return "unknown"
}

// NodeID is a stable identifier for a node.
type NodeID string

// Node holds identity & state.
type Node struct {
	ID          NodeID
	Address     string // host:port for intra-cluster RPC
	State       NodeState
	Incarnation uint64
	LastSeen    time.Time
}

// ErrInvalidAddress is returned when the node address is invalid.
var ErrInvalidAddress = errors.New("invalid node address")

// NewNode creates a node from address (host:port). If id empty, derive a short hex id using xxhash64.
func NewNode(id string, addr string) *Node {
	if id == "" {
		hv := xxhash.Sum64String(addr)

		b := make([]byte, nodeIDBytes)
		for i := 0; i < nodeIDBytes; i++ { //nolint:intrange
			b[i] = byte(hv >> (byteShift * i))
		}

		id = hex.EncodeToString(b)
	}

	return &Node{ID: NodeID(id), Address: addr, State: NodeAlive, Incarnation: 1, LastSeen: time.Now()}
}

// Validate basic fields.
func (n *Node) Validate() error {
	if n.Address == "" {
		return ErrInvalidAddress
	}

	_, _, err := net.SplitHostPort(n.Address)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAddress, err)
	}

	return nil
}
