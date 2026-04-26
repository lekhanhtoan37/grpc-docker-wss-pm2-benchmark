package stats

import (
	pb "benchmark-client/proto/control"
)

func ConnStatsToProto(cs *ConnStats, connIndex int, endpoint string) (*pb.ConnResult, error) {
	latBytes, err := EncodeHistogram(cs.Hist)
	if err != nil {
		return nil, err
	}
	reconnBytes, err := EncodeHistogram(cs.ReconnectHist)
	if err != nil {
		return nil, err
	}

	return &pb.ConnResult{
		ConnIndex:         int32(connIndex),
		Endpoint:          endpoint,
		MsgCount:          cs.Count.Load(),
		ByteCount:         cs.Bytes.Load(),
		RawCount:          cs.RawCount.Load(),
		RawBytes:          cs.RawBytes.Load(),
		DisconnectCount:   cs.DisconnectCount.Load(),
		ReconnectCount:    cs.ReconnectCount.Load(),
		FirstMsgReceived:  cs.FirstMsg.Load(),
		ConnActive:        cs.ConnActive.Load(),
		LatencyHistogram:  latBytes,
		ReconnectHistogram: reconnBytes,
	}, nil
}

func GroupStatsToProto(gs *GroupStats, name string, endpoints []string, conns int) (*pb.GroupResult, error) {
	merged := MergeGroupHistogram(gs)
	mergedBytes, err := EncodeHistogram(merged)
	if err != nil {
		return nil, err
	}

	mergedReconn := NewConnStats()
	for _, cs := range gs.Conns {
		mergedReconn.ReconnectHist.Merge(cs.ReconnectHist)
	}
	reconnBytes, err := EncodeHistogram(mergedReconn.ReconnectHist)
	if err != nil {
		return nil, err
	}

	connResults := make([]*pb.ConnResult, 0, len(gs.Conns))
	for ci, cs := range gs.Conns {
		epIdx := ci % len(endpoints)
		cr, err := ConnStatsToProto(cs, ci, endpoints[epIdx])
		if err != nil {
			return nil, err
		}
		connResults = append(connResults, cr)
	}

	return &pb.GroupResult{
		Name:               name,
		Connections:        connResults,
		LatencyHistogram:   mergedBytes,
		ReconnectHistogram: reconnBytes,
	}, nil
}
