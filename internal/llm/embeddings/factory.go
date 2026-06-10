package llembed

import (
	"context"
	"fmt"
	"os"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	geoemb "github.com/milosgajdos/go-embeddings"
	gebedrock "github.com/milosgajdos/go-embeddings/bedrock"
	gecohere "github.com/milosgajdos/go-embeddings/cohere"
	geopenai "github.com/milosgajdos/go-embeddings/openai"
	gevertex "github.com/milosgajdos/go-embeddings/vertexai"
	gevoyage "github.com/milosgajdos/go-embeddings/voyage"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// NewProviderEmbedder builds an Embedder for embedding_provider using go-embeddings, except Ollama (native /api/embed) and azure_openai (handled in llm.NewEmbedder).
func NewProviderEmbedder(cfg *config.Config, provider, apiKey string) (model.Embedder, error) {
	switch provider {
	case "openai":
		return newOpenAIEmbedder(cfg, apiKey)
	case "cohere":
		return newCohereEmbedder(cfg, apiKey)
	case "voyage":
		return newVoyageEmbedder(cfg, apiKey)
	case "vertex", "vertexai":
		return newVertexEmbedder(cfg, apiKey)
	case "ollama":
		return newNativeOllamaEmbedder(cfg, apiKey)
	case "bedrock":
		return newBedrockEmbedder(cfg)
	default:
		return nil, fmt.Errorf("llembed: unsupported provider %q", provider)
	}
}

func vecsToFloat32(em []*geoemb.Embedding) [][]float32 {
	out := make([][]float32, len(em))
	for i, e := range em {
		out[i] = e.ToFloat32()
	}
	return out
}

func callEmbedWithRetry(ctx context.Context, fn func() ([]*geoemb.Embedding, error)) ([]*geoemb.Embedding, error) {
	var embs []*geoemb.Embedding
	err := RunEmbedRetries(ctx, func() error {
		var err error
		embs, err = fn()
		return err
	})
	return embs, err
}

type geOpenAIEmbedder struct {
	cli   geoemb.Embedder[*geopenai.EmbeddingRequest]
	model geopenai.Model
}

func newOpenAIEmbedder(cfg *config.Config, apiKey string) (model.Embedder, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("openai embeddings: API key required")
	}
	opts := []geopenai.Option{
		geopenai.WithAPIKey(apiKey),
		geopenai.WithHTTPClient(GeHTTP(cfg)),
	}
	if u := trimAPIBase(cfg.LLM.BaseURL); u != "" {
		opts = append(opts, geopenai.WithBaseURL(u))
	}
	cli := geopenai.NewEmbedder(opts...)
	modelStr := strings.TrimSpace(cfg.LLM.EmbeddingModel)
	if modelStr == "" {
		modelStr = string(geopenai.TextSmallV3)
	}
	return &geOpenAIEmbedder{cli: cli, model: geopenai.Model(modelStr)}, nil
}

