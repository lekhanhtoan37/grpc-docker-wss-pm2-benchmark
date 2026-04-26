package coordinator

import "benchmark-client/internal/stats"

func ShardGroups(allGroups []stats.Group, numWorkers int) [][]stats.Group {
	if numWorkers <= 0 {
		numWorkers = 1
	}
	result := make([][]stats.Group, numWorkers)
	for i := range allGroups {
		workerIdx := i % numWorkers
		result[workerIdx] = append(result[workerIdx], allGroups[i])
	}
	return result
}
