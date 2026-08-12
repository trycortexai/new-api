package relayconvert

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatRequestToClaudeMessagesMovesDeveloperMessageToSystem(t *testing.T) {
	maxTokens := uint(64)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "developer", Content: "Follow the developer instruction."},
			{Role: "user", Content: "echo hi"},
		},
	})
	require.NoError(t, err)

	systemMessages, ok := got.System.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, systemMessages, 1)
	assert.Equal(t, "text", systemMessages[0].Type)
	assert.Equal(t, "Follow the developer instruction.", systemMessages[0].GetText())
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
	assert.Equal(t, "echo hi", got.Messages[0].Content)
}

func TestOpenAIChatRequestToClaudeMessagesPreservesSystemAndDeveloperInstructionOrder(t *testing.T) {
	maxTokens := uint(64)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "system", Content: "System instruction."},
			{Role: "developer", Content: []any{
				dto.MediaContent{Type: "text", Text: "First developer instruction."},
				dto.MediaContent{Type: "text", Text: "Second developer instruction."},
			}},
			{Role: "user", Content: "hello"},
		},
	})
	require.NoError(t, err)

	systemMessages, ok := got.System.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, systemMessages, 3)
	assert.Equal(t, "System instruction.", systemMessages[0].GetText())
	assert.Equal(t, "First developer instruction.", systemMessages[1].GetText())
	assert.Equal(t, "Second developer instruction.", systemMessages[2].GetText())
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
}
