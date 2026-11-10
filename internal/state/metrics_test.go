package state

import "testing"

func TestMetricsRecordCacheCreation(t *testing.T) {
	m := &metricsStore{ring: make([]RequestRecord, ringBufferSize)}
	m.RecordRequest(RequestRecord{InputTokens: 3, CachedTokens: 70, CacheCreationTokens: 20})
	snapshot := m.Snapshot()
	if snapshot.Aggregates.TotalInputTokens != 3 || snapshot.Aggregates.TotalCachedTokens != 70 ||
		snapshot.Aggregates.TotalCacheCreationTokens != 20 || snapshot.Recent[0].CacheCreationTokens != 20 {
		t.Fatalf("unexpected cache metrics: %+v", snapshot)
	}
}
