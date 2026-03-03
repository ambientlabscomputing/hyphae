// Package admin_server implements the gRPC admin socket server for hyphae.
package admin_server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ambientlabscomputing/hyphae/internal/proto/admin"
	"github.com/ambientlabscomputing/hyphae/internal/service"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
	sdktypes "github.com/ambientlabscomputing/hyphae/sdk"
)

type AdminServer struct {
	svc        service.Service
	grpcServer *grpc.Server
	listener   net.Listener
	socketPath string
	logger     *slog.Logger
	startedAt  time.Time
	version    string
}

func NewAdminServer(svc service.Service, socketPath string, version string) *AdminServer {
	return &AdminServer{svc: svc, socketPath: socketPath, version: version}
}

func (as *AdminServer) Start(ctx context.Context) error {
	as.logger = utils.GetLogger(ctx)
	as.startedAt = time.Now()

	dir := filepath.Dir(as.socketPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("admin_server: create socket dir: %w", err)
		}
	}
	if err := os.RemoveAll(as.socketPath); err != nil {
		return fmt.Errorf("admin_server: remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", as.socketPath)
	if err != nil {
		return fmt.Errorf("admin_server: listen %s: %w", as.socketPath, err)
	}
	if err := os.Chmod(as.socketPath, 0600); err != nil {
		ln.Close()
		return fmt.Errorf("admin_server: chmod socket: %w", err)
	}
	as.listener = ln

	as.grpcServer = grpc.NewServer(
		grpc.UnaryInterceptor(unaryInterceptor(ctx, as.logger)),
	)
	as.registerServices()

	go func() {
		as.logger.Info("Admin gRPC server listening", "socket", as.socketPath)
		if err := as.grpcServer.Serve(ln); err != nil {
			as.logger.Error("Admin gRPC server error", "error", err)
		}
	}()
	return nil
}

func (as *AdminServer) Stop(ctx context.Context) error {
	if as.grpcServer == nil {
		return nil
	}
	as.logger.Info("Stopping admin socket server")
	done := make(chan struct{})
	go func() {
		as.grpcServer.GracefulStop()
		close(done)
	}()
	if as.listener != nil {
		as.listener.Close()
	}
	_ = os.RemoveAll(as.socketPath)
	select {
	case <-ctx.Done():
		as.grpcServer.Stop()
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (as *AdminServer) registerServices() {
	admin.RegisterAdminHealthServiceServer(as.grpcServer, &healthServiceImpl{as: as})
	admin.RegisterAdminLeasesServiceServer(as.grpcServer, &leasesServiceImpl{as: as})
	admin.RegisterAdminConnectionsServiceServer(as.grpcServer, &connectionsServiceImpl{as: as})
}

type healthServiceImpl struct {
	admin.UnimplementedAdminHealthServiceServer
	as *AdminServer
}

func (h *healthServiceImpl) Check(ctx context.Context, _ *admin.Empty) (*admin.HealthCheckResponse, error) {
	leases, err := h.as.svc.ListLeases(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	conns, err := h.as.svc.ListConnections(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &admin.HealthCheckResponse{
		Status:          "ok",
		LeaseCount:      int32(len(leases)),
		ConnectionCount: int32(len(conns)),
	}, nil
}

func (h *healthServiceImpl) Detail(ctx context.Context, _ *admin.Empty) (*admin.HealthDetailResponse, error) {
	leases, err := h.as.svc.ListLeases(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	conns, err := h.as.svc.ListConnections(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	uptime := time.Since(h.as.startedAt).Round(time.Second).String()
	return &admin.HealthDetailResponse{
		Status:          "ok",
		LeaseCount:      int32(len(leases)),
		ConnectionCount: int32(len(conns)),
		Uptime:          uptime,
		Version:         h.as.version,
	}, nil
}

type leasesServiceImpl struct {
	admin.UnimplementedAdminLeasesServiceServer
	as *AdminServer
}

func (l *leasesServiceImpl) List(ctx context.Context, _ *admin.Empty) (*admin.ListLeasesResponse, error) {
	leases, err := l.as.svc.ListLeases(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	out := make([]*admin.LeaseProto, 0, len(leases))
	for _, lease := range leases {
		out = append(out, leaseToProto(lease))
	}
	return &admin.ListLeasesResponse{Leases: out}, nil
}

func (l *leasesServiceImpl) Get(ctx context.Context, req *admin.GetLeaseRequest) (*admin.LeaseProto, error) {
	lease, err := l.as.svc.GetLease(ctx, req.LeaseId)
	if err != nil {
		return nil, toGRPCError(err)
	}
	return leaseToProto(lease), nil
}

func (l *leasesServiceImpl) Revoke(ctx context.Context, req *admin.RevokeLeaseRequest) (*admin.Empty, error) {
	if err := l.as.svc.RevokeLease(ctx, req.LeaseId); err != nil {
		return nil, toGRPCError(err)
	}
	return &admin.Empty{}, nil
}

type connectionsServiceImpl struct {
	admin.UnimplementedAdminConnectionsServiceServer
	as *AdminServer
}

func (c *connectionsServiceImpl) List(ctx context.Context, _ *admin.Empty) (*admin.ListConnectionsResponse, error) {
	conns, err := c.as.svc.ListConnections(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	out := make([]*admin.ConnectionProto, 0, len(conns))
	for _, conn := range conns {
		out = append(out, connToProto(conn))
	}
	return &admin.ListConnectionsResponse{Connections: out}, nil
}

func leaseToProto(l *sdktypes.Lease) *admin.LeaseProto {
	p := &admin.LeaseProto{
		LeaseId:   l.LeaseID,
		OrgId:     l.OrgID,
		Hostname:  l.Hostname,
		ServerId:  l.ServerID,
		Status:    string(l.Status),
		CreatedAt: l.CreatedAt.Format(time.RFC3339),
	}
	if l.BoundAt != nil {
		p.BoundAt = l.BoundAt.Format(time.RFC3339)
	}
	return p
}

func connToProto(c *sdktypes.TunnelConnection) *admin.ConnectionProto {
	return &admin.ConnectionProto{
		ConnId:      c.ConnectionID,
		LeaseId:     c.LeaseID,
		ServerId:    c.ServerID,
		RemoteAddr:  c.RemoteAddr,
		ConnectedAt: c.ConnectedAt.Format(time.RFC3339),
	}
}

func unaryInterceptor(ctx context.Context, logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(rpcCtx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		_ = ctx
		logger.Debug("Admin RPC call", "method", info.FullMethod)
		resp, err := handler(rpcCtx, req)
		if err != nil {
			st, _ := status.FromError(err)
			logger.Error("Admin RPC error", "method", info.FullMethod, "code", st.Code(), "message", st.Message())
		}
		return resp, err
	}
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}
	switch err.Error() {
	case "not found":
		return status.Error(codes.NotFound, err.Error())
	case "already exists":
		return status.Error(codes.AlreadyExists, err.Error())
	case "invalid argument":
		return status.Error(codes.InvalidArgument, err.Error())
	case "unauthorized":
		return status.Error(codes.PermissionDenied, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
