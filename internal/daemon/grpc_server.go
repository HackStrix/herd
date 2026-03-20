package daemon

import (
	"context"
	"io"
	"log"

	pb "github.com/hackstrix/herd/proto/herd/v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements the HerdService gRPC server.
type Server struct {
	pb.UnimplementedHerdServiceServer
	// In Phase 3, we will add a reference to the core Pool structure here.
}

func NewServer() *Server {
	return &Server{}
}

// Acquire handles bidirectional streaming allocation.
func (s *Server) Acquire(stream pb.HerdService_AcquireServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil // Client closed gracefully
		}
		if err != nil {
			return err // Stream broken
		}

		log.Printf("Received Acquire Request for worker_type: %s", req.GetWorkerType())

		// TODO: In Phase 3, map this to core.Pool.Acquire()
		resp := &pb.AcquireResponse{
			SessionId:    "stub-session-123",
			ProxyAddress: "http://127.0.0.1:8080",
			WorkerPid:    9999,
		}

		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// Status returns daemon health metrics.
func (s *Server) Status(ctx context.Context, _ *emptypb.Empty) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{
		ActiveWorkers: 0,
		IdleWorkers:   2,
		MaxWorkers:    10,
	}, nil
}
