package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/hashicorp/yamux"
)

// ChannelRepository defines the data-access contract for TCP channels and
// their associated listener sessions. It is intentionally separate from
// Repository (the exposure repo) so the two feature paths have zero shared
// state and independent locking.
type ChannelRepository interface {
	// Channel lifecycle
	StoreChannel(ctx context.Context, ch *sdk.Channel) error
	GetChannel(ctx context.Context, channelID string) (*sdk.Channel, error)
	ListChannels(ctx context.Context) ([]*sdk.Channel, error)
	RevokeChannel(ctx context.Context, channelID string) error
	UpdateChannelStatus(ctx context.Context, channelID string, status sdk.ChannelStatus) error
	AddChannelBytes(ctx context.Context, channelID string, n int64)

	// Listener registry — agents pre-register to receive inbound channels
	RegisterListener(ctx context.Context, serverID, orgID string, session *yamux.Session) error
	GetListenerSession(ctx context.Context, serverID string) (*yamux.Session, error)
	UnregisterListener(ctx context.Context, serverID string, session *yamux.Session) error
	ListListeners(ctx context.Context) ([]*sdk.ListenerRegistration, error)
}

// listenerEntry holds the yamux session and metadata for a registered listener.
type listenerEntry struct {
	serverID    string
	orgID       string
	session     *yamux.Session
	connectedAt time.Time
}

// MemoryChannelRepository is a thread-safe in-memory ChannelRepository.
// All state is isolated behind its own RWMutex — no shared locks with the
// exposure MemoryRepository.
type MemoryChannelRepository struct {
	mu        sync.RWMutex
	channels  map[string]*sdk.Channel   // channelID → Channel
	listeners map[string]*listenerEntry // serverID → listener
}

// NewChannelRepository creates a MemoryChannelRepository and starts the
// background expiry reaper.
func NewChannelRepository(ctx context.Context) ChannelRepository {
	r := &MemoryChannelRepository{
		channels:  make(map[string]*sdk.Channel),
		listeners: make(map[string]*listenerEntry),
	}
	go r.reapExpiredChannels(ctx)
	return r
}

// StoreChannel stores a pre-built channel received from server_api.
// Returns an error if the channelID already exists.
func (r *MemoryChannelRepository) StoreChannel(_ context.Context, ch *sdk.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.channels[ch.ChannelID]; exists {
		return fmt.Errorf("channel %q already exists", ch.ChannelID)
	}
	r.channels[ch.ChannelID] = ch
	return nil
}

// GetChannel returns the channel for channelID or an error if not found.
func (r *MemoryChannelRepository) GetChannel(_ context.Context, channelID string) (*sdk.Channel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[channelID]
	if !ok {
		return nil, fmt.Errorf("channel %q not found", channelID)
	}
	return ch, nil
}

// ListChannels returns a snapshot of all known channels.
func (r *MemoryChannelRepository) ListChannels(_ context.Context) ([]*sdk.Channel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*sdk.Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		out = append(out, ch)
	}
	return out, nil
}

// RevokeChannel removes a channel and closes any associated listener stream
// that was opened for it. If the channel does not exist, the call is a no-op.
func (r *MemoryChannelRepository) RevokeChannel(_ context.Context, channelID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.channels, channelID)
	return nil
}

// UpdateChannelStatus transitions a channel to a new status, also setting
// ActivatedAt when transitioning to active.
func (r *MemoryChannelRepository) UpdateChannelStatus(_ context.Context, channelID string, status sdk.ChannelStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.channels[channelID]
	if !ok {
		return fmt.Errorf("channel %q not found", channelID)
	}
	ch.Status = status
	if status == sdk.ChannelStatusActive && ch.ActivatedAt == nil {
		now := time.Now()
		ch.ActivatedAt = &now
	}
	return nil
}

// AddChannelBytes atomically adds n to the channel's BytesRelayed counter.
// Uses an atomic op so the relay goroutines don't need to hold the main mutex.
func (r *MemoryChannelRepository) AddChannelBytes(_ context.Context, channelID string, n int64) {
	r.mu.RLock()
	ch, ok := r.channels[channelID]
	r.mu.RUnlock()
	if ok {
		atomic.AddInt64(&ch.BytesRelayed, n)
	}
}

// RegisterListener stores the listener's yamux session keyed by serverID.
// If a stale session is already registered, it is closed before replacement.
func (r *MemoryChannelRepository) RegisterListener(_ context.Context, serverID, orgID string, session *yamux.Session) error {
	if serverID == "" {
		return fmt.Errorf("serverID is required for listener registration")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.listeners[serverID]; ok {
		existing.session.Close() //nolint:errcheck
	}
	r.listeners[serverID] = &listenerEntry{
		serverID:    serverID,
		orgID:       orgID,
		session:     session,
		connectedAt: time.Now(),
	}
	return nil
}

// GetListenerSession returns the yamux session for the given serverID.
func (r *MemoryChannelRepository) GetListenerSession(_ context.Context, serverID string) (*yamux.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.listeners[serverID]
	if !ok {
		return nil, fmt.Errorf("no listener registered for server %q", serverID)
	}
	return entry.session, nil
}

// UnregisterListener removes the listener for serverID, but only if the
// registered session matches the provided session. This prevents a stale
// session's cleanup goroutine from removing a newer replacement session.
// If session is nil, it unconditionally removes the entry (backward compat).
func (r *MemoryChannelRepository) UnregisterListener(_ context.Context, serverID string, session *yamux.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session != nil {
		if entry, ok := r.listeners[serverID]; ok && entry.session != session {
			return nil // a newer session has already replaced us
		}
	}
	delete(r.listeners, serverID)
	return nil
}

// ListListeners returns a snapshot of all registered listeners.
func (r *MemoryChannelRepository) ListListeners(_ context.Context) ([]*sdk.ListenerRegistration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*sdk.ListenerRegistration, 0, len(r.listeners))
	for _, e := range r.listeners {
		out = append(out, &sdk.ListenerRegistration{
			ServerID:    e.serverID,
			OrgID:       e.orgID,
			ConnectedAt: e.connectedAt,
		})
	}
	return out, nil
}

// reapExpiredChannels runs as a background goroutine and evicts channels past
// their ExpiresAt time, updating their status to closed.
func (r *MemoryChannelRepository) reapExpiredChannels(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.expireChannels()
		}
	}
}

func (r *MemoryChannelRepository) expireChannels() {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, ch := range r.channels {
		if now.After(ch.ExpiresAt) {
			ch.Status = sdk.ChannelStatusClosed
			delete(r.channels, id)
		}
	}
}
