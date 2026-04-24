// Package client provides a gRPC admin client for hyphctl.
package client

import (
	"google.golang.org/grpc"

	"github.com/ambientlabscomputing/hyphae/internal/proto/admin"
)

// AdminClient wraps a gRPC connection and provides lazy service client accessors.
type AdminClient struct {
	conn        *grpc.ClientConn
	health      admin.AdminHealthServiceClient
	leases      admin.AdminLeasesServiceClient
	connections admin.AdminConnectionsServiceClient
}

// NewAdminClientFromConn creates an AdminClient from an existing gRPC connection.
func NewAdminClientFromConn(conn *grpc.ClientConn) *AdminClient {
	return &AdminClient{conn: conn}
}

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
