package stats

import (
	"sync/atomic"

	"github.com/HdrHistogram/hdrhistogram-go"
)

type Group struct {
	Name      string
	Type      string
	Endpoints []string
}

type ConnStats struct {
	Hist            *hdrhistogram.Histogram
	ReconnectHist   *hdrhistogram.Histogram
	Count           atomic.Int64
	Bytes           atomic.Int64
	RawCount        atomic.Int64
	RawBytes        atomic.Int64
	FirstMsg        atomic.Bool
	ConnActive      atomic.Bool
	DisconnectCount atomic.Int64
	ReconnectCount  atomic.Int64
}

type GroupStats struct {
	Conns []*ConnStats
}

func NewConnStats() *ConnStats {
	return &ConnStats{
		Hist:          hdrhistogram.New(1, 3600000000, 3),
		ReconnectHist: hdrhistogram.New(1, 30000000, 3),
	}
}

func NewGroupStats(conns int) *GroupStats {
	gs := &GroupStats{
		Conns: make([]*ConnStats, conns),
	}
	for i := range gs.Conns {
		gs.Conns[i] = NewConnStats()
	}
	return gs
}
