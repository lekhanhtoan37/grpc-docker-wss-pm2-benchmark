package coordinator

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	pb "benchmark-client/proto/control"

	"benchmark-client/internal/stats"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startTestServer(t *testing.T, groups []stats.Group, numWorkers, warmup, measure, conns int) (string, *PhaseManager, *Aggregator, *grpc.Server) {
	t.Helper()

	sharded := ShardGroups(groups, numWorkers)

	pbShards := make([][]pb.GroupAssignment, len(sharded))
	for i, shard := range sharded {
		for _, g := range shard {
			pbShards[i] = append(pbShards[i], pb.GroupAssignment{
				Name:        g.Name,
				Type:        g.Type,
				Endpoints:   g.Endpoints,
				Connections: int32(conns),
			})
		}
	}

	phase := NewPhaseManager(numWorkers, warmup, measure)
	phase.ComputeBarrierTime()
	agg := NewAggregator()
	srv := NewServer(phase, agg, pbShards, numWorkers)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	RegisterServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)

	return lis.Addr().String(), phase, agg, grpcSrv
}

func TestIntegration_RegisterAndGetGroups(t *testing.T) {
	groups := []stats.Group{
		{Name: "gA", Type: "ws", Endpoints: []string{"ws://localhost:1111"}},
		{Name: "gB", Type: "ws", Endpoints: []string{"ws://localhost:2222"}},
	}

	addr, _, _, grpcSrv := startTestServer(t, groups, 2, 5, 10, 1)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewCoordinatorServiceClient(conn)

	resp, err := client.RegisterWorker(context.Background(), &pb.RegisterRequest{
		WorkerId: "test-w1",
		Hostname: "testhost",
	})
	if err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	if len(resp.Groups) == 0 {
		t.Fatal("worker should receive group assignments")
	}

	t.Logf("Worker 1 got %d groups:", len(resp.Groups))
	for _, g := range resp.Groups {
		t.Logf("  - %s (%s) conns=%d endpoints=%v", g.Name, g.Type, g.Connections, g.Endpoints)
	}

	found := false
	for _, g := range resp.Groups {
		if g.Name == "gA" {
			found = true
			if g.Connections != 1 {
				t.Errorf("connections: got %d, want 1", g.Connections)
			}
		}
	}
	if !found {
		t.Error("expected gA in worker 1 assignments")
	}

	if resp.MeasureStartUnixMs == 0 {
		t.Error("MeasureStartUnixMs should be non-zero (barrier time)")
	}
	if resp.WarmupSeconds != 5 {
		t.Errorf("warmup: got %d, want 5", resp.WarmupSeconds)
	}
	if resp.MeasureSeconds != 10 {
		t.Errorf("measure: got %d, want 10", resp.MeasureSeconds)
	}
}

func TestIntegration_TwoWorkers_ShardCorrectly(t *testing.T) {
	groups := []stats.Group{
		{Name: "g1", Type: "ws", Endpoints: []string{"ws://localhost:1"}},
		{Name: "g2", Type: "ws", Endpoints: []string{"ws://localhost:2"}},
		{Name: "g3", Type: "grpc", Endpoints: []string{"localhost:3"}},
		{Name: "g4", Type: "ws", Endpoints: []string{"ws://localhost:4"}},
	}

	addr, _, _, grpcSrv := startTestServer(t, groups, 2, 5, 10, 1)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := pb.NewCoordinatorServiceClient(conn)

	resp1, err := client.RegisterWorker(context.Background(), &pb.RegisterRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("w1 register: %v", err)
	}

	resp2, err := client.RegisterWorker(context.Background(), &pb.RegisterRequest{WorkerId: "w2"})
	if err != nil {
		t.Fatalf("w2 register: %v", err)
	}

	names1 := make(map[string]bool)
	for _, g := range resp1.Groups {
		names1[g.Name] = true
	}
	names2 := make(map[string]bool)
	for _, g := range resp2.Groups {
		names2[g.Name] = true
	}

	allNames := make(map[string]bool)
	for k := range names1 {
		allNames[k] = true
	}
	for k := range names2 {
		allNames[k] = true
	}

	if len(allNames) != 4 {
		t.Errorf("expected 4 unique groups across workers, got %d: %v", len(allNames), allNames)
	}

	for k := range names1 {
		if names2[k] {
			t.Errorf("group %s assigned to BOTH workers (should be exclusive)", k)
		}
	}

	t.Logf("w1 groups: %v", names1)
	t.Logf("w2 groups: %v", names2)
}

func TestIntegration_ReportFinalAndAggregate(t *testing.T) {
	groups := []stats.Group{
		{Name: "gA", Type: "ws", Endpoints: []string{"ws://localhost:1"}},
	}

	addr, phase, agg, grpcSrv := startTestServer(t, groups, 2, 5, 10, 1)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := pb.NewCoordinatorServiceClient(conn)

	_, err = client.RegisterWorker(context.Background(), &pb.RegisterRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("w1 register: %v", err)
	}
	_, err = client.RegisterWorker(context.Background(), &pb.RegisterRequest{WorkerId: "w2"})
	if err != nil {
		t.Fatalf("w2 register: %v", err)
	}

	w1Report := makeFakeReport("w1", "gA", 500, 25000, []int64{100, 200, 300})
	w2Report := makeFakeReport("w2", "gA", 700, 35000, []int64{150, 250, 350})

	_, err = client.ReportFinal(context.Background(), w1Report)
	if err != nil {
		t.Fatalf("w1 ReportFinal: %v", err)
	}

	_, err = client.ReportFinal(context.Background(), w2Report)
	if err != nil {
		t.Fatalf("w2 ReportFinal: %v", err)
	}

	if agg.ResultCount() != 2 {
		t.Errorf("result count: got %d, want 2", agg.ResultCount())
	}

	gs := agg.MergeToGroupStats()
	ga, ok := gs["gA"]
	if !ok {
		t.Fatal("group gA not found in merged stats")
	}

	totalMsgs, totalBytes, _, _ := stats.AggregateGroup(ga)
	if totalMsgs != 1200 {
		t.Errorf("totalMsgs: got %d, want 1200 (500+700)", totalMsgs)
	}
	if totalBytes != 60000 {
		t.Errorf("totalBytes: got %d, want 60000 (25000+35000)", totalBytes)
	}

	mergedHists := agg.MergeToGroupHistograms()
	h := mergedHists["gA"]
	if h.TotalCount() != 6 {
		t.Errorf("histogram samples: got %d, want 6", h.TotalCount())
	}

	t.Logf("Merged: msgs=%d bytes=%d hist_samples=%d", totalMsgs, totalBytes, h.TotalCount())
	t.Logf("  p50=%.1f p90=%.1f p99=%.1f",
		float64(h.ValueAtPercentile(50))/1000,
		float64(h.ValueAtPercentile(90))/1000,
		float64(h.ValueAtPercentile(99))/1000)

	_ = phase
}