func (g *geOpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	texts = NormalizeTexts(texts)
	if len(texts) == 0 {
		return nil, nil
	}
	req := &geopenai.EmbeddingRequest{
		Input:          texts,
		Model:          g.model,
		EncodingFormat: geopenai.EncodingFloat,
	}
	embs, err := callEmbedWithRetry(ctx, func() ([]*geoemb.Embedding, error) {
		return g.cli.Embed(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: %w", err)
	}
	if len(embs) != len(texts) {
		return nil, fmt.Errorf("openai embeddings: got %d vectors for %d inputs", len(embs), len(texts))
	}
	return vecsToFloat32(embs), nil
}

type cohereEmbedder struct {
	cli   geoemb.Embedder[*gecohere.EmbeddingRequest]
	model gecohere.Model
}

func newCohereEmbedder(cfg *config.Config, apiKey string) (model.Embedder, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("cohere embeddings: API key required")
	}
	opts := []gecohere.Option{
		gecohere.WithAPIKey(apiKey),
		gecohere.WithHTTPClient(GeHTTP(cfg)),
	}
	if u := trimAPIBase(cfg.LLM.BaseURL); u != "" {
		opts = append(opts, gecohere.WithBaseURL(u))
	}
	cli := gecohere.NewEmbedder(opts...)
	modelStr := strings.TrimSpace(cfg.LLM.EmbeddingModel)
	if modelStr == "" {
		modelStr = string(gecohere.EnglishV3)
	}
	return &cohereEmbedder{cli: cli, model: gecohere.Model(modelStr)}, nil
}

func (c *cohereEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	texts = NormalizeTexts(texts)
	if len(texts) == 0 {
		return nil, nil
	}
	req := &gecohere.EmbeddingRequest{
		Texts:     texts,
		Model:     c.model,
		InputType: gecohere.SearchDocInput,
		Truncate:  gecohere.EndTrunc,
	}
	embs, err := callEmbedWithRetry(ctx, func() ([]*geoemb.Embedding, error) {
		return c.cli.Embed(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("cohere embeddings: %w", err)
	}
	if len(embs) != len(texts) {
		return nil, fmt.Errorf("cohere embeddings: got %d vectors for %d inputs", len(embs), len(texts))
	}
	return vecsToFloat32(embs), nil
}

type voyageEmbedder struct {
	cli   geoemb.Embedder[*gevoyage.EmbeddingRequest]
	model gevoyage.Model
}

func newVoyageEmbedder(cfg *config.Config, apiKey string) (model.Embedder, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("voyage embeddings: API key required")
	}
	opts := []gevoyage.Option{
		gevoyage.WithAPIKey(apiKey),
		gevoyage.WithHTTPClient(GeHTTP(cfg)),
	}
	if u := trimAPIBase(cfg.LLM.BaseURL); u != "" {
		opts = append(opts, gevoyage.WithBaseURL(u))
	}
	cli := gevoyage.NewEmbedder(opts...)
	modelStr := strings.TrimSpace(cfg.LLM.EmbeddingModel)
	if modelStr == "" {
		modelStr = string(gevoyage.VoyageV2)
	}
	return &voyageEmbedder{cli: cli, model: gevoyage.Model(modelStr)}, nil
}

func (v *voyageEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	texts = NormalizeTexts(texts)
	if len(texts) == 0 {
		return nil, nil
	}
	req := &gevoyage.EmbeddingRequest{
		Input:          texts,
		Model:          v.model,
		EncodingFormat: gevoyage.EncodingNone,
	}
	embs, err := callEmbedWithRetry(ctx, func() ([]*geoemb.Embedding, error) {
		return v.cli.Embed(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("voyage embeddings: %w", err)
	}
	if len(embs) != len(texts) {
		return nil, fmt.Errorf("voyage embeddings: got %d vectors for %d inputs", len(embs), len(texts))
	}
	return vecsToFloat32(embs), nil
}

type vertexEmbedder struct {
	cli geoemb.Embedder[*gevertex.EmbeddingRequest]
}

func newVertexEmbedder(cfg *config.Config, apiKey string) (model.Embedder, error) {
	token := strings.TrimSpace(apiKey)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("VERTEXAI_TOKEN"))
	}
	if token == "" {
		return nil, fmt.Errorf("vertex embeddings: set llm.embedding_api_key or VERTEXAI_TOKEN")
	}
	project := strings.TrimSpace(os.Getenv("GOOGLE_PROJECT_ID"))
	if project == "" {
		return nil, fmt.Errorf("vertex embeddings: GOOGLE_PROJECT_ID env required")
	}
	modelID := strings.TrimSpace(cfg.LLM.EmbeddingModel)
	if modelID == "" {
		modelID = strings.TrimSpace(os.Getenv("VERTEXAI_MODEL_ID"))
	}
	if modelID == "" {
		return nil, fmt.Errorf("vertex embeddings: llm.embedding_model or VERTEXAI_MODEL_ID required")
	}
	opts := []gevertex.Option{
		gevertex.WithToken(token),
		gevertex.WithProjectID(project),
		gevertex.WithModelID(modelID),
		gevertex.WithHTTPClient(GeHTTP(cfg)),
	}
	if u := trimAPIBase(cfg.LLM.BaseURL); u != "" {
		opts = append(opts, gevertex.WithBaseURL(u))
	}
	cli := gevertex.NewEmbedder(opts...)
	return &vertexEmbedder{cli: cli}, nil
}

func (v *vertexEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	texts = NormalizeTexts(texts)
	if len(texts) == 0 {
		return nil, nil
	}
	inst := make([]gevertex.Instance, len(texts))
	for i, t := range texts {
		inst[i] = gevertex.Instance{
			TaskType: gevertex.RetrDocTask,
			Content:  t,
		}
	}
	req := &gevertex.EmbeddingRequest{
		Instances: inst,
		Params:    gevertex.Params{AutoTruncate: true},
	}
	embs, err := callEmbedWithRetry(ctx, func() ([]*geoemb.Embedding, error) {
		return v.cli.Embed(ctx, req)
	})
	if err != nil {
		return nil, fmt.Errorf("vertex embeddings: %w", err)
	}
	if len(embs) != len(texts) {
		return nil, fmt.Errorf("vertex embeddings: got %d vectors for %d inputs", len(embs), len(texts))
	}
	return vecsToFloat32(embs), nil
}

type bedrockEmbedder struct {
	cli *gebedrock.Client
}

func newBedrockEmbedder(cfg *config.Config) (model.Embedder, error) {
	modelID := strings.TrimSpace(cfg.LLM.EmbeddingModel)
	if modelID == "" {
		modelID = strings.TrimSpace(os.Getenv("AWS_BEDROCK_MODEL_ID"))
	}
	if modelID == "" {
		return nil, fmt.Errorf("bedrock embeddings: llm.embedding_model or AWS_BEDROCK_MODEL_ID required")
	}
	region := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if region == "" {
		region = gebedrock.DefaultRegion
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("bedrock: aws config: %w", err)
	}
	br := bedrockruntime.NewFromConfig(awsCfg)
	cli := gebedrock.NewClient(
		gebedrock.WithBedrockClient(br),
		gebedrock.WithModelID(modelID),
		gebedrock.WithRegion(region),
	)
	return &bedrockEmbedder{cli: cli}, nil
}

func (b *bedrockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	texts = NormalizeTexts(texts)
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		req := &gebedrock.Request{InputText: t}
		embs, err := callEmbedWithRetry(ctx, func() ([]*geoemb.Embedding, error) {
			return b.cli.Embed(ctx, req)
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock embeddings: %w", err)
		}
		if len(embs) != 1 || len(embs[0].Vector) == 0 {
			return nil, fmt.Errorf("bedrock embeddings: unexpected response for input %d", i)
		}
		out[i] = embs[0].ToFloat32()
	}
	return out, nil
}
