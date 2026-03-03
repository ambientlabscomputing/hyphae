// Package client provides a gRPC admin client for hyphctl.
package client

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ambientlabscomputing/hyphae/internal/proto/admin"
)

type adminClientKey struct{}

func WithClient(ctx context.Context, c *AdminClient) context.Context {
	return context.WithValue(ctx, adminClientKey{}, c)
}

func GetClient(ctx context.Context) *AdminClient {
	c, _ := ctx.Value(adminClientKey{}).(*AdminClient)
	return c
}

type AdminClient struct {
	conn        *grpc.ClientConn
	health      admin.AdminHealthServiceClient
	leases      admin.AdminLeasesServiceClient
	connections admin.AdminConnectionsServiceClient
}

func NewAdminClient(socketPath string, _ time.Duration) (*AdminClient, error) {
	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("admin client %s: %w", socketPath, err)
	}
	return &AdminClient{conn: conn}, nil
}

func (c *AdminClient) Close() error { return c.conn.Close() }

func (c *AdminClient) Health() admin.AdminHealthServiceClient {
	if c.health == nil {
		c.health = admin.NewAdminHealthServiceClient(c.conn)
	}
	return c.health
}

func (c *AdminClient) Leases() admin.AdminLeasesServiceClient {
	if c.leases == nil {
		c.leases = admin.NewAdminLeasesServiceClient(c.conn)
	}
	return c.leases
}

func (c *AdminClient) Connections() admin.AdminConnectionsServiceClient {
	if c.connections == nil {
		c.connections = admin.NewAdminConnectionsServiceClient(c.conn)
	}
	return c.connections
}
