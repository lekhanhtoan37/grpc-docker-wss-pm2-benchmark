package coordinator

import (
	"context"
	"log"
	"sync/atomic"

	pb "benchmark-client/proto/control"

	"google.golang.org/grpc"
)

type Server struct {
	pb.UnimplementedCoordinatorServiceServer

	phase        *PhaseManager
	aggregator   *Aggregator
	shards       [][]pb.GroupAssignment
	workerCount  atomic.Int32
	totalWorkers int
}

func NewServer(phase *PhaseManager, aggregator *Aggregator, shards [][]pb.GroupAssignment, totalWorkers int) *Server {
	return &Server{
		phase:        phase,
		aggregator:   aggregator,
		shards:       shards,
		totalWorkers: totalWorkers,
	}
}

func (s *Server) RegisterWorker(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	idx := int(s.workerCount.Add(1) - 1)
	log.Printf("[coordinator] Worker %s (%s) registered, assigned shard %d", req.WorkerId, req.Hostname, idx)

	var groups []*pb.GroupAssignment
	if idx < len(s.shards) {
		for i := range s.shards[idx] {
			groups = append(groups, &s.shards[idx][i])
		}
	}

	warmupSec := s.phase.GetWarmupSeconds()
	measureSec := s.phase.GetMeasureDuration()
	measureStartMs := s.phase.GetMeasureStartMs()

	resp := &pb.RegisterResponse{
		Groups:             groups,
		WarmupSeconds:      warmupSec,
		MeasureSeconds:     measureSec,
		MeasureStartUnixMs: measureStartMs,
	}

	s.phase.RegisterWorker()

	return resp, nil
}

func (s *Server) ReportSnapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	return &pb.SnapshotResponse{Ok: true}, nil
}

func (s *Server) ReportFinal(ctx context.Context, req *pb.FinalReport) (*pb.FinalResponse, error) {
	log.Printf("[coordinator] Received final report from worker %s (%d groups)", req.WorkerId, len(req.Groups))

	s.aggregator.AddWorkerResult(req.WorkerId, req)
	s.phase.RecordFinalResult()

	return &pb.FinalResponse{Ok: true}, nil
}

func RegisterServer(s *grpc.Server, srv *Server) {
	pb.RegisterCoordinatorServiceServer(s, srv)
}
