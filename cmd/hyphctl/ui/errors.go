// Package ui provides display utilities for the hyphctl CLI.
package ui

import (
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HandleGRPCError converts a gRPC error into a user-friendly string.
// Returns an empty string for nil.
func HandleGRPCError(err error) string {
	if err == nil {
		return ""
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Sprintf("Error: %v", err)
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return fmt.Sprintf("Invalid input: %s", st.Message())
	case codes.NotFound:
		return fmt.Sprintf("Not found: %s", st.Message())
	case codes.AlreadyExists:
		return fmt.Sprintf("Already exists: %s", st.Message())
	case codes.PermissionDenied:
		return fmt.Sprintf("Permission denied: %s", st.Message())
	case codes.Unavailable:
		return "Server unavailable — is hyphae running?"
	case codes.DeadlineExceeded:
		return "Request timed out"
	default:
		msg := st.Message()
		if msg == "" {
			msg = st.Code().String()
		}
		return strings.TrimPrefix(msg, "rpc error: code = Internal desc = ")
	}
}
