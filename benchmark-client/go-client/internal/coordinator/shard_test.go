package coordinator

import (
	"testing"

	"benchmark-client/internal/stats"
)

func TestShardGroups_TwoWorkers(t *testing.T) {
	groups := []stats.Group{
		{Name: "A", Type: "ws"},
		{Name: "B", Type: "ws"},
		{Name: "C", Type: "grpc"},
		{Name: "D", Type: "ws"},
	}

	result := ShardGroups(groups, 2)

	if len(result) != 2 {
		t.Fatalf("expected 2 shards, got %d", len(result))
	}
	if len(result[0]) != 2 {
		t.Errorf("shard 0: expected 2 groups, got %d", len(result[0]))
	}
	if len(result[1]) != 2 {
		t.Errorf("shard 1: expected 2 groups, got %d", len(result[1]))
	}

	if result[0][0].Name != "A" || result[0][1].Name != "C" {
		t.Errorf("shard 0: expected [A, C], got [%s, %s]", result[0][0].Name, result[0][1].Name)
	}
	if result[1][0].Name != "B" || result[1][1].Name != "D" {
		t.Errorf("shard 1: expected [B, D], got [%s, %s]", result[1][0].Name, result[1][1].Name)
	}
}

func TestShardGroups_ThreeWorkers(t *testing.T) {
	groups := []stats.Group{
		{Name: "A"}, {Name: "B"}, {Name: "C"},
		{Name: "D"}, {Name: "E"}, {Name: "F"},
		{Name: "G"}, {Name: "H"}, {Name: "I"},
	}

	result := ShardGroups(groups, 3)
	if len(result) != 3 {
		t.Fatalf("expected 3 shards, got %d", len(result))
	}
	for i, shard := range result {
		if len(shard) != 3 {
			t.Errorf("shard %d: expected 3 groups, got %d", i, len(shard))
		}
	}

	if result[0][0].Name != "A" || result[0][1].Name != "D" || result[0][2].Name != "G" {
		t.Errorf("shard 0 wrong: %v", result[0])
	}
}

func TestShardGroups_MoreWorkersThanGroups(t *testing.T) {
	groups := []stats.Group{
		{Name: "A"}, {Name: "B"},
	}

	result := ShardGroups(groups, 5)
	if len(result) != 5 {
		t.Fatalf("expected 5 shards, got %d", len(result))
	}
	if len(result[0]) != 1 || result[0][0].Name != "A" {
		t.Errorf("shard 0 wrong")
	}
	if len(result[1]) != 1 || result[1][0].Name != "B" {
		t.Errorf("shard 1 wrong")
	}
	for i := 2; i < 5; i++ {
		if len(result[i]) != 0 {
			t.Errorf("shard %d should be empty, got %d groups", i, len(result[i]))
		}
	}
}

func TestShardGroups_ZeroWorkers(t *testing.T) {
	groups := []stats.Group{{Name: "A"}, {Name: "B"}}
	result := ShardGroups(groups, 0)
	if len(result) != 1 {
		t.Fatalf("expected 1 shard for numWorkers=0, got %d", len(result))
	}
	if len(result[0]) != 2 {
		t.Errorf("expected 2 groups in single shard, got %d", len(result[0]))
	}
}

func TestShardGroups_AllGroupsPreserved(t *testing.T) {
	groups := []stats.Group{
		{Name: "A"}, {Name: "B"}, {Name: "C"},
		{Name: "D"}, {Name: "E"}, {Name: "F"},
		{Name: "G"}, {Name: "H"}, {Name: "I"},
	}

	result := ShardGroups(groups, 4)
	total := 0
	for _, shard := range result {
		total += len(shard)
	}
	if total != 9 {
		t.Errorf("expected 9 total groups across shards, got %d", total)
	}
}