func TestIntegration_FullPhaseLifecycle(t *testing.T) {
	groups := []stats.Group{
		{Name: "g1", Type: "ws", Endpoints: []string{"ws://localhost:1"}},
		{Name: "g2", Type: "ws", Endpoints: []string{"ws://localhost:2"}},
	}

	addr, phase, agg, grpcSrv := startTestServer(t, groups, 2, 1, 2, 1)
	defer grpcSrv.GracefulStop()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- phase.WaitForWorkers(context.Background(), 10*time.Second)
	}()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := pb.NewCoordinatorServiceClient(conn)

	resp1, err := client.RegisterWorker(context.Background(), &pb.RegisterRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("w1 register: %v", err)
	}
	resp2, err := client.RegisterWorker(context.Background(), &pb.RegisterRequest{WorkerId: "w2"})
	if err != nil {
		t.Fatalf("w2 register: %v", err)
	}

	if err := <-waitDone; err != nil {
		t.Fatalf("WaitForWorkers: %v", err)
	}

	barrier := phase.GetMeasureStartMs()
	now := time.Now().UnixMilli()
	if barrier <= now {
		t.Errorf("barrier %d should be in the future (now=%d)", barrier, now)
	}

	t.Logf("Barrier time: %d (now=%d, delta=%dms)", barrier, now, barrier-now)

	for _, resp := range []*pb.RegisterResponse{resp1, resp2} {
		if resp.MeasureStartUnixMs != barrier {
			t.Errorf("worker barrier mismatch: got %d, want %d", resp.MeasureStartUnixMs, barrier)
		}
	}

	resultsDone := make(chan error, 1)
	go func() {
		resultsDone <- phase.WaitForResults(context.Background(), 10*time.Second)
	}()

	w1Report := makeFakeReport("w1", resp1.Groups[0].Name, 100, 5000, []int64{100})
	w2Report := makeFakeReport("w2", resp2.Groups[0].Name, 200, 10000, []int64{200})

	_, err = client.ReportFinal(context.Background(), w1Report)
	if err != nil {
		t.Fatalf("w1 final: %v", err)
	}
	_, err = client.ReportFinal(context.Background(), w2Report)
	if err != nil {
		t.Fatalf("w2 final: %v", err)
	}

	if err := <-resultsDone; err != nil {
		t.Fatalf("WaitForResults: %v", err)
	}

	if agg.ResultCount() != 2 {
		t.Errorf("expected 2 results, got %d", agg.ResultCount())
	}

	totalGroups := 0
	for _, wr := range []string{"w1", "w2"} {
		if _, ok := agg.MergeToGroupStats()[resp1.Groups[0].Name]; !ok {
			if _, ok2 := agg.MergeToGroupStats()[resp2.Groups[0].Name]; !ok2 {
				t.Errorf("no groups found for worker %s", wr)
			}
		}
	}
	totalGroups = len(agg.MergeToGroupStats())

	if totalGroups != 2 {
		t.Errorf("expected 2 groups in merged stats, got %d", totalGroups)
	}

	t.Logf("Full lifecycle OK: 2 workers registered, 2 results received, %d groups merged", totalGroups)
}

func TestIntegration_ShardDistribution(t *testing.T) {
	numWorkers := 3
	groups := []stats.Group{
		{Name: "A"}, {Name: "B"}, {Name: "C"},
		{Name: "D"}, {Name: "E"}, {Name: "F"},
		{Name: "G"}, {Name: "H"}, {Name: "I"},
	}

	addr, _, _, grpcSrv := startTestServer(t, groups, numWorkers, 5, 10, 1)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := pb.NewCoordinatorServiceClient(conn)

	allAssigned := make(map[string]string)
	for i := 0; i < numWorkers; i++ {
		resp, err := client.RegisterWorker(context.Background(), &pb.RegisterRequest{
			WorkerId: fmt.Sprintf("w%d", i),
		})
		if err != nil {
			t.Fatalf("w%d register: %v", i, err)
		}

		for _, g := range resp.Groups {
			if prev, exists := allAssigned[g.Name]; exists {
				t.Errorf("group %s assigned to both w%d and %s", g.Name, i, prev)
			}
			allAssigned[g.Name] = fmt.Sprintf("w%d", i)
		}
		t.Logf("w%d got: %v", i, func() (names []string) {
			for _, g := range resp.Groups {
				names = append(names, g.Name)
			}
			return
		}())
	}

	if len(allAssigned) != 9 {
		t.Errorf("expected 9 groups assigned, got %d: %v", len(allAssigned), allAssigned)
	}
}
