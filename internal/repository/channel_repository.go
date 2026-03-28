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

	// Listener registry — agents pre-register to receive inbound channels.
	// Each listener session is keyed by (serverID, channelID) so that multiple
	// channels for the same destination agent get independent yamux sessions.
	RegisterListener(ctx context.Context, serverID, orgID, channelID string, session *yamux.Session) error
	GetListenerSession(ctx context.Context, serverID, channelID string) (*yamux.Session, error)
	UnregisterListener(ctx context.Context, serverID, channelID string, session *yamux.Session) error
	ListListeners(ctx context.Context) ([]*sdk.ListenerRegistration, error)
}

// listenerEntry holds the yamux session and metadata for a registered listener.
type listenerEntry struct {
	serverID    string
	orgID       string
	channelID   string
	session     *yamux.Session
	connectedAt time.Time
}

// errorGracePeriod is how long a channel may stay in error state before the
// watchdog evicts it. 60 seconds is enough for any in-flight retry to resolve
// itself while still keeping the connection pool clean.
const errorGracePeriod = 60 * time.Second

// MemoryChannelRepository is a thread-safe in-memory ChannelRepository.
// All state is isolated behind its own RWMutex — no shared locks with the
// exposure MemoryRepository.
type MemoryChannelRepository struct {
	mu             sync.RWMutex
	channels       map[string]*sdk.Channel   // channelID → Channel
	listeners      map[string]*listenerEntry // "serverID:channelID" → listener
	errorAt        map[string]time.Time      // channelID → time channel entered error state
	reaperInterval time.Duration
}

// defaultReapInterval is used when no interval is provided to NewChannelRepository.
const defaultReapInterval = 5 * time.Second

// NewChannelRepository creates a MemoryChannelRepository and starts the
// background expiry reaper. reaperInterval controls how often the reaper
// scans for stale channels; pass 0 to use the default (5 s).
func NewChannelRepository(ctx context.Context, reaperInterval time.Duration) ChannelRepository {
	if reaperInterval <= 0 {
		reaperInterval = defaultReapInterval
	}
	r := &MemoryChannelRepository{
		channels:       make(map[string]*sdk.Channel),
		listeners:      make(map[string]*listenerEntry),
		errorAt:        make(map[string]time.Time),
		reaperInterval: reaperInterval,
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

// RevokeChannel removes a channel and closes any associated listener yamux
// sessions that were opened for it. If the channel does not exist, the call
// is a no-op. Closing the sessions unblocks the drain loop in
// handleListenerConn, which will then unregister the listener.
func (r *MemoryChannelRepository) RevokeChannel(_ context.Context, channelID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.channels, channelID)
	delete(r.errorAt, channelID)

	// Close and remove listener sessions associated with this channel.
	suffix := ":" + channelID
	for key, entry := range r.listeners {
		if len(key) > len(suffix) && key[len(key)-len(suffix):] == suffix {
			entry.session.Close()
			delete(r.listeners, key)
		}
	}
	return nil
}

// UpdateChannelStatus transitions a channel to a new status, also setting
// ActivatedAt when transitioning to active. Tracks the time a channel enters
// error state so the watchdog can evict it after the grace period.
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
	if status == sdk.ChannelStatusError {
		if _, alreadyTracked := r.errorAt[channelID]; !alreadyTracked {
			r.errorAt[channelID] = time.Now()
		}
	} else {
		delete(r.errorAt, channelID)
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

// RegisterListener stores the listener's yamux session keyed by (serverID, channelID).
// If a stale session is already registered for the same key, it is closed before replacement.
func (r *MemoryChannelRepository) RegisterListener(_ context.Context, serverID, orgID, channelID string, session *yamux.Session) error {
	if serverID == "" {
		return fmt.Errorf("serverID is required for listener registration")
	}
	key := serverID + ":" + channelID
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.listeners[key]; ok {
		existing.session.Close() //nolint:errcheck
	}
	r.listeners[key] = &listenerEntry{
		serverID:    serverID,
		orgID:       orgID,
		channelID:   channelID,
		session:     session,
		connectedAt: time.Now(),
	}
	return nil
}

// GetListenerSession returns the yamux session for the given (serverID, channelID).
func (r *MemoryChannelRepository) GetListenerSession(_ context.Context, serverID, channelID string) (*yamux.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := serverID + ":" + channelID
	entry, ok := r.listeners[key]
	if !ok {
		return nil, fmt.Errorf("no listener registered for server %q channel %q", serverID, channelID)
	}
	return entry.session, nil
}

// UnregisterListener removes the listener for (serverID, channelID), but only if the
// registered session matches the provided session. This prevents a stale
// session's cleanup goroutine from removing a newer replacement session.
// If session is nil, it unconditionally removes the entry (backward compat).
func (r *MemoryChannelRepository) UnregisterListener(_ context.Context, serverID, channelID string, session *yamux.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := serverID + ":" + channelID
	if session != nil {
		if entry, ok := r.listeners[key]; ok && entry.session != session {
			return nil // a newer session has already replaced us
		}
	}
	delete(r.listeners, key)
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

// reapExpiredChannels runs as a background goroutine on a configurable tick.
// It evicts TTL-expired channels, evicts channels that have been stuck in
// error state past the grace period, and prunes dead listener sessions.
func (r *MemoryChannelRepository) reapExpiredChannels(ctx context.Context) {
	ticker := time.NewTicker(r.reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.expireChannels()
			r.pruneDeadListeners()
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
			delete(r.errorAt, id)
			continue
		}
		// Evict channels that have been stuck in error state past the grace period.
		// These accumulate during retry storms and exhaust the per-node connection
		// limit, blocking all subsequent channel establishment.
		if errTime, inError := r.errorAt[id]; inError && now.Sub(errTime) > errorGracePeriod {
			delete(r.channels, id)
			delete(r.errorAt, id)
		}
	}
}

// pruneDeadListeners removes listener sessions whose yamux connection has
// already closed. This prevents orphaned entries from blocking re-registration
// and avoids routing new initiators to a dead session.
func (r *MemoryChannelRepository) pruneDeadListeners() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, entry := range r.listeners {
		if entry.session.IsClosed() {
			delete(r.listeners, key)
		}
	}
}
