package grpc

import (
	"reflect"
	"testing"

	ardav1 "github.com/aswinsreeraj/arda/gen/go/arda/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestConverters_OutputSchema(t *testing.T) {
	schemaMap := map[string]any{
		"type":     "object",
		"required": []any{"sentiment", "score"},
		"properties": map[string]any{
			"sentiment": map[string]any{"type": "string"},
			"score":     map[string]any{"type": "number"},
		},
	}
	schemaStruct, err := structpb.NewStruct(schemaMap)
	if err != nil {
		t.Fatalf("failed to create structpb: %v", err)
	}

	protoCfg := &ardav1.AgentConfiguration{
		PromptTemplate:       "Analyze text",
		ReliabilityThreshold: 0.9,
		ExecutionMode:        ardav1.ExecutionMode_EXECUTION_MODE_AUTONOMOUS,
		OutputSchema: &ardav1.OutputSchema{
			Name:        "SentimentAnalysis",
			Description: "Analyzes sentiment",
			Strict:      true,
			Schema:      schemaStruct,
		},
	}

	domainCfg := ProtoToDomainConfig(protoCfg)
	if domainCfg.OutputSchema == nil {
		t.Fatal("expected domain.OutputSchema to be non-nil")
	}
	if domainCfg.OutputSchema.Name != "SentimentAnalysis" {
		t.Errorf("got name %q, want 'SentimentAnalysis'", domainCfg.OutputSchema.Name)
	}
	if !domainCfg.OutputSchema.Strict {
		t.Errorf("got strict %v, want true", domainCfg.OutputSchema.Strict)
	}
	if domainCfg.OutputSchema.Schema["type"] != "object" {
		t.Errorf("got schema type %v, want 'object'", domainCfg.OutputSchema.Schema["type"])
	}

	convertedProto, err := DomainConfigToProto(domainCfg)
	if err != nil {
		t.Fatalf("DomainConfigToProto failed: %v", err)
	}
	if convertedProto.OutputSchema == nil {
		t.Fatal("expected proto.OutputSchema to be non-nil")
	}
	if convertedProto.OutputSchema.Name != "SentimentAnalysis" {
		t.Errorf("got proto name %q, want 'SentimentAnalysis'", convertedProto.OutputSchema.Name)
	}
	convertedMap := convertedProto.OutputSchema.Schema.AsMap()
	if !reflect.DeepEqual(convertedMap["type"], "object") {
		t.Errorf("got convertedMap type %v, want 'object'", convertedMap["type"])
	}
}

func TestConverters_NilOutputSchema(t *testing.T) {
	protoCfg := &ardav1.AgentConfiguration{
		PromptTemplate: "Analyze text",
	}
	domainCfg := ProtoToDomainConfig(protoCfg)
	if domainCfg.OutputSchema != nil {
		t.Errorf("expected nil OutputSchema, got: %v", domainCfg.OutputSchema)
	}

	convertedProto, err := DomainConfigToProto(domainCfg)
	if err != nil {
		t.Fatalf("DomainConfigToProto failed: %v", err)
	}
	if convertedProto.OutputSchema != nil {
		t.Errorf("expected nil proto OutputSchema, got: %v", convertedProto.OutputSchema)
	}
}
