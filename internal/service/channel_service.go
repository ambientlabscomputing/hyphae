package service

import (
	"context"
	"fmt"

	"github.com/ambientlabscomputing/hyphae/sdk"
	jwt "github.com/golang-jwt/jwt/v4"
	"github.com/hashicorp/yamux"
)

// channelGrantClaims are the JWT claims for channel grant tokens.
// These are signed by server_api's ES256 key and only verified here.
type channelGrantClaims struct {
	ChannelID      string `json:"channel_id"`
	OrgID          string `json:"org_id"`
	SourceServerID string `json:"source_server_id"`
	DestServerID   string `json:"dest_server_id"`
	Purpose        string `json:"purpose"`
	Nonce          string `json:"nonce"`
	jwt.RegisteredClaims
}

// errChannelsDisabled is returned when a channel operation is attempted but
// the feature is not enabled in config.
var errChannelsDisabled = fmt.Errorf("channels: feature is not enabled on this gateway")

// ── RegisterChannel ───────────────────────────────────────────────────────────

// RegisterChannel stores a pre-built channel received from server_api.
// server_api is the sole authority for channel creation and grant signing —
// hyphae only stores the channel record for relay lookup.
func (s *AppService) RegisterChannel(ctx context.Context, ch *sdk.Channel) error {
	if !s.cfg.ChannelsEnabled {
		return errChannelsDisabled
	}
	if ch.ChannelID == "" {
		return fmt.Errorf("channels: channel_id is required")
	}
	if ch.OrgID == "" {
		return fmt.Errorf("channels: org_id is required")
	}
	if ch.SourceServerID == "" {
		return fmt.Errorf("channels: source_server_id is required")
	}
	if ch.DestServerID == "" {
		return fmt.Errorf("channels: dest_server_id is required")
	}
	if ch.ExpiresAt.IsZero() {
		return fmt.Errorf("channels: expires_at is required")
	}
	return s.channelRepo.StoreChannel(ctx, ch)
}

// ── ValidateChannelGrant ──────────────────────────────────────────────────────

// ValidateChannelGrant parses and cryptographically verifies a channel grant
// JWT using server_api's ES256 public key. Returns the extracted claims or an
// error if the token is invalid, expired, or signed with an unexpected key.
func (s *AppService) ValidateChannelGrant(_ context.Context, grantToken string) (*sdk.ChannelGrant, error) {
	if !s.cfg.ChannelsEnabled {
		return nil, errChannelsDisabled
	}
	if grantToken == "" {
		return nil, fmt.Errorf("channels: grant token is required")
	}

	var claims channelGrantClaims
	_, err := jwt.ParseWithClaims(grantToken, &claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("channels: unexpected signing method %q", t.Header["alg"])
		}
		return s.cfg.ChannelGrantVerifyKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("channels: invalid grant: %w", err)
	}

	return &sdk.ChannelGrant{
		ChannelID:      claims.ChannelID,
		OrgID:          claims.OrgID,
		SourceServerID: claims.SourceServerID,
		DestServerID:   claims.DestServerID,
		Purpose:        claims.Purpose,
		Nonce:          claims.Nonce,
	}, nil
}

// ── Channel CRUD pass-throughs ────────────────────────────────────────────────

func (s *AppService) GetChannel(ctx context.Context, channelID string) (*sdk.Channel, error) {
	if !s.cfg.ChannelsEnabled {
		return nil, errChannelsDisabled
	}
	return s.channelRepo.GetChannel(ctx, channelID)
}

func (s *AppService) ListChannels(ctx context.Context) ([]*sdk.Channel, error) {
	if !s.cfg.ChannelsEnabled {
		return nil, errChannelsDisabled
	}
	return s.channelRepo.ListChannels(ctx)
}

func (s *AppService) RevokeChannel(ctx context.Context, channelID string) error {
	if !s.cfg.ChannelsEnabled {
		return errChannelsDisabled
	}
	return s.channelRepo.RevokeChannel(ctx, channelID)
}

func (s *AppService) UpdateChannelStatus(ctx context.Context, channelID string, status sdk.ChannelStatus) error {
	if !s.cfg.ChannelsEnabled {
		return errChannelsDisabled
	}
	return s.channelRepo.UpdateChannelStatus(ctx, channelID, status)
}

func (s *AppService) AddChannelBytes(ctx context.Context, channelID string, n int64) {
	if s.cfg.ChannelsEnabled {
		s.channelRepo.AddChannelBytes(ctx, channelID, n)
	}
}

// ── Listener operations ───────────────────────────────────────────────────────

func (s *AppService) RegisterListener(ctx context.Context, serverID, orgID string, session *yamux.Session) error {
	if !s.cfg.ChannelsEnabled {
		return errChannelsDisabled
	}
	return s.channelRepo.RegisterListener(ctx, serverID, orgID, session)
}

func (s *AppService) GetListenerSession(ctx context.Context, serverID string) (*yamux.Session, error) {
	if !s.cfg.ChannelsEnabled {
		return nil, errChannelsDisabled
	}
	return s.channelRepo.GetListenerSession(ctx, serverID)
}

func (s *AppService) UnregisterListener(ctx context.Context, serverID string, session *yamux.Session) error {
	if !s.cfg.ChannelsEnabled {
		return errChannelsDisabled
	}
	return s.channelRepo.UnregisterListener(ctx, serverID, session)
}

func (s *AppService) ListListeners(ctx context.Context) ([]*sdk.ListenerRegistration, error) {
	if !s.cfg.ChannelsEnabled {
		return nil, errChannelsDisabled
	}
	return s.channelRepo.ListListeners(ctx)
}
