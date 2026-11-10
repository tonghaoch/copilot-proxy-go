package state

import "testing"

func TestMetricsRecordCacheCreation(t *testing.T) {
	m := &metricsStore{ring: make([]RequestRecord, ringBufferSize)}
	m.RecordRequest(RequestRecord{InputTokens: 3, CachedTokens: 70, CacheCreationTokens: 20,
		CacheWriteDetailsKnown: true, CacheWrite5mTokens: 5, CacheWrite1hTokens: 15})
	m.RecordRequest(RequestRecord{CacheCreationTokens: 7})
	snapshot := m.Snapshot()
	if snapshot.Aggregates.TotalInputTokens != 3 || snapshot.Aggregates.TotalCachedTokens != 70 ||
		snapshot.Aggregates.TotalCacheCreationTokens != 27 ||
		snapshot.Aggregates.TotalCacheWrite5mTokens != 5 || snapshot.Aggregates.TotalCacheWrite1hTokens != 15 ||
		snapshot.Recent[1].CacheCreationTokens != 20 {
		t.Fatalf("unexpected cache metrics: %+v", snapshot)
	}
}
