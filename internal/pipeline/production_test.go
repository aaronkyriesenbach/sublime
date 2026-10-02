package pipeline_test

import (
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/config"
	"github.com/aaronkyriesenbach/sublime/internal/pipeline"
)

func TestNewProduction_WhisperOnlyChainNeedsNoSecrets(t *testing.T) {
	whisper := config.ProviderConfig{Name: "whisper", Endpoint: "http://whisper:8080", WorkerCount: 1}

	primary, statuses, tiers, err := pipeline.NewProduction(pipeline.ProductionConfig{
		ProviderChain: []config.ProviderConfig{whisper},
		ProviderTiers: []config.ProviderTier{{Providers: []config.ProviderConfig{whisper}}},
		WorkerCount:   8,
	})
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if primary == nil || len(statuses) != 1 || len(tiers) != 1 {
		t.Fatalf("primary=%v statuses=%d tiers=%d, want one whisper Provider", primary, len(statuses), len(tiers))
	}
	if statuses[0].Name != "whisper" || statuses[0].WorkerCount != 1 {
		t.Errorf("status = %+v, want whisper with its own worker count of 1, not the generic 8", statuses[0])
	}
}

func TestNewProduction_WhisperStatusReportsSuspension(t *testing.T) {
	whisper := config.ProviderConfig{Name: "whisper", Endpoint: "http://whisper:8080", WorkerCount: 1}

	_, statuses, _, err := pipeline.NewProduction(pipeline.ProductionConfig{
		ProviderChain: []config.ProviderConfig{whisper},
		ProviderTiers: []config.ProviderTier{{Providers: []config.ProviderConfig{whisper}}},
		WorkerCount:   8,
	})
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if statuses[0].Suspension == nil {
		t.Fatal("whisper status has no Suspension callback, so /status could never show it Suspended")
	}
	if _, suspended := statuses[0].Suspension(); suspended {
		t.Error("a freshly built whisper Provider reports Suspended, want not suspended")
	}
}
